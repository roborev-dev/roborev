package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

type finding struct {
	path   string
	line   int
	column int
	reason string
}

func main() {
	fix := flag.Bool("fix", false, "rewrite files to remove redundant guards")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: go run ./scripts/redundant_if_assert_probe.go [-fix] [paths...]\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Scans *_test.go files for redundant if-guards around testify assertions.\n")
	}
	flag.Parse()

	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	fset := token.NewFileSet()
	var findings []finding
	totalFixed := 0

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			fileFindings := scanFile(fset, path, file)
			findings = append(findings, fileFindings...)
			if *fix && len(fileFindings) > 0 {
				fixed := applyFixes(file)
				if fixed > 0 {
					if err := writeFormattedFile(fset, path, file); err != nil {
						return fmt.Errorf("write %s: %w", path, err)
					}
					totalFixed += fixed
				}
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
	}

	if len(findings) == 0 {
		fmt.Println("no redundant if-guarded testify assertions found")
		return
	}

	if *fix {
		fmt.Printf("found %d redundant guards; fixed %d\n", len(findings), totalFixed)
		return
	}

	for _, f := range findings {
		fmt.Printf("%s:%d:%d: %s\n", f.path, f.line, f.column, f.reason)
	}
	os.Exit(1)
}

func scanFile(fset *token.FileSet, path string, file *ast.File) []finding {
	var out []finding
	ast.Inspect(file, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		found, ok := matchRedundantGuard(fset, path, ifStmt)
		if ok {
			out = append(out, found)
		}
		return true
	})
	return out
}

func matchRedundantGuard(fset *token.FileSet, path string, ifStmt *ast.IfStmt) (finding, bool) {
	method, ok := redundantGuardMethod(ifStmt)
	if !ok {
		return finding{}, false
	}

	pos := fset.Position(ifStmt.Pos())
	return finding{
		path:   path,
		line:   pos.Line,
		column: pos.Column,
		reason: fmt.Sprintf("remove redundant if guard; call %s directly", method),
	}, true
}

func redundantGuardMethod(ifStmt *ast.IfStmt) (string, bool) {
	if ifStmt.Init != nil || ifStmt.Else != nil {
		return "", false
	}

	expr, op, negated, ok := parseCondition(ifStmt.Cond)
	if !ok || ifStmt.Body == nil || len(ifStmt.Body.List) != 1 {
		return "", false
	}

	call, ok := stmtCall(ifStmt.Body.List[0])
	if !ok {
		return "", false
	}

	method, argExprs, ok := testifyCall(call)
	if !ok || !argsContainExpr(argExprs, expr) {
		return "", false
	}

	redundant := false
	switch op {
	case token.NEQ:
		redundant = slices.Contains([]string{"NoError", "Nil", "Equal"}, method)
	case token.EQL:
		redundant = slices.Contains([]string{"Error", "NotNil", "NotEqual"}, method)
	}
	if negated {
		redundant = slices.Contains([]string{"True"}, method)
	} else if op == token.ILLEGAL {
		redundant = slices.Contains([]string{"False"}, method)
	}

	if !redundant {
		return "", false
	}
	return method, true
}

func parseCondition(cond ast.Expr) (string, token.Token, bool, bool) {
	switch c := cond.(type) {
	case *ast.BinaryExpr:
		if c.Op != token.EQL && c.Op != token.NEQ {
			return "", token.ILLEGAL, false, false
		}
		left := exprString(c.X)
		right := exprString(c.Y)
		if right == "nil" {
			return left, c.Op, false, true
		}
		if left == "nil" {
			return right, c.Op, false, true
		}
		if c.Op == token.NEQ {
			return left, c.Op, false, true
		}
		return "", token.ILLEGAL, false, false
	case *ast.UnaryExpr:
		if c.Op == token.NOT {
			return exprString(c.X), token.ILLEGAL, true, true
		}
		return "", token.ILLEGAL, false, false
	case *ast.Ident:
		return c.Name, token.ILLEGAL, false, true
	default:
		return "", token.ILLEGAL, false, false
	}
}

func stmtCall(stmt ast.Stmt) (*ast.CallExpr, bool) {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	return call, ok
}

func testifyCall(call *ast.CallExpr) (string, []string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", nil, false
	}
	recv := exprString(sel.X)
	if recv != "assert" && recv != "require" {
		return "", nil, false
	}
	method := sel.Sel.Name
	allowed := map[string]struct{}{
		"NoError":  {},
		"Error":    {},
		"Nil":      {},
		"NotNil":   {},
		"Equal":    {},
		"NotEqual": {},
		"True":     {},
		"False":    {},
	}
	if _, ok := allowed[method]; !ok {
		return "", nil, false
	}

	args := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		args = append(args, exprString(arg))
	}
	return method, args, true
}

func argsContainExpr(args []string, expr string) bool {
	return slices.Contains(args, expr)
}

func exprString(e ast.Expr) string {
	var b strings.Builder
	cfg := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
	_ = cfg.Fprint(&b, token.NewFileSet(), e)
	return b.String()
}

func applyFixes(file *ast.File) int {
	fixed := 0
	ast.Inspect(file, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i, stmt := range block.List {
			ifStmt, ok := stmt.(*ast.IfStmt)
			if !ok {
				continue
			}
			if _, ok := redundantGuardMethod(ifStmt); !ok {
				continue
			}
			replacement, ok := rebuiltExprStmt(ifStmt.Body.List[0])
			if !ok {
				continue
			}
			block.List[i] = replacement
			fixed++
		}
		return true
	})
	return fixed
}

func rebuiltExprStmt(stmt ast.Stmt) (ast.Stmt, bool) {
	call, ok := stmtCall(stmt)
	if !ok {
		return nil, false
	}
	callText, ok := normalizedCallString(call)
	if !ok {
		return nil, false
	}
	expr, err := parser.ParseExpr(callText)
	if err != nil {
		return nil, false
	}
	zeroTokenPositions(reflect.ValueOf(expr))
	return &ast.ExprStmt{X: expr}, true
}

func normalizedCallString(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	parts := make([]string, 0, len(call.Args))
	for _, arg := range call.Args {
		parts = append(parts, exprString(arg))
	}
	return fmt.Sprintf("%s.%s(%s)", exprString(sel.X), sel.Sel.Name, strings.Join(parts, ", ")), true
}

func writeFormattedFile(fset *token.FileSet, path string, file *ast.File) error {
	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func zeroTokenPositions(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return
		}
		zeroTokenPositions(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			ft := v.Type().Field(i)
			if ft.PkgPath != "" {
				continue
			}
			if f.Type() == reflect.TypeFor[token.Pos]() && f.CanSet() {
				f.SetInt(0)
				continue
			}
			zeroTokenPositions(f)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			zeroTokenPositions(v.Index(i))
		}
	}
}

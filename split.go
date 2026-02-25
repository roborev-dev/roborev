package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	fset := token.NewFileSet()
	filename := "cmd/roborev/tui_filter_test.go"
	node, err := parser.ParseFile(fset, filename, nil, parser.ParseComments)
	if err != nil {
		panic(err)
	}

	files := map[string]*ast.File{
		"tui_filter_tree_test.go":   {Name: ast.NewIdent("main")},
		"tui_filter_search_test.go": {Name: ast.NewIdent("main")},
		"tui_filter_stack_test.go":  {Name: ast.NewIdent("main")},
		"tui_filter_queue_test.go":  {Name: ast.NewIdent("main")},
		"tui_filter_test.go":        {Name: ast.NewIdent("main")},
	}

	for _, v := range files {
		for _, imp := range node.Imports {
			v.Decls = append(v.Decls, &ast.GenDecl{
				Tok:   token.IMPORT,
				Specs: []ast.Spec{imp},
			})
		}
	}

	for _, d := range node.Decls {
		if fn, isFn := d.(*ast.FuncDecl); isFn {
			name := fn.Name.Name
			var targetFile string

			if strings.Contains(name, "Tree") || strings.Contains(name, "Branch") || strings.Contains(name, "LazyLoad") || strings.Contains(name, "Expand") || strings.Contains(name, "Collapse") || strings.Contains(name, "RootPath") || name == "countLoading" || name == "countLoaded" {
				if strings.Contains(name, "BranchFilter") {
					targetFile = "tui_filter_queue_test.go"
				} else {
					targetFile = "tui_filter_tree_test.go"
				}
			} else if strings.Contains(name, "Search") || strings.Contains(name, "Typing") || strings.Contains(name, "Backspace") || strings.Contains(name, "HAndL") {
				targetFile = "tui_filter_search_test.go"
			} else if strings.Contains(name, "Stack") || strings.Contains(name, "Escape") || strings.Contains(name, "Clear") || strings.Contains(name, "Pop") || strings.Contains(name, "Locked") || strings.Contains(name, "RemoveFilter") {
				targetFile = "tui_filter_stack_test.go"
			} else if strings.Contains(name, "Queue") || strings.Contains(name, "ZeroVisible") || strings.Contains(name, "Refresh") || strings.Contains(name, "Nav") || strings.Contains(name, "Status") || strings.Contains(name, "Batches") || strings.Contains(name, "Cwd") {
				targetFile = "tui_filter_queue_test.go"
			} else {
				targetFile = "tui_filter_test.go"
			}
			files[targetFile].Decls = append(files[targetFile].Decls, d)
		} else {
			// keep non-functions in the original file
			files["tui_filter_test.go"].Decls = append(files["tui_filter_test.go"].Decls, d)
		}
	}

	for fname, f := range files {
		var buf bytes.Buffer
		err := printer.Fprint(&buf, fset, f)
		if err != nil {
			panic(err)
		}

		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			fmt.Printf("Error formatting %s: %v\n", fname, err)
			formatted = buf.Bytes()
		}

		err = os.WriteFile(filepath.Join("cmd", "roborev", fname), formatted, 0644)
		if err != nil {
			panic(err)
		}
	}
}

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ollamaToolMaxGrepLines = 500
	ollamaToolBashTimeout  = 60
)

// safePathUnderRoot resolves path relative to root and ensures it stays under root.
// Returns an error if path escapes root (e.g. ".." or absolute path outside root).
func safePathUnderRoot(root, path string) (string, error) {
	path = filepath.Clean(path)
	if filepath.IsAbs(path) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve root: %w", err)
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err != nil {
			return "", fmt.Errorf("path outside repo: %s", path)
		}
		if strings.HasPrefix(rel, "..") || rel == ".." {
			return "", fmt.Errorf("path outside repo: %s", path)
		}
		return filepath.Join(absRoot, rel), nil
	}
	// Relative path: join and ensure result is under root
	joined := filepath.Join(root, path)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(absRoot, absJoined)
	if err != nil {
		return "", fmt.Errorf("path outside repo: %s", path)
	}
	if strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path outside repo: %s", path)
	}
	return absJoined, nil
}

// getStringArg returns a string from args by key, or "" if missing/wrong type.
func getStringArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ExecuteTool runs a single tool by name with the given args under repoPath.
// When agentic is false, only Read, Glob, and Grep are allowed.
// When agentic is true, Edit, Write, and Bash are also allowed.
// Returns the tool output as a string, or an error message string for the model.
func ExecuteTool(ctx context.Context, repoPath, name string, args map[string]any, agentic bool) (string, error) {
	switch name {
	case "Read":
		return executeRead(repoPath, args)
	case "Glob":
		return executeGlob(repoPath, args)
	case "Grep":
		return executeGrep(ctx, repoPath, args)
	case "Edit", "Write", "Bash":
		if !agentic {
			return "", fmt.Errorf("tool %s requires agentic mode", name)
		}
		switch name {
		case "Edit":
			return executeEdit(repoPath, args)
		case "Write":
			return executeWrite(repoPath, args)
		case "Bash":
			return executeBash(ctx, repoPath, args)
		}
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

func executeRead(repoPath string, args map[string]any) (string, error) {
	path := getStringArg(args, "file_path")
	if path == "" {
		return "", fmt.Errorf("file_path is required")
	}
	fullPath, err := safePathUnderRoot(repoPath, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

func executeGlob(repoPath string, args map[string]any) (string, error) {
	pattern := getStringArg(args, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	// Restrict pattern to repo: join with repoPath so results are under repo
	searchPath := filepath.Join(repoPath, pattern)
	matches, err := filepath.Glob(searchPath)
	if err != nil {
		return "", fmt.Errorf("glob %s: %w", pattern, err)
	}
	var out []string
	absRepo, _ := filepath.Abs(repoPath)
	for _, m := range matches {
		absM, err := filepath.Abs(m)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRepo, absM)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		// Return paths relative to repo
		out = append(out, filepath.ToSlash(rel))
	}
	return strings.Join(out, "\n"), nil
}

func executeGrep(ctx context.Context, repoPath string, args map[string]any) (string, error) {
	pattern := getStringArg(args, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	pathArg := getStringArg(args, "path")
	var searchDir string
	if pathArg != "" {
		var err error
		searchDir, err = safePathUnderRoot(repoPath, pathArg)
		if err != nil {
			return "", err
		}
	} else {
		searchDir = repoPath
	}
	// Use grep -r -n -E with bounded output
	cmd := exec.CommandContext(ctx, "grep", "-r", "-n", "-E", pattern, searchDir)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		// grep exits 1 when no match; treat as empty result
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("grep: %w", err)
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) > ollamaToolMaxGrepLines {
		lines = lines[:ollamaToolMaxGrepLines]
		lines = append(lines, fmt.Sprintf("... truncated to %d lines", ollamaToolMaxGrepLines))
	}
	// Make paths relative to repo for readability
	absRepo, _ := filepath.Abs(repoPath)
	var result []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		// grep output is "path:lineNum:content"; normalize path to repo-relative
		if idx := strings.Index(line, ":"); idx > 0 {
			pathPart := line[:idx]
			absPath, err := filepath.Abs(pathPart)
			if err == nil {
				rel, err := filepath.Rel(absRepo, absPath)
				if err == nil && !strings.HasPrefix(rel, "..") {
					line = filepath.ToSlash(rel) + line[idx:]
				}
			}
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n"), nil
}

func executeEdit(repoPath string, args map[string]any) (string, error) {
	path := getStringArg(args, "file_path")
	oldStr := getStringArg(args, "old_string")
	newStr := getStringArg(args, "new_string")
	if path == "" {
		return "", fmt.Errorf("file_path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("old_string is required")
	}
	fullPath, err := safePathUnderRoot(repoPath, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)
	if !strings.Contains(content, oldStr) {
		return "", fmt.Errorf("old_string not found in %s", path)
	}
	// Replace first occurrence only
	content = strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return "ok", nil
}

func executeWrite(repoPath string, args map[string]any) (string, error) {
	path := getStringArg(args, "file_path")
	contents := getStringArg(args, "contents")
	if path == "" {
		return "", fmt.Errorf("file_path is required")
	}
	fullPath, err := safePathUnderRoot(repoPath, path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return "ok", nil
}

func executeBash(ctx context.Context, repoPath string, args map[string]any) (string, error) {
	command := getStringArg(args, "command")
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	// Run with timeout to avoid runaway commands
	bashCtx, cancel := context.WithTimeout(ctx, ollamaToolBashTimeout*time.Second)
	defer cancel()

	cmd := exec.CommandContext(bashCtx, "sh", "-c", command)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bash: %w\n%s", err, out)
	}
	return string(out), nil
}

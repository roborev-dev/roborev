package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/prompt/analyze"
	"github.com/roborev-dev/roborev/internal/storage"
)

func analyzeCmd() *cobra.Command {
	var (
		agentName string
		model     string
		reasoning string
		wait      bool
		quiet     bool
		listTypes bool
	)

	cmd := &cobra.Command{
		Use:   "analyze <type> <files...>",
		Short: "Run built-in analysis on files",
		Long: `Run a built-in analysis type on one or more files.

This command provides predefined analysis prompts for common code review
tasks like finding duplication, suggesting refactorings, or identifying
test fixture opportunities.

The output is formatted for easy copy-paste into agent sessions, with
a header showing the analysis type and files analyzed.

Available analysis types:
  test-fixtures  Find test fixture and helper opportunities
  duplication    Find code duplication across files
  refactor       Suggest refactoring opportunities
  complexity     Analyze complexity and suggest simplifications
  api-design     Review API consistency and design patterns
  dead-code      Find unused exports and unreachable code
  architecture   Review architectural patterns and structure

Examples:
  roborev analyze test-fixtures internal/storage/*_test.go
  roborev analyze duplication cmd/roborev/*.go
  roborev analyze refactor --wait main.go utils.go
  roborev analyze complexity --agent gemini ./...
  roborev analyze architecture internal/storage/    # analyze a directory
  roborev analyze --list
`,
		Args: func(cmd *cobra.Command, args []string) error {
			if listTypes {
				return nil
			}
			if len(args) < 2 {
				return fmt.Errorf("requires analysis type and at least one file")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if listTypes {
				return listAnalysisTypes(cmd)
			}
			return runAnalysis(cmd, args[0], args[1:], agentName, model, reasoning, wait, quiet)
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to use (default: from config)")
	cmd.Flags().StringVar(&model, "model", "", "model for agent")
	cmd.Flags().StringVar(&reasoning, "reasoning", "", "reasoning level: fast, standard, or thorough")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for job to complete and show result")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress output (just enqueue)")
	cmd.Flags().BoolVar(&listTypes, "list", false, "list available analysis types")

	return cmd
}

func listAnalysisTypes(cmd *cobra.Command) error {
	cmd.Println("Available analysis types:")
	cmd.Println()
	for _, t := range analyze.AllTypes {
		cmd.Printf("  %-14s %s\n", t.Name, t.Description)
	}
	return nil
}

func runAnalysis(cmd *cobra.Command, typeName string, filePatterns []string, agentName, modelStr, reasoningStr string, wait, quiet bool) error {
	// Validate analysis type
	analysisType := analyze.GetType(typeName)
	if analysisType == nil {
		return fmt.Errorf("unknown analysis type %q (use --list to see available types)", typeName)
	}

	// Get working directory and repo root
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	repoRoot := workDir
	if root, err := git.GetRepoRoot(workDir); err == nil {
		repoRoot = root
	}

	// Expand file patterns and read contents
	files, err := expandAndReadFiles(repoRoot, filePatterns)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no files matched the provided patterns")
	}

	if !quiet {
		cmd.Printf("Analyzing %d file(s) with %q analysis...\n", len(files), typeName)
	}

	// Build the full prompt
	fullPrompt, err := analysisType.BuildPrompt(files)
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}

	// Ensure daemon is running
	if err := ensureDaemon(); err != nil {
		return err
	}

	// Build the request
	reqBody, _ := json.Marshal(map[string]interface{}{
		"repo_path":     repoRoot,
		"git_ref":       "analyze",
		"agent":         agentName,
		"model":         modelStr,
		"reasoning":     reasoningStr,
		"custom_prompt": fullPrompt,
		"agentic":       false, // Analysis is read-only
	})

	resp, err := http.Post(serverAddr+"/api/enqueue", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("enqueue failed: %s", body)
	}

	var job storage.ReviewJob
	if err := json.Unmarshal(body, &job); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !quiet {
		cmd.Printf("Enqueued analysis job %d (agent: %s)\n", job.ID, job.Agent)
	}

	// If --wait, poll until job completes and show result
	if wait {
		return waitForPromptJob(cmd, serverAddr, job.ID, quiet)
	}

	return nil
}

// expandAndReadFiles expands glob patterns and reads file contents.
// Returns a map of relative path -> content.
func expandAndReadFiles(repoRoot string, patterns []string) (map[string]string, error) {
	files := make(map[string]string)
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		// Handle ./... pattern (all files recursively)
		if pattern == "./..." || pattern == "..." {
			if err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					// Skip hidden directories and common non-code directories
					base := filepath.Base(path)
					if strings.HasPrefix(base, ".") || base == "node_modules" || base == "vendor" {
						return filepath.SkipDir
					}
					return nil
				}
				// Only include source files
				if isSourceFile(path) {
					relPath, _ := filepath.Rel(repoRoot, path)
					if !seen[relPath] {
						seen[relPath] = true
						content, err := os.ReadFile(path)
						if err != nil {
							return fmt.Errorf("read %s: %w", relPath, err)
						}
						files[relPath] = string(content)
					}
				}
				return nil
			}); err != nil {
				return nil, err
			}
			continue
		}

		// Make pattern absolute if relative
		absPattern := pattern
		if !filepath.IsAbs(pattern) {
			absPattern = filepath.Join(repoRoot, pattern)
		}

		// Expand glob pattern
		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}

		if len(matches) == 0 {
			// Try as literal file
			if _, err := os.Stat(absPattern); err == nil {
				matches = []string{absPattern}
			} else {
				return nil, fmt.Errorf("no files match pattern %q", pattern)
			}
		}

		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				continue
			}

			if info.IsDir() {
				// If directory, include all source files in it
				filepath.Walk(match, func(path string, info os.FileInfo, err error) error {
					if err != nil || info.IsDir() {
						return err
					}
					if isSourceFile(path) {
						relPath, _ := filepath.Rel(repoRoot, path)
						if !seen[relPath] {
							seen[relPath] = true
							content, err := os.ReadFile(path)
							if err != nil {
								return nil // Skip files we can't read
							}
							files[relPath] = string(content)
						}
					}
					return nil
				})
			} else {
				relPath, _ := filepath.Rel(repoRoot, match)
				if !seen[relPath] {
					seen[relPath] = true
					content, err := os.ReadFile(match)
					if err != nil {
						return nil, fmt.Errorf("read %s: %w", relPath, err)
					}
					files[relPath] = string(content)
				}
			}
		}
	}

	return files, nil
}

// isSourceFile returns true if the file looks like source code
func isSourceFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	sourceExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
		".rs": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true, ".cc": true,
		".java": true, ".kt": true, ".scala": true, ".rb": true, ".php": true,
		".swift": true, ".m": true, ".cs": true, ".fs": true, ".vb": true,
		".sh": true, ".bash": true, ".zsh": true, ".fish": true,
		".sql": true, ".graphql": true, ".proto": true,
		".yaml": true, ".yml": true, ".toml": true, ".json": true,
		".md": true, ".txt": true, ".html": true, ".css": true, ".scss": true,
	}
	return sourceExts[ext]
}

// sortedKeys returns sorted keys from a map for deterministic output
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestIsBrokenPipe(t *testing.T) {
	if isBrokenPipe(nil) {
		t.Error("nil should not be broken pipe")
	}
	if isBrokenPipe(fmt.Errorf("other error")) {
		t.Error("non-EPIPE error should not be broken pipe")
	}
	if !isBrokenPipe(syscall.EPIPE) {
		t.Error("bare EPIPE should be broken pipe")
	}
	if !isBrokenPipe(fmt.Errorf("write: %w", syscall.EPIPE)) {
		t.Error("wrapped EPIPE should be broken pipe")
	}
}

func TestLogCleanCmd(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError string
	}{
		{"NegativeDays", []string{"--days", "-1"}, "--days must be between 0 and 3650"},
		{"OverflowDays", []string{"--days", "999999"}, "--days must be between 0 and 3650"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := logCleanCmd()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantError)
			} else if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantError, err)
			}
		})
	}
}

func TestLogCmd(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantError string
		wantOut   string
		setup     func(dir string)
	}{
		{"InvalidJobID", []string{"abc"}, "invalid job ID", "", nil},
		{"MissingLogFile", []string{"99999"}, "no log for job", "", nil},
		{"PathFlag", []string{"--path", "42"}, "", "logs/jobs/42.log", nil},
		{"RawFlag", []string{"--raw", "42"}, "", `{"type":"assistant"}` + "\n", func(dir string) {
			logDir := filepath.Join(dir, "logs", "jobs")
			os.MkdirAll(logDir, 0755)
			logPath := filepath.Join(logDir, "42.log")
			rawContent := `{"type":"assistant"}` + "\n"
			os.WriteFile(logPath, []byte(rawContent), 0644)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ROBOREV_DATA_DIR", dir)
			if tt.setup != nil {
				tt.setup(dir)
			}

			var buf bytes.Buffer
			cmd := logCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(&buf)
			cmd.SilenceUsage = true

			err := cmd.Execute()
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			out := buf.String()
			switch tt.name {
			case "PathFlag":
				out = strings.TrimSpace(out)
				want := filepath.Join(dir, tt.wantOut)
				if out != want {
					t.Errorf("output %q doesn't match expected %q", out, want)
				}
			case "RawFlag":
				if out != tt.wantOut {
					t.Errorf("output %q doesn't match expected %q", out, tt.wantOut)
				}
			default:
				if !strings.HasSuffix(strings.TrimSpace(out), tt.wantOut) {
					t.Errorf("output %q doesn't match expected %q", out, tt.wantOut)
				}
			}
		})
	}
}

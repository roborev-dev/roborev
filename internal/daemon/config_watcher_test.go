package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

// configWatcherHarness encapsulates the watcher, broadcaster, temp paths, and event channel.
type configWatcherHarness struct {
	Watcher     *ConfigWatcher
	Broadcaster Broadcaster
	ConfigPath  string
	EventCh     <-chan Event
	dir         string
}

const (
	eventConfigReloaded = "config.reloaded"
	eventConfigFailed   = "config.reload.failed"
	reloadTimeout       = 2 * time.Second
)

// assertEqual is a generic test helper
func assertEqual[T comparable](t *testing.T, want, got T, msg string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %v, got %v", msg, want, got)
	}
}

func newUnstartedHarness(t *testing.T, initialConfig string) *configWatcherHarness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	writeTestFile(t, path, initialConfig)

	cfg, err := config.LoadGlobalFrom(path)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	bc := NewBroadcaster()
	_, ch := bc.Subscribe("")
	cw := NewConfigWatcher(path, cfg, bc, nil)

	return &configWatcherHarness{
		Watcher:     cw,
		Broadcaster: bc,
		ConfigPath:  path,
		EventCh:     ch,
		dir:         dir,
	}
}

func newConfigWatcherHarness(t *testing.T, initialConfig string) *configWatcherHarness {
	t.Helper()
	h := newUnstartedHarness(t, initialConfig)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := h.Watcher.Start(ctx); err != nil {
		t.Fatalf("Failed to start watcher: %v", err)
	}
	t.Cleanup(h.Watcher.Stop)

	return h
}

func setupUnstartedWatcher(t *testing.T, configPath string) *ConfigWatcher {
	t.Helper()
	cfg := &config.Config{DefaultAgent: "test"}
	broadcaster := NewBroadcaster()
	return NewConfigWatcher(configPath, cfg, broadcaster, nil)
}

func (h *configWatcherHarness) updateConfig(t *testing.T, content string) {
	t.Helper()
	writeTestFile(t, h.ConfigPath, content)
}

func (h *configWatcherHarness) updateConfigAndWait(t *testing.T, content string) {
	t.Helper()
	h.updateConfig(t, content)
	h.waitForReload(t)
}

func (h *configWatcherHarness) waitForEvent(t *testing.T, eventType string) {
	t.Helper()
	timeout := time.After(reloadTimeout)
	for {
		select {
		case event := <-h.EventCh:
			if event.Type == eventType {
				return
			}
		case <-timeout:
			t.Fatalf("Timeout waiting for %s event", eventType)
		}
	}
}

func (h *configWatcherHarness) waitForReload(t *testing.T) {
	t.Helper()
	h.waitForEvent(t, eventConfigReloaded)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write %s: %v", filepath.Base(path), err)
	}
}

func TestStaticConfig(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "test-agent",
		MaxWorkers:   5,
	}

	sc := NewStaticConfig(cfg)

	// Should always return the same config
	assertEqual(t, cfg, sc.Config(), "StaticConfig.Config() should return the same config object")

	// Call multiple times to verify consistency
	for range 3 {
		assertEqual(t, "test-agent", sc.Config().DefaultAgent, "StaticConfig.Config().DefaultAgent")
	}
}

func TestNewConfigWatcher(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "initial-agent",
		MaxWorkers:   3,
	}
	broadcaster := NewBroadcaster()

	cw := NewConfigWatcher("/path/to/config.toml", cfg, broadcaster, nil)

	assertEqual(t, cfg, cw.Config(), "NewConfigWatcher should store the initial config")
	assertEqual(t, "/path/to/config.toml", cw.configPath, "configPath")

	// LastReloadedAt should be zero initially
	assertEqual(t, true, cw.LastReloadedAt().IsZero(), "LastReloadedAt should be zero initially")
}

func TestConfigWatcher_NoConfigPath(t *testing.T) {
	cw := setupUnstartedWatcher(t, "")

	ctx := t.Context()

	err := cw.Start(ctx)
	if err != nil {
		t.Errorf("Start with empty configPath should not error, got: %v", err)
	}

	// Stop should not panic
	cw.Stop()
}

func TestConfigWatcher_Reloads(t *testing.T) {
	tests := []struct {
		name          string
		initialConfig string
		updateConfig  string
		validate      func(*testing.T, *config.Config)
	}{
		{
			name: "Update Agent and Workers",
			initialConfig: `
default_agent = "initial-agent"
max_workers = 2
`,
			updateConfig: `
default_agent = "updated-agent"
max_workers = 4
`,
			validate: func(t *testing.T, c *config.Config) {
				assertEqual(t, "updated-agent", c.DefaultAgent, "DefaultAgent")
				assertEqual(t, 4, c.MaxWorkers, "MaxWorkers")
			},
		},
		{
			name: "Update Hot Reloadable Settings",
			initialConfig: `
default_agent = "initial-agent"
review_context_count = 3
job_timeout_minutes = 10
`,
			updateConfig: `
default_agent = "updated-agent"
review_context_count = 7
job_timeout_minutes = 30
`,
			validate: func(t *testing.T, c *config.Config) {
				assertEqual(t, 7, c.ReviewContextCount, "ReviewContextCount")
				assertEqual(t, 30, c.JobTimeoutMinutes, "JobTimeoutMinutes")
				assertEqual(t, "updated-agent", c.DefaultAgent, "DefaultAgent")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConfigWatcherHarness(t, tt.initialConfig)

			// Verify initial state
			assertEqual(t, true, h.Watcher.LastReloadedAt().IsZero(), "LastReloadedAt should be zero initially")

			h.updateConfigAndWait(t, tt.updateConfig)

			tt.validate(t, h.Watcher.Config())

			// Verify LastReloadedAt was updated and is recent
			assertEqual(t, false, h.Watcher.LastReloadedAt().IsZero(), "LastReloadedAt should not be zero after reload")
			if time.Since(h.Watcher.LastReloadedAt()) > 5*time.Second {
				t.Errorf("LastReloadedAt should be recent (within 5s), got %v", h.Watcher.LastReloadedAt())
			}
		})
	}
}

func TestConfigWatcher_InvalidConfigDoesNotCrash(t *testing.T) {
	h := newConfigWatcherHarness(t, `default_agent = "test-agent"`)

	// Write invalid TOML - this should not crash the watcher
	h.updateConfig(t, `this is not valid toml [[[`)

	h.waitForEvent(t, eventConfigFailed)

	// Config should still be the original value
	assertEqual(t, "test-agent", h.Watcher.Config().DefaultAgent, "Config should not change on invalid TOML")

	// Watcher should still be working - fix the config
	h.updateConfigAndWait(t, `default_agent = "fixed-agent"`)

	// Now config should be updated
	assertEqual(t, "fixed-agent", h.Watcher.Config().DefaultAgent, "Config should update after fix")
}

func TestConfigGetter_Interface(t *testing.T) {
	// Verify both StaticConfig and ConfigWatcher implement ConfigGetter
	var _ ConfigGetter = (*StaticConfig)(nil)
	var _ ConfigGetter = (*ConfigWatcher)(nil)
}

func TestConfigWatcher_DoubleStopSafe(t *testing.T) {
	cw := setupUnstartedWatcher(t, "")

	// Multiple Stop() calls should not panic
	cw.Stop()
	cw.Stop()
	cw.Stop()
}

func TestConfigWatcher_StopAfterStart(t *testing.T) {
	h := newConfigWatcherHarness(t, `default_agent = "test"`)

	// Stop multiple times should not panic (cleanup will also call Stop)
	h.Watcher.Stop()
	h.Watcher.Stop()
}

func TestConfigWatcher_StartAfterStopErrors(t *testing.T) {
	h := newUnstartedHarness(t, `default_agent = "test"`)
	ctx := context.Background()

	// Start and stop
	if err := h.Watcher.Start(ctx); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	h.Watcher.Stop()

	// Start after Stop should error (not restart-safe)
	err := h.Watcher.Start(ctx)
	if err == nil {
		t.Error("Expected error when calling Start after Stop")
	}
}

func TestConfigWatcher_ReloadCounter(t *testing.T) {
	h := newConfigWatcherHarness(t, `default_agent = "v1"`)

	// Initial counter should be 0
	assertEqual(t, uint64(0), h.Watcher.ReloadCounter(), "Initial ReloadCounter")

	// First reload
	h.updateConfigAndWait(t, `default_agent = "v2"`)
	assertEqual(t, uint64(1), h.Watcher.ReloadCounter(), "After first reload")

	// Second reload
	h.updateConfigAndWait(t, `default_agent = "v3"`)
	assertEqual(t, uint64(2), h.Watcher.ReloadCounter(), "After second reload")
}

func TestConfigWatcher_AtomicSaveViaRename(t *testing.T) {
	h := newConfigWatcherHarness(t, `default_agent = "original"`)

	// Simulate atomic save: write to temp file then rename
	tmpFile := filepath.Join(h.dir, "config.toml.tmp")
	writeTestFile(t, tmpFile, `default_agent = "atomic-saved"`)
	if err := os.Rename(tmpFile, h.ConfigPath); err != nil {
		t.Fatalf("Failed to rename: %v", err)
	}

	h.waitForReload(t)

	// Verify config was updated
	assertEqual(t, "atomic-saved", h.Watcher.Config().DefaultAgent, "After atomic save")
}

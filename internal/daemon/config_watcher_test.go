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

func newConfigWatcherHarness(t *testing.T, initialConfig string) *configWatcherHarness {
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
	cw := NewConfigWatcher(path, cfg, bc)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start watcher: %v", err)
	}
	t.Cleanup(cw.Stop)

	return &configWatcherHarness{
		Watcher:     cw,
		Broadcaster: bc,
		ConfigPath:  path,
		EventCh:     ch,
		dir:         dir,
	}
}

func (h *configWatcherHarness) updateConfig(t *testing.T, content string) {
	t.Helper()
	writeTestFile(t, h.ConfigPath, content)
}

func (h *configWatcherHarness) waitForReload(t *testing.T) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case event := <-h.EventCh:
			if event.Type == "config.reloaded" {
				return
			}
		case <-timeout:
			t.Fatal("Timeout waiting for config.reloaded event")
		}
	}
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
	if sc.Config() != cfg {
		t.Error("StaticConfig.Config() should return the same config object")
	}

	// Call multiple times to verify consistency
	for i := 0; i < 3; i++ {
		if sc.Config().DefaultAgent != "test-agent" {
			t.Errorf("StaticConfig.Config().DefaultAgent = %q, want %q", sc.Config().DefaultAgent, "test-agent")
		}
	}
}

func TestNewConfigWatcher(t *testing.T) {
	cfg := &config.Config{
		DefaultAgent: "initial-agent",
		MaxWorkers:   3,
	}
	broadcaster := NewBroadcaster()

	cw := NewConfigWatcher("/path/to/config.toml", cfg, broadcaster)

	if cw.Config() != cfg {
		t.Error("NewConfigWatcher should store the initial config")
	}

	if cw.configPath != "/path/to/config.toml" {
		t.Errorf("configPath = %q, want %q", cw.configPath, "/path/to/config.toml")
	}

	// LastReloadedAt should be zero initially
	if !cw.LastReloadedAt().IsZero() {
		t.Error("LastReloadedAt should be zero initially")
	}
}

func TestConfigWatcher_NoConfigPath(t *testing.T) {
	cfg := &config.Config{DefaultAgent: "test"}
	broadcaster := NewBroadcaster()

	// When configPath is empty, Start should be a no-op
	cw := NewConfigWatcher("", cfg, broadcaster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cw.Start(ctx)
	if err != nil {
		t.Errorf("Start with empty configPath should not error, got: %v", err)
	}

	// Stop should not panic
	cw.Stop()
}

func TestConfigWatcher_ReloadOnFileChange(t *testing.T) {
	h := newConfigWatcherHarness(t, "default_agent = \"initial-agent\"\nmax_workers = 2\n")

	// Verify initial state
	if h.Watcher.Config().DefaultAgent != "initial-agent" {
		t.Errorf("Initial DefaultAgent = %q, want %q", h.Watcher.Config().DefaultAgent, "initial-agent")
	}
	if !h.Watcher.LastReloadedAt().IsZero() {
		t.Error("LastReloadedAt should be zero before reload")
	}

	// Update config file
	h.updateConfig(t, "default_agent = \"updated-agent\"\nmax_workers = 4\n")
	h.waitForReload(t)

	// Verify config was updated
	if h.Watcher.Config().DefaultAgent != "updated-agent" {
		t.Errorf("After reload, DefaultAgent = %q, want %q", h.Watcher.Config().DefaultAgent, "updated-agent")
	}
	if h.Watcher.Config().MaxWorkers != 4 {
		t.Errorf("After reload, MaxWorkers = %d, want %d", h.Watcher.Config().MaxWorkers, 4)
	}

	// Verify LastReloadedAt was set
	if h.Watcher.LastReloadedAt().IsZero() {
		t.Error("LastReloadedAt should be set after reload")
	}
	if time.Since(h.Watcher.LastReloadedAt()) > 5*time.Second {
		t.Error("LastReloadedAt should be recent")
	}
}

func TestConfigWatcher_InvalidConfigDoesNotCrash(t *testing.T) {
	h := newConfigWatcherHarness(t, "default_agent = \"test-agent\"\n")

	// Write invalid TOML - this should not crash the watcher
	h.updateConfig(t, "this is not valid toml [[[\n")

	// Wait for debounce and reload attempt
	time.Sleep(500 * time.Millisecond)

	// Config should still be the original value
	if h.Watcher.Config().DefaultAgent != "test-agent" {
		t.Errorf("Config should not change on invalid TOML, DefaultAgent = %q", h.Watcher.Config().DefaultAgent)
	}

	// Watcher should still be working - fix the config
	h.updateConfig(t, "default_agent = \"fixed-agent\"\n")

	// Wait for reload
	time.Sleep(500 * time.Millisecond)

	// Now config should be updated
	if h.Watcher.Config().DefaultAgent != "fixed-agent" {
		t.Errorf("After fix, DefaultAgent = %q, want %q", h.Watcher.Config().DefaultAgent, "fixed-agent")
	}
}

func TestConfigGetter_Interface(t *testing.T) {
	// Verify both StaticConfig and ConfigWatcher implement ConfigGetter
	var _ ConfigGetter = (*StaticConfig)(nil)
	var _ ConfigGetter = (*ConfigWatcher)(nil)
}

func TestConfigWatcher_HotReloadableSettingsTakeEffect(t *testing.T) {
	h := newConfigWatcherHarness(t, "default_agent = \"initial-agent\"\nreview_context_count = 3\njob_timeout_minutes = 10\n")

	// Verify initial values
	if h.Watcher.Config().ReviewContextCount != 3 {
		t.Errorf("Initial ReviewContextCount = %d, want 3", h.Watcher.Config().ReviewContextCount)
	}
	if h.Watcher.Config().JobTimeoutMinutes != 10 {
		t.Errorf("Initial JobTimeoutMinutes = %d, want 10", h.Watcher.Config().JobTimeoutMinutes)
	}
	initialReloadTime := h.Watcher.LastReloadedAt()

	// Update config with new hot-reloadable values
	h.updateConfig(t, "default_agent = \"updated-agent\"\nreview_context_count = 7\njob_timeout_minutes = 30\n")
	h.waitForReload(t)

	// Verify hot-reloadable settings took effect
	if h.Watcher.Config().ReviewContextCount != 7 {
		t.Errorf("After reload, ReviewContextCount = %d, want 7", h.Watcher.Config().ReviewContextCount)
	}
	if h.Watcher.Config().JobTimeoutMinutes != 30 {
		t.Errorf("After reload, JobTimeoutMinutes = %d, want 30", h.Watcher.Config().JobTimeoutMinutes)
	}
	if h.Watcher.Config().DefaultAgent != "updated-agent" {
		t.Errorf("After reload, DefaultAgent = %q, want %q", h.Watcher.Config().DefaultAgent, "updated-agent")
	}

	// Verify LastReloadedAt changed
	if h.Watcher.LastReloadedAt().Equal(initialReloadTime) {
		t.Error("LastReloadedAt should have changed after reload")
	}
	if h.Watcher.LastReloadedAt().IsZero() {
		t.Error("LastReloadedAt should not be zero after reload")
	}
}

func TestConfigWatcher_DoubleStopSafe(t *testing.T) {
	cfg := &config.Config{DefaultAgent: "test"}
	broadcaster := NewBroadcaster()

	cw := NewConfigWatcher("", cfg, broadcaster)

	// Multiple Stop() calls should not panic
	cw.Stop()
	cw.Stop()
	cw.Stop()
}

func TestConfigWatcher_StopAfterStart(t *testing.T) {
	h := newConfigWatcherHarness(t, "default_agent = \"test\"\n")

	// Stop multiple times should not panic (cleanup will also call Stop)
	h.Watcher.Stop()
	h.Watcher.Stop()
}

func TestConfigWatcher_StartAfterStopErrors(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")

	writeTestFile(t, configPath, "default_agent = \"test\"\n")

	cfg, _ := config.LoadGlobalFrom(configPath)
	broadcaster := NewBroadcaster()
	cw := NewConfigWatcher(configPath, cfg, broadcaster)

	ctx := context.Background()

	// Start and stop
	if err := cw.Start(ctx); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	cw.Stop()

	// Start after Stop should error (not restart-safe)
	err := cw.Start(ctx)
	if err == nil {
		t.Error("Expected error when calling Start after Stop")
	}
}

func TestConfigWatcher_ReloadCounter(t *testing.T) {
	h := newConfigWatcherHarness(t, "default_agent = \"v1\"\n")

	// Initial counter should be 0
	if h.Watcher.ReloadCounter() != 0 {
		t.Errorf("Initial ReloadCounter = %d, want 0", h.Watcher.ReloadCounter())
	}

	// First reload
	h.updateConfig(t, "default_agent = \"v2\"\n")
	h.waitForReload(t)
	if h.Watcher.ReloadCounter() != 1 {
		t.Errorf("After first reload, ReloadCounter = %d, want 1", h.Watcher.ReloadCounter())
	}

	// Second reload
	h.updateConfig(t, "default_agent = \"v3\"\n")
	h.waitForReload(t)
	if h.Watcher.ReloadCounter() != 2 {
		t.Errorf("After second reload, ReloadCounter = %d, want 2", h.Watcher.ReloadCounter())
	}
}

func TestConfigWatcher_AtomicSaveViaRename(t *testing.T) {
	h := newConfigWatcherHarness(t, "default_agent = \"original\"\n")

	// Simulate atomic save: write to temp file then rename
	tmpFile := filepath.Join(h.dir, "config.toml.tmp")
	writeTestFile(t, tmpFile, "default_agent = \"atomic-saved\"\n")
	if err := os.Rename(tmpFile, h.ConfigPath); err != nil {
		t.Fatalf("Failed to rename: %v", err)
	}

	h.waitForReload(t)

	// Verify config was updated
	if h.Watcher.Config().DefaultAgent != "atomic-saved" {
		t.Errorf("After atomic save, DefaultAgent = %q, want %q", h.Watcher.Config().DefaultAgent, "atomic-saved")
	}
}

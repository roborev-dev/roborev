package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
)

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
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Write initial config
	initialConfig := `default_agent = "initial-agent"
max_workers = 2
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Load initial config
	cfg, err := config.LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	// Create broadcaster and subscribe to events
	broadcaster := NewBroadcaster()
	_, eventCh := broadcaster.Subscribe("")

	// Create and start config watcher
	cw := NewConfigWatcher(configPath, cfg, broadcaster)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start config watcher: %v", err)
	}
	defer cw.Stop()

	// Verify initial state
	if cw.Config().DefaultAgent != "initial-agent" {
		t.Errorf("Initial DefaultAgent = %q, want %q", cw.Config().DefaultAgent, "initial-agent")
	}
	if !cw.LastReloadedAt().IsZero() {
		t.Error("LastReloadedAt should be zero before reload")
	}

	// Update config file
	updatedConfig := `default_agent = "updated-agent"
max_workers = 4
`
	if err := os.WriteFile(configPath, []byte(updatedConfig), 0644); err != nil {
		t.Fatalf("Failed to write updated config: %v", err)
	}

	// Wait for the config reload event (with timeout)
	// The watcher has a 200ms debounce, so we need to wait a bit longer
	timeout := time.After(2 * time.Second)
	var eventReceived bool
	for !eventReceived {
		select {
		case event := <-eventCh:
			if event.Type == "config.reloaded" {
				eventReceived = true
			}
		case <-timeout:
			t.Fatal("Timeout waiting for config.reloaded event")
		}
	}

	// Verify config was updated
	if cw.Config().DefaultAgent != "updated-agent" {
		t.Errorf("After reload, DefaultAgent = %q, want %q", cw.Config().DefaultAgent, "updated-agent")
	}
	if cw.Config().MaxWorkers != 4 {
		t.Errorf("After reload, MaxWorkers = %d, want %d", cw.Config().MaxWorkers, 4)
	}

	// Verify LastReloadedAt was set
	if cw.LastReloadedAt().IsZero() {
		t.Error("LastReloadedAt should be set after reload")
	}
	if time.Since(cw.LastReloadedAt()) > 5*time.Second {
		t.Error("LastReloadedAt should be recent")
	}
}

func TestConfigWatcher_InvalidConfigDoesNotCrash(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Write valid initial config
	initialConfig := `default_agent = "test-agent"
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Load initial config
	cfg, err := config.LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	// Create and start config watcher
	broadcaster := NewBroadcaster()
	cw := NewConfigWatcher(configPath, cfg, broadcaster)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start config watcher: %v", err)
	}
	defer cw.Stop()

	// Write invalid TOML - this should not crash the watcher
	invalidConfig := `this is not valid toml [[[
`
	if err := os.WriteFile(configPath, []byte(invalidConfig), 0644); err != nil {
		t.Fatalf("Failed to write invalid config: %v", err)
	}

	// Wait for debounce and reload attempt
	time.Sleep(500 * time.Millisecond)

	// Config should still be the original value
	if cw.Config().DefaultAgent != "test-agent" {
		t.Errorf("Config should not change on invalid TOML, DefaultAgent = %q", cw.Config().DefaultAgent)
	}

	// Watcher should still be working - fix the config
	fixedConfig := `default_agent = "fixed-agent"
`
	if err := os.WriteFile(configPath, []byte(fixedConfig), 0644); err != nil {
		t.Fatalf("Failed to write fixed config: %v", err)
	}

	// Wait for reload
	time.Sleep(500 * time.Millisecond)

	// Now config should be updated
	if cw.Config().DefaultAgent != "fixed-agent" {
		t.Errorf("After fix, DefaultAgent = %q, want %q", cw.Config().DefaultAgent, "fixed-agent")
	}
}

func TestConfigGetter_Interface(t *testing.T) {
	// Verify both StaticConfig and ConfigWatcher implement ConfigGetter
	var _ ConfigGetter = (*StaticConfig)(nil)
	var _ ConfigGetter = (*ConfigWatcher)(nil)
}

func TestConfigWatcher_HotReloadableSettingsTakeEffect(t *testing.T) {
	// This test verifies that hot-reloadable settings actually take effect
	// after a config file change
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Write initial config with specific hot-reloadable values
	initialConfig := `default_agent = "initial-agent"
review_context_count = 3
job_timeout_minutes = 10
`
	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	cfg, err := config.LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	broadcaster := NewBroadcaster()
	_, eventCh := broadcaster.Subscribe("")

	cw := NewConfigWatcher(configPath, cfg, broadcaster)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start config watcher: %v", err)
	}
	defer cw.Stop()

	// Verify initial values
	if cw.Config().ReviewContextCount != 3 {
		t.Errorf("Initial ReviewContextCount = %d, want 3", cw.Config().ReviewContextCount)
	}
	if cw.Config().JobTimeoutMinutes != 10 {
		t.Errorf("Initial JobTimeoutMinutes = %d, want 10", cw.Config().JobTimeoutMinutes)
	}
	initialReloadTime := cw.LastReloadedAt()

	// Update config with new hot-reloadable values
	updatedConfig := `default_agent = "updated-agent"
review_context_count = 7
job_timeout_minutes = 30
`
	if err := os.WriteFile(configPath, []byte(updatedConfig), 0644); err != nil {
		t.Fatalf("Failed to write updated config: %v", err)
	}

	// Wait for config.reloaded event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case event := <-eventCh:
			if event.Type == "config.reloaded" {
				goto reloaded
			}
		case <-timeout:
			t.Fatal("Timeout waiting for config.reloaded event")
		}
	}
reloaded:

	// Verify hot-reloadable settings took effect
	if cw.Config().ReviewContextCount != 7 {
		t.Errorf("After reload, ReviewContextCount = %d, want 7", cw.Config().ReviewContextCount)
	}
	if cw.Config().JobTimeoutMinutes != 30 {
		t.Errorf("After reload, JobTimeoutMinutes = %d, want 30", cw.Config().JobTimeoutMinutes)
	}
	if cw.Config().DefaultAgent != "updated-agent" {
		t.Errorf("After reload, DefaultAgent = %q, want %q", cw.Config().DefaultAgent, "updated-agent")
	}

	// Verify LastReloadedAt changed
	if cw.LastReloadedAt().Equal(initialReloadTime) {
		t.Error("LastReloadedAt should have changed after reload")
	}
	if cw.LastReloadedAt().IsZero() {
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
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	if err := os.WriteFile(configPath, []byte("default_agent = \"test\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, _ := config.LoadGlobalFrom(configPath)
	broadcaster := NewBroadcaster()
	cw := NewConfigWatcher(configPath, cfg, broadcaster)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start: %v", err)
	}

	// Stop multiple times should not panic
	cw.Stop()
	cw.Stop()
}

func TestConfigWatcher_StartAfterStopErrors(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	if err := os.WriteFile(configPath, []byte("default_agent = \"test\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

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
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	if err := os.WriteFile(configPath, []byte("default_agent = \"v1\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := config.LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	broadcaster := NewBroadcaster()
	_, eventCh := broadcaster.Subscribe("")

	cw := NewConfigWatcher(configPath, cfg, broadcaster)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start: %v", err)
	}
	defer cw.Stop()

	// Initial counter should be 0
	if cw.ReloadCounter() != 0 {
		t.Errorf("Initial ReloadCounter = %d, want 0", cw.ReloadCounter())
	}

	// First reload
	if err := os.WriteFile(configPath, []byte("default_agent = \"v2\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	waitForReload(t, eventCh)
	if cw.ReloadCounter() != 1 {
		t.Errorf("After first reload, ReloadCounter = %d, want 1", cw.ReloadCounter())
	}

	// Second reload
	if err := os.WriteFile(configPath, []byte("default_agent = \"v3\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	waitForReload(t, eventCh)
	if cw.ReloadCounter() != 2 {
		t.Errorf("After second reload, ReloadCounter = %d, want 2", cw.ReloadCounter())
	}
}

func TestConfigWatcher_AtomicSaveViaRename(t *testing.T) {
	// Test that config reloads work when editors do atomic saves via rename
	// (e.g., write to temp file then rename over original)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	if err := os.WriteFile(configPath, []byte("default_agent = \"original\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := config.LoadGlobalFrom(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	broadcaster := NewBroadcaster()
	_, eventCh := broadcaster.Subscribe("")

	cw := NewConfigWatcher(configPath, cfg, broadcaster)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := cw.Start(ctx); err != nil {
		t.Fatalf("Failed to start: %v", err)
	}
	defer cw.Stop()

	// Simulate atomic save: write to temp file then rename
	tmpFile := filepath.Join(tmpDir, "config.toml.tmp")
	if err := os.WriteFile(tmpFile, []byte("default_agent = \"atomic-saved\"\n"), 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}
	if err := os.Rename(tmpFile, configPath); err != nil {
		t.Fatalf("Failed to rename: %v", err)
	}

	// Wait for reload event
	waitForReload(t, eventCh)

	// Verify config was updated
	if cw.Config().DefaultAgent != "atomic-saved" {
		t.Errorf("After atomic save, DefaultAgent = %q, want %q", cw.Config().DefaultAgent, "atomic-saved")
	}
}

func waitForReload(t *testing.T, eventCh <-chan Event) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case event := <-eventCh:
			if event.Type == "config.reloaded" {
				return
			}
		case <-timeout:
			t.Fatal("Timeout waiting for config.reloaded event")
		}
	}
}

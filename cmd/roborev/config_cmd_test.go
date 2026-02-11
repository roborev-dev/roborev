package main

import (
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSetConfigKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Set a string value (creates file)
	if err := setConfigKey(path, "default_agent", "gemini"); err != nil {
		t.Fatalf("setConfigKey string: %v", err)
	}

	raw := readTOML(t, path)
	if raw["default_agent"] != "gemini" {
		t.Errorf("default_agent = %v, want gemini", raw["default_agent"])
	}

	// Set an integer value
	if err := setConfigKey(path, "max_workers", "8"); err != nil {
		t.Fatalf("setConfigKey int: %v", err)
	}

	raw = readTOML(t, path)
	if raw["max_workers"] != int64(8) {
		t.Errorf("max_workers = %v (%T), want 8", raw["max_workers"], raw["max_workers"])
	}
	// Previous value should be preserved
	if raw["default_agent"] != "gemini" {
		t.Errorf("default_agent lost after second set: %v", raw["default_agent"])
	}

	// Set a boolean value
	if err := setConfigKey(path, "sync.enabled", "true"); err != nil {
		t.Fatalf("setConfigKey bool: %v", err)
	}

	raw = readTOML(t, path)
	sync, ok := raw["sync"].(map[string]interface{})
	if !ok {
		t.Fatalf("sync is not a map: %v (%T)", raw["sync"], raw["sync"])
	}
	if sync["enabled"] != true {
		t.Errorf("sync.enabled = %v, want true", sync["enabled"])
	}
}

func TestSetConfigKeyNestedCreation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Set a nested key on a new file
	if err := setConfigKey(path, "ci.poll_interval", "10m"); err != nil {
		t.Fatalf("setConfigKey nested: %v", err)
	}

	raw := readTOML(t, path)
	ci, ok := raw["ci"].(map[string]interface{})
	if !ok {
		t.Fatalf("ci is not a map: %v (%T)", raw["ci"], raw["ci"])
	}
	if ci["poll_interval"] != "10m" {
		t.Errorf("ci.poll_interval = %v, want 10m", ci["poll_interval"])
	}
}

func TestSetConfigKeyInvalidKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	err := setConfigKey(path, "nonexistent_key", "value")
	if err == nil {
		t.Fatal("expected error for invalid key")
	}
}

func TestSetConfigKeySlice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := setConfigKey(path, "ci.repos", "org/repo1,org/repo2"); err != nil {
		t.Fatalf("setConfigKey slice: %v", err)
	}

	raw := readTOML(t, path)
	ci, ok := raw["ci"].(map[string]interface{})
	if !ok {
		t.Fatalf("ci is not a map: %v (%T)", raw["ci"], raw["ci"])
	}
	repos, ok := ci["repos"].([]interface{})
	if !ok {
		t.Fatalf("ci.repos is not a slice: %v (%T)", ci["repos"], ci["repos"])
	}
	if len(repos) != 2 {
		t.Errorf("ci.repos length = %d, want 2", len(repos))
	}
}

func TestSetConfigKeyRepoConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".roborev.toml")

	if err := setConfigKey(path, "agent", "claude-code"); err != nil {
		t.Fatalf("setConfigKey repo: %v", err)
	}

	raw := readTOML(t, path)
	if raw["agent"] != "claude-code" {
		t.Errorf("agent = %v, want claude-code", raw["agent"])
	}
}

func TestSetRawMapKey(t *testing.T) {
	m := make(map[string]interface{})

	// Simple key
	setRawMapKey(m, "foo", "bar")
	if m["foo"] != "bar" {
		t.Errorf("foo = %v, want bar", m["foo"])
	}

	// Nested key
	setRawMapKey(m, "a.b.c", 42)
	a, ok := m["a"].(map[string]interface{})
	if !ok {
		t.Fatalf("a is not a map")
	}
	b, ok := a["b"].(map[string]interface{})
	if !ok {
		t.Fatalf("a.b is not a map")
	}
	if b["c"] != 42 {
		t.Errorf("a.b.c = %v, want 42", b["c"])
	}
}

func readTOML(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw := make(map[string]interface{})
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		t.Fatalf("read TOML %s: %v", path, err)
	}
	return raw
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
	"github.com/roborev-dev/roborev/internal/config"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Get and set roborev configuration",
		Long:  "Inspect or modify roborev configuration values. Similar to git config.",
	}

	cmd.AddCommand(configGetCmd())
	cmd.AddCommand(configSetCmd())
	cmd.AddCommand(configListCmd())

	return cmd
}

func configGetCmd() *cobra.Command {
	var globalFlag, localFlag bool

	cmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			if globalFlag && localFlag {
				return fmt.Errorf("cannot use both --global and --local")
			}

			if globalFlag {
				cfg, err := config.LoadGlobal()
				if err != nil {
					return fmt.Errorf("load global config: %w", err)
				}
				val, err := config.GetConfigValue(cfg, key)
				if err != nil {
					return err
				}
				if val == "" {
					return fmt.Errorf("key %q is not set in global config", key)
				}
				fmt.Println(val)
				return nil
			}

			if localFlag {
				repoPath, err := findRepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repository")
				}
				repoCfg, err := config.LoadRepoConfig(repoPath)
				if err != nil {
					return fmt.Errorf("load repo config: %w", err)
				}
				if repoCfg == nil {
					return fmt.Errorf("no local config (.roborev.toml) found")
				}
				val, err := config.GetConfigValue(repoCfg, key)
				if err != nil {
					return err
				}
				if val == "" {
					return fmt.Errorf("key %q is not set in local config", key)
				}
				fmt.Println(val)
				return nil
			}

			// Merged: try local first, then global
			repoPath, _ := findRepoRoot()
			if repoPath != "" {
				if repoCfg, err := config.LoadRepoConfig(repoPath); err == nil && repoCfg != nil {
					if val, err := config.GetConfigValue(repoCfg, key); err == nil && val != "" {
						fmt.Println(val)
						return nil
					}
				}
			}

			cfg, err := config.LoadGlobal()
			if err != nil {
				return fmt.Errorf("load global config: %w", err)
			}
			val, err := config.GetConfigValue(cfg, key)
			if err != nil {
				return err
			}
			if val == "" {
				return fmt.Errorf("key %q is not set", key)
			}
			fmt.Println(val)
			return nil
		},
	}

	cmd.Flags().BoolVar(&globalFlag, "global", false, "get from global config only")
	cmd.Flags().BoolVar(&localFlag, "local", false, "get from local repo config only")

	return cmd
}

func configSetCmd() *cobra.Command {
	var globalFlag bool

	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			if globalFlag {
				return setConfigKey(config.GlobalConfigPath(), key, value)
			}

			// Default: set in local config
			repoPath, err := findRepoRoot()
			if err != nil {
				return fmt.Errorf("not in a git repository (use --global for global config)")
			}
			localPath := filepath.Join(repoPath, ".roborev.toml")
			return setConfigKey(localPath, key, value)
		},
	}

	cmd.Flags().BoolVar(&globalFlag, "global", false, "set in global config")

	return cmd
}

func configListCmd() *cobra.Command {
	var globalFlag, localFlag, showOrigin bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configuration values",
		RunE: func(cmd *cobra.Command, args []string) error {
			if globalFlag && localFlag {
				return fmt.Errorf("cannot use both --global and --local")
			}

			if globalFlag {
				cfg, err := config.LoadGlobal()
				if err != nil {
					return fmt.Errorf("load global config: %w", err)
				}
				kvs := config.ListConfigKeys(cfg)
				printKeyValues(kvs)
				return nil
			}

			if localFlag {
				repoPath, err := findRepoRoot()
				if err != nil {
					return fmt.Errorf("not in a git repository")
				}
				repoCfg, err := config.LoadRepoConfig(repoPath)
				if err != nil {
					return fmt.Errorf("load repo config: %w", err)
				}
				if repoCfg == nil {
					return fmt.Errorf("no local config (.roborev.toml) found")
				}
				kvs := config.ListConfigKeys(repoCfg)
				printKeyValues(kvs)
				return nil
			}

			// Merged view
			cfg, err := config.LoadGlobal()
			if err != nil {
				return fmt.Errorf("load global config: %w", err)
			}

			var repoCfg *config.RepoConfig
			if repoPath, err := findRepoRoot(); err == nil {
				repoCfg, _ = config.LoadRepoConfig(repoPath)
			}

			kvos := config.MergedConfigWithOrigin(cfg, repoCfg)
			if showOrigin {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				for _, kvo := range kvos {
					val := kvo.Value
					if config.IsSensitiveKey(kvo.Key) {
						val = config.MaskValue(val)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\n", kvo.Origin, kvo.Key, val)
				}
				return w.Flush()
			}

			for _, kvo := range kvos {
				val := kvo.Value
				if config.IsSensitiveKey(kvo.Key) {
					val = config.MaskValue(val)
				}
				fmt.Printf("%s=%s\n", kvo.Key, val)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&globalFlag, "global", false, "list global config only")
	cmd.Flags().BoolVar(&localFlag, "local", false, "list local repo config only")
	cmd.Flags().BoolVar(&showOrigin, "show-origin", false, "show where each value comes from (global/local/default)")

	return cmd
}

// printKeyValues prints key-value pairs, masking sensitive values
func printKeyValues(kvs []config.KeyValue) {
	for _, kv := range kvs {
		val := kv.Value
		if config.IsSensitiveKey(kv.Key) {
			val = config.MaskValue(val)
		}
		fmt.Printf("%s=%s\n", kv.Key, val)
	}
}

// setConfigKey sets a key in a TOML file using raw map manipulation
// to avoid writing default values for every field.
func setConfigKey(path, key, value string) error {
	// Load existing file as raw map
	raw := make(map[string]interface{})
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}

	// Validate the key exists on the appropriate struct
	// and convert the value to the correct type.
	var validationCfg interface{}
	if filepath.Base(path) == "config.toml" {
		validationCfg = &config.Config{}
	} else {
		validationCfg = &config.RepoConfig{}
	}
	if err := config.SetConfigValue(validationCfg, key, value); err != nil {
		return err
	}

	// Now get the typed value back for storage
	typedVal, _ := config.GetConfigValue(validationCfg, key)

	// Set in raw map, handling dot notation for nested keys
	if err := setRawMapKey(raw, key, coerceValue(typedVal, value)); err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// Write back
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(raw)
}

// setRawMapKey sets a value in a nested map using dot-separated keys
func setRawMapKey(m map[string]interface{}, key string, value interface{}) error {
	parts := splitDotKey(key)

	if len(parts) == 1 {
		m[parts[0]] = value
		return nil
	}

	// Navigate/create nested maps
	current := m
	for _, part := range parts[:len(parts)-1] {
		if sub, ok := current[part]; ok {
			if subMap, ok := sub.(map[string]interface{}); ok {
				current = subMap
			} else {
				// Overwrite non-map value with new map
				newMap := make(map[string]interface{})
				current[part] = newMap
				current = newMap
			}
		} else {
			newMap := make(map[string]interface{})
			current[part] = newMap
			current = newMap
		}
	}

	current[parts[len(parts)-1]] = value
	return nil
}

// splitDotKey splits a key on dots
func splitDotKey(key string) []string {
	return splitString(key, '.')
}

// coerceValue tries to convert a string to an appropriate Go type for TOML encoding
func coerceValue(typedVal, rawVal string) interface{} {
	// Try bool
	if rawVal == "true" {
		return true
	}
	if rawVal == "false" {
		return false
	}

	// Try integer
	var n int64
	if _, err := fmt.Sscanf(rawVal, "%d", &n); err == nil && fmt.Sprintf("%d", n) == rawVal {
		return n
	}

	// Check for comma-separated list
	if typedVal == rawVal && contains(rawVal, ",") {
		parts := splitComma(rawVal)
		result := make([]interface{}, len(parts))
		for i, p := range parts {
			result[i] = p
		}
		return result
	}

	return rawVal
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var result []string
	for _, part := range splitOnComma(s) {
		trimmed := trimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func splitOnComma(s string) []string {
	return splitString(s, ',')
}

func splitString(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// findRepoRoot walks up from the current directory to find a git repo root
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a git repository")
		}
		dir = parent
	}
}

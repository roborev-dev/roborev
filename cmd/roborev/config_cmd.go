package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
				if !config.IsConfigValueSet(cfg, key) {
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
				if !config.IsConfigValueSet(repoCfg, key) {
					return fmt.Errorf("key %q is not set in local config", key)
				}
				fmt.Println(val)
				return nil
			}

			// Merged: try local first, then global
			if !config.IsValidKey(key) {
				return fmt.Errorf("unknown config key: %q", key)
			}

			repoPath, _ := findRepoRoot()
			if repoPath != "" {
				if repoCfg, loadErr := config.LoadRepoConfig(repoPath); loadErr == nil && repoCfg != nil {
					if config.IsConfigValueSet(repoCfg, key) {
						val, err := config.GetConfigValue(repoCfg, key)
						if err != nil {
							return err
						}
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
			if !config.IsConfigValueSet(cfg, key) {
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
	var globalFlag, localFlag bool

	cmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			if globalFlag && localFlag {
				return fmt.Errorf("cannot use both --global and --local")
			}

			if globalFlag {
				return setConfigKey(config.GlobalConfigPath(), key, value)
			}

			// Default (and --local): set in local config
			repoPath, err := findRepoRoot()
			if err != nil {
				return fmt.Errorf("not in a git repository (use --global for global config)")
			}
			localPath := filepath.Join(repoPath, ".roborev.toml")
			return setConfigKey(localPath, key, value)
		},
	}

	cmd.Flags().BoolVar(&globalFlag, "global", false, "set in global config")
	cmd.Flags().BoolVar(&localFlag, "local", false, "set in local repo config (default)")

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

	// Validate the key exists on one of the config structs
	// and convert the value to the correct type.
	// Try both Config and RepoConfig since either key set is valid in either file.
	globalCfg := &config.Config{}
	repoCfg := &config.RepoConfig{}
	var validationCfg interface{}
	if err := config.SetConfigValue(globalCfg, key, value); err == nil {
		validationCfg = globalCfg
	} else if err := config.SetConfigValue(repoCfg, key, value); err == nil {
		validationCfg = repoCfg
	} else {
		return fmt.Errorf("unknown config key: %q", key)
	}

	// Set in raw map, handling dot notation for nested keys
	setRawMapKey(raw, key, coerceValue(validationCfg, key, value))

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// Preserve original file permissions if the file exists
	var mode os.FileMode = 0644
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode()
	}

	// Write to temp file and rename for atomicity
	f, err := os.CreateTemp(filepath.Dir(path), ".roborev-config-*.toml")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath) // clean up on any failure; no-op after successful rename

	if err := toml.NewEncoder(f).Encode(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

// setRawMapKey sets a value in a nested map using dot-separated keys.
func setRawMapKey(m map[string]interface{}, key string, value interface{}) {
	parts := strings.Split(key, ".")

	if len(parts) == 1 {
		m[parts[0]] = value
		return
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
}

// coerceValue uses the typed config struct to determine the correct TOML type
// for the given key's value.
func coerceValue(validationCfg interface{}, key, rawVal string) interface{} {
	v := reflect.ValueOf(validationCfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	field, err := config.FindFieldByTOMLKey(v, key)
	if err != nil {
		// Unreachable: key was already validated by SetConfigValue above.
		// Fall back to raw string to avoid panicking on impossible paths.
		return rawVal
	}

	switch field.Kind() {
	case reflect.String:
		return rawVal
	case reflect.Bool:
		return field.Bool()
	case reflect.Int, reflect.Int64:
		return field.Int()
	case reflect.Slice:
		if field.Type().Elem().Kind() == reflect.String {
			result := make([]interface{}, field.Len())
			for i := 0; i < field.Len(); i++ {
				result[i] = field.Index(i).String()
			}
			return result
		}
		return rawVal
	case reflect.Ptr:
		if field.IsNil() {
			return rawVal
		}
		elem := field.Elem()
		if elem.Kind() == reflect.Bool {
			return elem.Bool()
		}
		return rawVal
	default:
		return rawVal
	}
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

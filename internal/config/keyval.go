package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// KeyValue represents a config key and its value
type KeyValue struct {
	Key   string
	Value string
}

// sensitiveKeys contains keys whose values should be masked in list output.
var sensitiveKeys = map[string]bool{
	"anthropic_api_key":         true,
	"sync.postgres_url":         true,
	"ci.github_app_private_key": true,
}

// IsSensitiveKey returns true if the key holds a secret that should be masked.
func IsSensitiveKey(key string) bool {
	return sensitiveKeys[key]
}

// MaskValue returns a masked version of a sensitive value, showing only the last 4 chars.
func MaskValue(val string) string {
	if len(val) <= 4 {
		return "****"
	}
	return "****" + val[len(val)-4:]
}

// KeyValueOrigin represents a config key, its value, and where it came from
type KeyValueOrigin struct {
	Key    string
	Value  string
	Origin string // "global", "local", "default"
}

// GetConfigValue retrieves a value from a config struct by its TOML key.
// Supports dot-separated keys for nested structs (e.g., "sync.enabled").
func GetConfigValue(cfg interface{}, key string) (string, error) {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", fmt.Errorf("expected struct, got %s", v.Kind())
	}

	field, err := FindFieldByTOMLKey(v, key)
	if err != nil {
		return "", err
	}

	return formatValue(field), nil
}

// SetConfigValue sets a value on a config struct by its TOML key.
// Converts the string value to the appropriate Go type.
func SetConfigValue(cfg interface{}, key string, value string) error {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("expected pointer to struct, got %s", v.Kind())
	}

	field, err := FindFieldByTOMLKey(v, key)
	if err != nil {
		return err
	}

	if !field.CanSet() {
		return fmt.Errorf("cannot set field for key %q", key)
	}

	return setFieldValue(field, value)
}

// ListConfigKeys returns all non-zero values from a config struct as key-value pairs.
func ListConfigKeys(cfg interface{}) []KeyValue {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}

	return listFields(v, "")
}

// MergedConfigWithOrigin returns all config values with their origin.
// global is the loaded global config (already has defaults applied by LoadGlobal).
// repo is the loaded repo config (nil if no .roborev.toml).
// Values are attributed: local if set (non-zero) in repo config, global if
// different from the default, otherwise default.
func MergedConfigWithOrigin(global *Config, repo *RepoConfig) []KeyValueOrigin {
	defaults := DefaultConfig()

	// Get all keys from the global config (which includes defaults)
	globalKVs := listAllFields(reflect.ValueOf(global).Elem(), "")
	defaultMap := make(map[string]string)
	for _, kv := range listAllFields(reflect.ValueOf(defaults).Elem(), "") {
		defaultMap[kv.Key] = kv.Value
	}

	// Get non-zero repo values (these are actual overrides)
	repoMap := make(map[string]string)
	if repo != nil {
		for _, kv := range listFields(reflect.ValueOf(repo).Elem(), "") {
			repoMap[kv.Key] = kv.Value
		}
	}

	var result []KeyValueOrigin
	for _, kv := range globalKVs {
		// Check if repo overrides this key
		if repoVal, ok := repoMap[kv.Key]; ok {
			result = append(result, KeyValueOrigin{Key: kv.Key, Value: repoVal, Origin: "local"})
			continue
		}

		// Check if global value differs from default
		if defaultVal, ok := defaultMap[kv.Key]; ok && kv.Value == defaultVal {
			result = append(result, KeyValueOrigin{Key: kv.Key, Value: kv.Value, Origin: "default"})
		} else {
			result = append(result, KeyValueOrigin{Key: kv.Key, Value: kv.Value, Origin: "global"})
		}
	}

	// Add repo-only keys not in global (e.g., "agent", "review_guidelines")
	globalKeySet := make(map[string]bool)
	for _, kv := range globalKVs {
		globalKeySet[kv.Key] = true
	}
	for key, val := range repoMap {
		if !globalKeySet[key] {
			result = append(result, KeyValueOrigin{Key: key, Value: val, Origin: "local"})
		}
	}

	return result
}

// FindFieldByTOMLKey locates a struct field by its TOML tag, supporting dot notation.
func FindFieldByTOMLKey(v reflect.Value, key string) (reflect.Value, error) {
	parts := strings.SplitN(key, ".", 2)
	tagName := parts[0]

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("toml")
		if tag == "" {
			continue
		}
		// Handle tag options like `toml:"name,omitempty"`
		tagKey := strings.Split(tag, ",")[0]
		if tagKey != tagName {
			continue
		}

		fieldVal := v.Field(i)

		// If there's a remaining dot path, recurse into nested struct
		if len(parts) == 2 {
			if fieldVal.Kind() == reflect.Struct {
				return FindFieldByTOMLKey(fieldVal, parts[1])
			}
			return reflect.Value{}, fmt.Errorf("key %q: %q is not a nested struct", key, tagName)
		}

		return fieldVal, nil
	}

	return reflect.Value{}, fmt.Errorf("unknown config key: %q", key)
}

// formatValue converts a reflect.Value to its string representation
func formatValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			strs := make([]string, v.Len())
			for i := 0; i < v.Len(); i++ {
				strs[i] = v.Index(i).String()
			}
			return strings.Join(strs, ",")
		}
		return fmt.Sprintf("%v", v.Interface())
	case reflect.Ptr:
		if v.IsNil() {
			return ""
		}
		return formatValue(v.Elem())
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

// setFieldValue sets a reflect.Value from a string, handling type conversion
func setFieldValue(field reflect.Value, value string) error {
	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer value: %q", value)
		}
		field.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean value: %q", value)
		}
		field.SetBool(b)
	case reflect.Slice:
		if field.Type().Elem().Kind() == reflect.String {
			parts := strings.Split(value, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			field.Set(reflect.ValueOf(parts))
		} else {
			return fmt.Errorf("unsupported slice type for key")
		}
	case reflect.Ptr:
		// Handle *bool
		if field.Type().Elem().Kind() == reflect.Bool {
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean value: %q", value)
			}
			field.Set(reflect.ValueOf(&b))
		} else {
			return fmt.Errorf("unsupported pointer type for key")
		}
	default:
		return fmt.Errorf("unsupported field type: %s", field.Kind())
	}
	return nil
}

// listFields returns key-value pairs for all non-zero fields in a struct
func listFields(v reflect.Value, prefix string) []KeyValue {
	var result []KeyValue
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("toml")
		if tag == "" {
			continue
		}
		tagKey := strings.Split(tag, ",")[0]

		fullKey := tagKey
		if prefix != "" {
			fullKey = prefix + "." + tagKey
		}

		fieldVal := v.Field(i)

		// Recurse into nested structs (but not slices of structs or maps)
		if fieldVal.Kind() == reflect.Struct {
			result = append(result, listFields(fieldVal, fullKey)...)
			continue
		}

		// Skip zero values
		if fieldVal.IsZero() {
			continue
		}

		result = append(result, KeyValue{Key: fullKey, Value: formatValue(fieldVal)})
	}

	return result
}

// listAllFields returns key-value pairs for ALL fields (including zero) in a struct.
// Used for merged config comparison.
func listAllFields(v reflect.Value, prefix string) []KeyValue {
	var result []KeyValue
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("toml")
		if tag == "" {
			continue
		}
		tagKey := strings.Split(tag, ",")[0]

		fullKey := tagKey
		if prefix != "" {
			fullKey = prefix + "." + tagKey
		}

		fieldVal := v.Field(i)

		// Recurse into nested structs
		if fieldVal.Kind() == reflect.Struct {
			result = append(result, listAllFields(fieldVal, fullKey)...)
			continue
		}

		// Skip map and slice-of-struct types that don't have simple representations
		if fieldVal.Kind() == reflect.Map {
			continue
		}
		if fieldVal.Kind() == reflect.Slice && fieldVal.Type().Elem().Kind() == reflect.Struct {
			continue
		}

		result = append(result, KeyValue{Key: fullKey, Value: formatValue(fieldVal)})
	}

	return result
}

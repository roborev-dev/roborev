package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// KeyValue represents a config key and its value
type KeyValue struct {
	Key   string
	Value string
}

// sensitiveKeys is populated at init time by scanning Config and RepoConfig
// struct tags for `sensitive:"true"`, so new sensitive fields are automatically
// detected without maintaining a separate list.
var sensitiveKeys map[string]bool

func init() {
	sensitiveKeys = make(map[string]bool)
	collectSensitiveKeys(reflect.TypeOf(Config{}), "", sensitiveKeys)
	collectSensitiveKeys(reflect.TypeOf(RepoConfig{}), "", sensitiveKeys)
}

// getTOMLKey extracts the TOML key name from a struct field's tag.
// Returns "" if the field has no toml tag.
func getTOMLKey(field reflect.StructField) string {
	tag := field.Tag.Get("toml")
	if tag == "" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// collectSensitiveKeys walks struct fields and records TOML keys tagged sensitive:"true".
func collectSensitiveKeys(t reflect.Type, prefix string, out map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tagKey := getTOMLKey(field)
		if tagKey == "" {
			continue
		}
		fullKey := tagKey
		if prefix != "" {
			fullKey = prefix + "." + tagKey
		}
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			collectSensitiveKeys(ft, fullKey, out)
			continue
		}
		if field.Tag.Get("sensitive") == "true" {
			out[fullKey] = true
		}
	}
}

// IsValidKey returns true if the key is recognized by either Config or RepoConfig.
func IsValidKey(key string) bool {
	_, err1 := FindFieldByTOMLKey(reflect.ValueOf(Config{}), key)
	_, err2 := FindFieldByTOMLKey(reflect.ValueOf(RepoConfig{}), key)
	return err1 == nil || err2 == nil
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

// IsConfigValueSet returns true if the field for key exists and is non-zero.
func IsConfigValueSet(cfg interface{}, key string) bool {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}
	field, err := FindFieldByTOMLKey(v, key)
	if err != nil {
		return false
	}
	return !field.IsZero()
}

// LoadRawGlobal loads the global config file as a raw TOML map.
func LoadRawGlobal() (map[string]interface{}, error) {
	return LoadRawTOML(GlobalConfigPath())
}

// LoadRawRepo loads the repo config file as a raw TOML map.
func LoadRawRepo(repoPath string) (map[string]interface{}, error) {
	return LoadRawTOML(filepath.Join(repoPath, ".roborev.toml"))
}

// LoadRawTOML loads a TOML file as a raw map.
func LoadRawTOML(path string) (map[string]interface{}, error) {
	raw := make(map[string]interface{})
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// IsKeyInTOMLFile checks whether a dot-separated key was explicitly present
// in a raw TOML map (as returned by toml.Decode into map[string]interface{}).
// This correctly detects explicit false/0 values that IsZero would miss.
func IsKeyInTOMLFile(raw map[string]interface{}, key string) bool {
	parts := strings.SplitN(key, ".", 2)
	val, ok := raw[parts[0]]
	if !ok {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	sub, ok := val.(map[string]interface{})
	if !ok {
		return false
	}
	return IsKeyInTOMLFile(sub, parts[1])
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

	field, err := FindOrCreateFieldByTOMLKey(v, key)
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

// kvMap builds a map from key to formatted value for all fields in a struct.
func kvMap(cfg interface{}) map[string]string {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	m := make(map[string]string)
	for _, kv := range listAllFields(v, "") {
		m[kv.Key] = kv.Value
	}
	return m
}

// determineOrigin decides the origin label for a global config key.
// It returns ("", false) if the key should be omitted from output.
func determineOrigin(key, value, defaultValue string, rawGlobal map[string]interface{}) (string, bool) {
	isDefault := defaultValue == value
	isEmptyDefault := value == "" || value == "0" || value == "false"
	explicitInGlobal := IsKeyInTOMLFile(rawGlobal, key)

	if isEmptyDefault && !explicitInGlobal {
		return "", false
	}
	if isDefault {
		return "default", true
	}
	return "global", true
}

// MergedConfigWithOrigin returns all effective config values with their origin.
// global is the loaded global config (already has defaults applied by LoadGlobal).
// repo is the loaded repo config (nil if no .roborev.toml).
// rawGlobal and rawRepo are raw TOML maps for detecting explicit presence of
// false/0 values. Pass nil if not available.
func MergedConfigWithOrigin(global *Config, repo *RepoConfig, rawGlobal, rawRepo map[string]interface{}) []KeyValueOrigin {
	if rawGlobal == nil {
		rawGlobal = make(map[string]interface{})
	}
	if rawRepo == nil {
		rawRepo = make(map[string]interface{})
	}

	defaultMap := kvMap(DefaultConfig())
	repoValMap := make(map[string]string)
	if repo != nil {
		repoValMap = kvMap(repo)
	}

	globalKVs := listAllFields(reflect.ValueOf(global).Elem(), "")

	var result []KeyValueOrigin
	for _, kv := range globalKVs {
		// Check if repo explicitly sets this key (via raw TOML presence)
		if IsKeyInTOMLFile(rawRepo, kv.Key) {
			result = append(result, KeyValueOrigin{Key: kv.Key, Value: repoValMap[kv.Key], Origin: "local"})
			continue
		}

		if origin, ok := determineOrigin(kv.Key, kv.Value, defaultMap[kv.Key], rawGlobal); ok {
			result = append(result, KeyValueOrigin{Key: kv.Key, Value: kv.Value, Origin: origin})
		}
	}

	// Add repo-only keys not in global (e.g., "agent", "review_guidelines")
	globalKeySet := make(map[string]bool)
	for _, kv := range globalKVs {
		globalKeySet[kv.Key] = true
	}
	var repoOnlyKeys []string
	for key := range repoValMap {
		if !globalKeySet[key] && IsKeyInTOMLFile(rawRepo, key) {
			repoOnlyKeys = append(repoOnlyKeys, key)
		}
	}
	sort.Strings(repoOnlyKeys)
	for _, key := range repoOnlyKeys {
		result = append(result, KeyValueOrigin{Key: key, Value: repoValMap[key], Origin: "local"})
	}

	return result
}

// FindFieldByTOMLKey locates a struct field by its TOML tag, supporting dot notation.
// It never mutates the struct; nil pointer fields are resolved via a throwaway zero struct.
func FindFieldByTOMLKey(v reflect.Value, key string) (reflect.Value, error) {
	return findFieldByTOMLKey(v, key, false)
}

// FindOrCreateFieldByTOMLKey is like FindFieldByTOMLKey but initializes nil pointer
// fields so the returned field is settable. Use this in SetConfigValue.
func FindOrCreateFieldByTOMLKey(v reflect.Value, key string) (reflect.Value, error) {
	return findFieldByTOMLKey(v, key, true)
}

func findFieldByTOMLKey(v reflect.Value, key string, initPointers bool) (reflect.Value, error) {
	parts := strings.SplitN(key, ".", 2)
	tagName := parts[0]

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tagKey := getTOMLKey(field)
		if tagKey == "" || tagKey != tagName {
			continue
		}

		fieldVal := v.Field(i)

		// If there's a remaining dot path, recurse into nested struct
		if len(parts) == 2 {
			if fieldVal.Kind() == reflect.Ptr {
				if fieldVal.IsNil() {
					if initPointers && fieldVal.CanSet() {
						// Initialize the nil pointer so the returned field is settable.
						fieldVal.Set(reflect.New(fieldVal.Type().Elem()))
					} else {
						// Read-only path: use a throwaway zero struct.
						zeroStruct := reflect.New(fieldVal.Type().Elem()).Elem()
						return findFieldByTOMLKey(zeroStruct, parts[1], initPointers)
					}
				}
				fieldVal = fieldVal.Elem()
			}
			if fieldVal.Kind() == reflect.Struct {
				return findFieldByTOMLKey(fieldVal, parts[1], initPointers)
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

// listFields returns key-value pairs for all non-zero fields in a struct.
func listFields(v reflect.Value, prefix string) []KeyValue {
	return flattenStruct(v, prefix, false)
}

// listAllFields returns key-value pairs for ALL fields (including zero) in a struct.
// Used for merged config comparison.
func listAllFields(v reflect.Value, prefix string) []KeyValue {
	return flattenStruct(v, prefix, true)
}

// flattenStruct walks a struct's fields recursively, building dot-separated keys
// from TOML tags. When includeZero is false, zero-valued leaf fields are skipped.
func flattenStruct(v reflect.Value, prefix string, includeZero bool) []KeyValue {
	var result []KeyValue
	t := v.Type()

	for i := 0; i < t.NumField(); i++ {
		tagKey := getTOMLKey(t.Field(i))
		if tagKey == "" {
			continue
		}

		fullKey := tagKey
		if prefix != "" {
			fullKey = prefix + "." + tagKey
		}

		fieldVal := v.Field(i)

		// Recurse into nested structs
		if fieldVal.Kind() == reflect.Ptr && !fieldVal.IsNil() && fieldVal.Elem().Kind() == reflect.Struct {
			result = append(result, flattenStruct(fieldVal.Elem(), fullKey, includeZero)...)
			continue
		}
		if fieldVal.Kind() == reflect.Struct {
			result = append(result, flattenStruct(fieldVal, fullKey, includeZero)...)
			continue
		}

		// Skip map and slice-of-struct types that don't have simple representations
		if fieldVal.Kind() == reflect.Map {
			continue
		}
		if fieldVal.Kind() == reflect.Slice && fieldVal.Type().Elem().Kind() == reflect.Struct {
			continue
		}

		if !includeZero && fieldVal.IsZero() {
			continue
		}

		result = append(result, KeyValue{Key: fullKey, Value: formatValue(fieldVal)})
	}

	return result
}

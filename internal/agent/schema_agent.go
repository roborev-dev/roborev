package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/roborev-dev/roborev/internal/config"
)

// SchemaAgent is an optional Agent capability. Implementers return a single
// JSON document conforming to the given JSON Schema, via the underlying CLI's
// native structured-output mechanism (not via prompt nagging).
type SchemaAgent interface {
	Agent

	// ClassifyWithSchema runs one agent turn constrained by `schema` and
	// returns the raw JSON result. `out` receives progress/log lines but not
	// the structured result itself.
	ClassifyWithSchema(
		ctx context.Context,
		repoPath, gitRef, prompt string,
		schema json.RawMessage,
		out io.Writer,
	) (json.RawMessage, error)
}

// IsSchemaAgent reports whether a is a SchemaAgent.
func IsSchemaAgent(a Agent) bool {
	_, ok := a.(SchemaAgent)
	return ok
}

// ValidateClassifyAgent errors when the named agent isn't registered or isn't
// a SchemaAgent. Registered with config at init() time.
func ValidateClassifyAgent(name string) error {
	registryMu.RLock()
	a, ok := registry[name]
	if !ok {
		registryMu.RUnlock()
		return fmt.Errorf("unknown agent %q", name)
	}
	if IsSchemaAgent(a) {
		registryMu.RUnlock()
		return nil
	}
	var valid []string
	for n, r := range registry {
		if IsSchemaAgent(r) {
			valid = append(valid, n)
		}
	}
	registryMu.RUnlock()
	return fmt.Errorf(
		"agent %q does not support structured output (classify_agent must be one of: %v)",
		name, valid)
}

func init() {
	config.RegisterClassifyAgentValidator(ValidateClassifyAgent)
}

package storage

import (
	"log"

	"github.com/roborev-dev/roborev/internal/config"
)

// normalizeMinSeverityForWrite returns the canonical lowercase value or empty
// string. Invalid input is dropped (returned as empty), not rejected — sync
// ingest from a stale or corrupted remote machine should not fail the entire
// sync over a single bad column. Local user-facing entry points (CLI, daemon)
// validate fail-fast; this storage helper is the last-line guarantee for the
// stored invariant and is intentionally lossy on bad input.
func normalizeMinSeverityForWrite(value string) string {
	normalized, err := config.NormalizeMinSeverity(value)
	if err != nil {
		log.Printf("storage: dropping invalid min_severity %q for write", value)
		return ""
	}
	return normalized
}

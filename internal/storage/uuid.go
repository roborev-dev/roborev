package storage

import (
	"crypto/rand"
	"fmt"
)

// GenerateUUID generates a random UUID v4 string.
// Uses crypto/rand for secure random generation.
func GenerateUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// This should never happen with crypto/rand
		panic(fmt.Sprintf("failed to generate UUID: %v", err))
	}

	// Set version (4) and variant (RFC 4122)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant RFC 4122

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// sqliteUUIDExpr is the SQLite expression to generate a UUID v4.
// Used in migrations for backfilling existing rows.
const sqliteUUIDExpr = `lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' ||
  substr(hex(randomblob(2)),2) || '-' || substr('89ab',abs(random()) % 4 + 1, 1) ||
  substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6)))`

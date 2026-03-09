package storage

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const (
	formatIterations = 100
	uniqueIterations = 10000
)

func assertUUIDFormat(t *testing.T, uuid string) {
	t.Helper()
	assert.Regexp(t, uuidV4Pattern, uuid, "UUID format mismatch")
}

func checkUniqueness(t *testing.T, generator func() string, iterations int) {
	t.Helper()
	seen := make(map[string]struct{}, iterations)
	for i := range iterations {
		uuid := generator()
		require.Condition(t, func() bool {
			_, exists := seen[uuid]
			return !exists
		}, "UUID collision detected: iteration %d: %s", i, uuid)

		seen[uuid] = struct{}{}
	}
}

func TestGenerateUUID_Format(t *testing.T) {
	for range formatIterations {
		uuid := GenerateUUID()
		assertUUIDFormat(t, uuid)
	}
}

func TestGenerateUUID_Uniqueness(t *testing.T) {
	checkUniqueness(t, GenerateUUID, uniqueIterations)
}

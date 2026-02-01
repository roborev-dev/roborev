package storage

import (
	"regexp"
	"testing"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func assertUUIDFormat(t *testing.T, uuid string) {
	t.Helper()
	if !uuidV4Pattern.MatchString(uuid) {
		t.Errorf("UUID %q does not match expected v4 format", uuid)
	}
}

func checkUniqueness(t *testing.T, generator func() string, iterations int) {
	t.Helper()
	seen := make(map[string]bool, iterations)
	for i := 0; i < iterations; i++ {
		s := generator()
		if seen[s] {
			t.Fatalf("Collision detected at iteration %d: %s", i, s)
		}
		seen[s] = true
	}
}

func TestGenerateUUID(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		uuid := GenerateUUID()
		assertUUIDFormat(t, uuid)
		if seen[uuid] {
			t.Fatalf("Collision detected at iteration %d: %s", i, uuid)
		}
		seen[uuid] = true
	}
}

func TestGenerateUUID_Uniqueness(t *testing.T) {
	checkUniqueness(t, GenerateUUID, 10000)
}

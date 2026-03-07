package storage

import (
	"regexp"
	"testing"
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const (
	formatIterations = 100
	uniqueIterations = 10000
)

func TestGenerateUUID_Format(t *testing.T) {
	for range formatIterations {
		uuid := GenerateUUID()
		if !uuidV4Pattern.MatchString(uuid) {
			t.Fatalf("UUID %q does not match expected v4 format", uuid)
		}
	}
}

func TestGenerateUUID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, uniqueIterations)
	for i := range uniqueIterations {
		uuid := GenerateUUID()
		if _, exists := seen[uuid]; exists {
			t.Fatalf("Collision detected at iteration %d: %s", i, uuid)
		}
		seen[uuid] = struct{}{}
	}
}

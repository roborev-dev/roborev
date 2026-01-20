package storage

import (
	"regexp"
	"testing"
)

func TestGenerateUUID(t *testing.T) {
	// UUID v4 format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	// where x is hex and y is 8, 9, a, or b
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	// Generate multiple UUIDs and check format
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		uuid := GenerateUUID()

		if !pattern.MatchString(uuid) {
			t.Errorf("UUID %q does not match expected format", uuid)
		}

		if seen[uuid] {
			t.Errorf("Duplicate UUID generated: %s", uuid)
		}
		seen[uuid] = true
	}
}

func TestGenerateUUID_Uniqueness(t *testing.T) {
	// Generate a larger set to check uniqueness
	seen := make(map[string]bool)
	for i := 0; i < 10000; i++ {
		uuid := GenerateUUID()
		if seen[uuid] {
			t.Fatalf("Collision detected at iteration %d: %s", i, uuid)
		}
		seen[uuid] = true
	}
}

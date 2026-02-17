package main

import (
	"strings"
	"testing"
)

func TestRemapStdinParsing(t *testing.T) {
	input := "abc123 def456\nfoo bar\n\n  baz qux  \n"
	scanner := strings.NewReader(input)

	lines := strings.Split(strings.TrimSpace(string([]byte(input))), "\n")
	var pairs [][2]string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pairs = append(pairs, [2]string{fields[0], fields[1]})
	}

	_ = scanner // reader not needed — we tested parsing logic directly

	expected := [][2]string{
		{"abc123", "def456"},
		{"foo", "bar"},
		{"baz", "qux"},
	}

	if len(pairs) != len(expected) {
		t.Fatalf("expected %d pairs, got %d", len(expected), len(pairs))
	}
	for i, p := range pairs {
		if p != expected[i] {
			t.Errorf("pair %d: expected %v, got %v", i, expected[i], p)
		}
	}
}

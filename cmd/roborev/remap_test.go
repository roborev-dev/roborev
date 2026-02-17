package main

import (
	"strings"
	"testing"
)

func TestRemapStdinParsing(t *testing.T) {
	input := "abc123 def456\nfoo bar\n\n  baz qux  \n"

	lines := strings.Split(strings.TrimSpace(input), "\n")
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

func TestGitSHAValidation(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"abc123def456abc123def456abc123def456abc1", true},
		{"0000000000000000000000000000000000000000", true},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"abc123", false}, // too short
		{"ABC123DEF456ABC123DEF456ABC123DEF456ABC1", false}, // uppercase
		{"--option", false}, // flag injection
		{"-n1", false},      // short flag
		{"abc123def456abc123def456abc123def456abc1x", false}, // 41 chars
		{"abc123def456abc123def456abc123def456abc", false},   // 39 chars
		{"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", false},  // non-hex
		{"abc123def456abc123def456abc123def456abc1 ", false}, // trailing space
	}
	for _, tt := range tests {
		got := gitSHAPattern.MatchString(tt.input)
		if got != tt.valid {
			t.Errorf("gitSHAPattern.MatchString(%q) = %v, want %v",
				tt.input, got, tt.valid)
		}
	}
}

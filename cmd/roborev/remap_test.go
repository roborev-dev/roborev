package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRemapStdinParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected [][2]string
	}{
		{
			name:  "HappyPath",
			input: "abc123 def456\nfoo bar\n\n  baz qux  \n",
			expected: [][2]string{
				{"abc123", "def456"},
				{"foo", "bar"},
				{"baz", "qux"},
			},
		},
		{
			name:  "MalformedLines",
			input: "abc123 def456\nonlyone\n\n  baz qux  \n",
			expected: [][2]string{
				{"abc123", "def456"},
				{"baz", "qux"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRemapPairs(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("parseRemapPairs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("parseRemapPairs() = %v, want %v", got, tt.expected)
			}
		})
	}
}

type errReader struct{ err error }

func (e *errReader) Read(p []byte) (n int, err error) {
	return 0, e.err
}

func TestRemapStdinParsing_ScannerError(t *testing.T) {
	expectedErr := errors.New("read error")
	_, err := parseRemapPairs(&errReader{err: expectedErr})
	if err == nil {
		t.Fatal("parseRemapPairs() expected error, got nil")
	}
	if err != expectedErr {
		t.Errorf("parseRemapPairs() error = %v, want %v", err, expectedErr)
	}
}

func TestGitSHAValidation(t *testing.T) {
	sha256Valid := "abc123def456abc123def456abc123def456abc1" +
		"aabbccddeeff00112233aabb"
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"ValidSHA1", "abc123def456abc123def456abc123def456abc1", true},
		{"ZeroSHA1", "0000000000000000000000000000000000000000", true},
		{"AllAlphaSHA1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},
		{"ValidSHA256", sha256Valid, true},
		{"TooShort", "abc123", false},
		{"UppercaseInvalid", "ABC123DEF456ABC123DEF456ABC123DEF456ABC1", false},
		{"FlagInjection", "--option", false},
		{"ShortFlag", "-n1", false},
		{"TooLong41Chars", "abc123def456abc123def456abc123def456abc1x", false},
		{"TooShort39Chars", "abc123def456abc123def456abc123def456abc", false},
		{"NonHex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", false},
		{"TrailingSpace", "abc123def456abc123def456abc123def456abc1 ", false},
		{"TooLongSHA256Plus2", sha256Valid + "aa", false},
		{"TooShortSHA256Minus1", sha256Valid[:63], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gitSHAPattern.MatchString(tt.input)
			if got != tt.valid {
				t.Errorf("gitSHAPattern.MatchString(%q) = %v, want %v",
					tt.input, got, tt.valid)
			}
		})
	}
}

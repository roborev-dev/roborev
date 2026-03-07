package review

import (
	"strings"
	"testing"
)

func TestTrimPartialRune(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ascii only", "hello", "hello"},
		{
			"clean emoji boundary",
			"abc😀",
			"abc😀",
		},
		{
			"split 4-byte emoji after 1 byte",
			"abc\xf0",
			"abc",
		},
		{
			"split 4-byte emoji after 2 bytes",
			"abc\xf0\x9f",
			"abc",
		},
		{
			"split 4-byte emoji after 3 bytes",
			"abc\xf0\x9f\x98",
			"abc",
		},
		{
			"split 2-byte char after 1 byte",
			"abc\xc3",
			"abc",
		},
		{
			"interior invalid bytes preserved",
			"a\xffb",
			"a\xffb",
		},
		{
			"orphan continuation byte",
			"abc\x80",
			"abc",
		},
		{
			"two orphan continuation bytes",
			"abc\x80\x80",
			"abc",
		},
		{
			"no full string scan",
			strings.Repeat("x", 1000) + "\xff" + strings.Repeat("y", 1000),
			strings.Repeat("x", 1000) + "\xff" + strings.Repeat("y", 1000),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimPartialRune(tt.in)
			if got != tt.want {
				t.Errorf("TrimPartialRune(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

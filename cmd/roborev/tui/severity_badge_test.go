package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDerefOrZero(t *testing.T) {
	assert.Equal(t, 0, derefOrZero(nil), "nil pointer should yield 0")
	v := 42
	assert.Equal(t, 42, derefOrZero(&v), "non-nil pointer should yield underlying value")
	zero := 0
	assert.Equal(t, 0, derefOrZero(&zero), "explicit 0 pointer should yield 0")
}

func TestRenderSeverityBadge(t *testing.T) {
	tests := []struct {
		name      string
		h, m, l   int
		wantPlain string
	}{
		{
			name:      "all zero",
			wantPlain: "0/0/0",
		},
		{
			name:      "single digits",
			h:         3, m: 2, l: 5,
			wantPlain: "3/2/5",
		},
		{
			name:      "double digits",
			h:         12, m: 3, l: 8,
			wantPlain: "12/3/8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderSeverityBadge(tt.h, tt.m, tt.l)
			plain := stripANSI(out)
			assert.Equal(t, tt.wantPlain, plain)
		})
	}
}

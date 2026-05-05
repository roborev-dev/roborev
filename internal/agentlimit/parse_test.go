package agentlimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseResetDuration(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want time.Duration
	}{
		{"none", "agent failed", 0},
		{"reset after seconds clamped to min", "quota will reset after 5s.", 1 * time.Minute},
		{"reset after minutes", "quota will reset after 48m20s.", 48*time.Minute + 20*time.Second},
		{"reset after hours", "reset after 2h13m.", 2*time.Hour + 13*time.Minute},
		{"reset after with trailing punct", "reset after 1h.. retrying", 1 * time.Hour},
		{"reset after invalid", "reset after notaduration", 0},
		{"reset after negative", "reset after -5m", 0},
		{"reset after huge clamped to max", "reset after 100h", 24 * time.Hour},
		{"case insensitive", "RESET AFTER 30m", 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ParseResetDuration(tc.msg))
		})
	}
}

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

func TestParseResetTime(t *testing.T) {
	// Use a fixed "now" so same-day vs next-day rollover is deterministic.
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, loc) // noon UTC
	cases := []struct {
		name    string
		msg     string
		wantErr bool
		want    time.Time
	}{
		{"none", "agent failed", true, time.Time{}},
		{
			name: "resets at later today",
			msg:  "limit resets at 5:42 PM",
			want: time.Date(2026, 5, 5, 17, 42, 0, 0, loc),
		},
		{
			name: "try again at later today 24h",
			msg:  "try again at 17:42",
			want: time.Date(2026, 5, 5, 17, 42, 0, 0, loc),
		},
		{
			name: "resets at earlier today rolls to next day",
			msg:  "limit resets at 9:00 AM",
			want: time.Date(2026, 5, 6, 9, 0, 0, 0, loc),
		},
		{
			name: "case insensitive",
			msg:  "LIMIT RESETS AT 6:00 pm",
			want: time.Date(2026, 5, 5, 18, 0, 0, 0, loc),
		},
		{"unparseable token", "resets at moonrise", true, time.Time{}},
		{
			// Trailing content after the time defeats time.Parse and
			// produces zero. Documenting current conservative behavior
			// — extending the parser to handle "today" / "UTC" suffixes
			// is deferred until a real Claude message is captured.
			name:    "trailing word causes miss",
			msg:     "limit resets at 5:42 PM today",
			wantErr: true,
			want:    time.Time{},
		},
		{
			// 24h with a timezone abbreviation: same conservative miss.
			name:    "24h with trailing zone causes miss",
			msg:     "resets at 17:42 UTC",
			wantErr: true,
			want:    time.Time{},
		},
		{
			// Same instant as now: should roll forward, not return now.
			name: "exactly now rolls to next day",
			msg:  "limit resets at 12:00 PM",
			want: time.Date(2026, 5, 6, 12, 0, 0, 0, loc),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResetTimeAt(tc.msg, now)
			if tc.wantErr {
				assert.True(t, got.IsZero(), "expected zero time, got %v", got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseResetTimeRespectsLocation(t *testing.T) {
	// Non-UTC fixture: confirms now.Location() is honored end-to-end.
	est := time.FixedZone("EST", -5*60*60)
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, est) // noon EST

	got := parseResetTimeAt("limit resets at 5:42 PM", now)
	want := time.Date(2026, 5, 5, 17, 42, 0, 0, est)
	assert.Equal(t, want, got)
	assert.Equal(t, est, got.Location())
}

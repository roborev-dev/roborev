package autotype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountChangedLines(t *testing.T) {
	cases := []struct {
		name string
		diff string
		want int
	}{
		{"empty", "", 0},
		{"no changed lines", "--- a/f\n+++ b/f\n@@ -0,0 +0,0 @@\n", 0},
		{"one add", "+foo\n", 1},
		{"one delete", "-foo\n", 1},
		{"file markers ignored", "+++ b/new\n--- a/old\n+real_add\n-real_del\n", 2},
		{"comment in diff", "@@ -1,2 +1,2 @@\n-old\n+new\n context\n", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, CountChangedLines(c.diff))
		})
	}
}

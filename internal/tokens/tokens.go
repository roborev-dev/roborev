package tokens

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Usage holds token consumption data for a single review job.
// Stored as JSON in the review_jobs.token_usage column.
// Fields align with agentsview's token-use output.
type Usage struct {
	OutputTokens      int64 `json:"total_output_tokens,omitempty"`
	PeakContextTokens int64 `json:"peak_context_tokens,omitempty"`
}

// agentsviewResponse is the JSON shape returned by
// `agentsview token-use <session-id>`.
type agentsviewResponse struct {
	SessionID         string `json:"session_id"`
	Agent             string `json:"agent"`
	Project           string `json:"project"`
	OutputTokens      int64  `json:"total_output_tokens"`
	PeakContextTokens int64  `json:"peak_context_tokens"`
}

// FormatSummary returns a compact human-readable summary like
// "118.0k ctx · 28.8k out". Returns empty string if no data.
func (u Usage) FormatSummary() string {
	if u.PeakContextTokens == 0 && u.OutputTokens == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%s ctx · %s out",
		formatCount(u.PeakContextTokens),
		formatCount(u.OutputTokens),
	)
}

func formatCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// minVersion is the minimum agentsview version that supports
// the token-use subcommand (0.15.0).
var minVersion = [3]int{0, 15, 0}

// versionRe extracts major.minor.patch from "agentsview vX.Y.Z...".
var versionRe = regexp.MustCompile(
	`agentsview v(\d+)\.(\d+)\.(\d+)`,
)

// checkVersion returns true if agentsview at binPath reports a
// version >= minVersion. Returns false on any error.
func checkVersion(ctx context.Context, binPath string) bool {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(
		cmdCtx, binPath, "version",
	).Output()
	if err != nil {
		return false
	}

	m := versionRe.FindSubmatch(out)
	if m == nil {
		return false
	}
	var ver [3]int
	for i := range 3 {
		ver[i], _ = strconv.Atoi(string(m[i+1]))
	}
	for i := range 3 {
		if ver[i] > minVersion[i] {
			return true
		}
		if ver[i] < minVersion[i] {
			return false
		}
	}
	return true // equal
}

var (
	versionOnce   sync.Once
	versionOK     bool
	cachedBinPath string
)

// ResetVersionCache clears the cached version check result.
// Exposed for testing only.
func ResetVersionCache() {
	versionOnce = sync.Once{}
	versionOK = false
	cachedBinPath = ""
}

// resolveAgentsview checks once whether agentsview is installed and
// new enough. Returns the binary path and true, or ("", false).
func resolveAgentsview(ctx context.Context) (string, bool) {
	versionOnce.Do(func() {
		bin, err := exec.LookPath("agentsview")
		if err != nil {
			return
		}
		if checkVersion(ctx, bin) {
			cachedBinPath = bin
			versionOK = true
		}
	})
	return cachedBinPath, versionOK
}

// FetchForSession calls `agentsview token-use <sessionID>` to get
// token usage. Returns nil (no error) if agentsview is not installed,
// is too old (< 0.15.0), or the session data is unavailable.
func FetchForSession(
	ctx context.Context, sessionID string,
) (*Usage, error) {
	if sessionID == "" {
		return nil, nil
	}

	binPath, ok := resolveAgentsview(ctx)
	if !ok {
		return nil, nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		cmdCtx, binPath, "token-use", sessionID,
	)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := string(exitErr.Stderr)
			// Session not found: exit 1, no stdout, no stderr
			if exitErr.ExitCode() == 1 &&
				len(out) == 0 && len(stderr) == 0 {
				return nil, nil
			}
			return nil, fmt.Errorf(
				"agentsview token-use: exit %d: %s",
				exitErr.ExitCode(), stderr,
			)
		}
		return nil, fmt.Errorf("agentsview token-use: %w", err)
	}

	var resp agentsviewResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("parse agentsview output: %w", err)
	}

	if resp.OutputTokens == 0 && resp.PeakContextTokens == 0 {
		return nil, nil
	}
	return &Usage{
		OutputTokens:      resp.OutputTokens,
		PeakContextTokens: resp.PeakContextTokens,
	}, nil
}

// ParseJSON deserializes a token_usage JSON blob from the database.
// Returns nil for empty/null values.
func ParseJSON(data string) *Usage {
	if data == "" {
		return nil
	}
	var u Usage
	if err := json.Unmarshal([]byte(data), &u); err != nil {
		return nil
	}
	if u.OutputTokens == 0 && u.PeakContextTokens == 0 {
		return nil
	}
	return &u
}

// ToJSON serializes token usage to JSON for database storage.
func ToJSON(u *Usage) string {
	if u == nil {
		return ""
	}
	data, err := json.Marshal(u)
	if err != nil {
		return ""
	}
	return string(data)
}

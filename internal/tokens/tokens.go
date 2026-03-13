package tokens

import (
	"context"
	"encoding/json"
	"fmt"
)

// Usage holds token consumption data for a single review job.
// Stored as JSON in the review_jobs.token_usage column.
type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
	// Agent-specific fields (populated when available)
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	ThinkingTokens      int64 `json:"thinking_tokens,omitempty"`
}

// FormatSummary returns a compact human-readable summary like
// "45k in · 3.9k out". Returns empty string if no data.
func (u Usage) FormatSummary() string {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return ""
	}
	return fmt.Sprintf(
		"%s in · %s out", formatCount(u.InputTokens),
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

// FetchForSession calls agentsview to get token usage for a
// session. Returns nil (no error) if agentsview is not installed
// or the session data is unavailable.
func FetchForSession(
	ctx context.Context, agent, sessionID string,
) (*Usage, error) {
	return fetchForSession(ctx, agent, sessionID, false)
}

// FetchForSessionTurn calls agentsview to get token usage for
// only the last turn of a resumed session. Returns nil (no error)
// if agentsview is not installed or the session data is unavailable.
func FetchForSessionTurn(
	ctx context.Context, agent, sessionID string,
) (*Usage, error) {
	return fetchForSession(ctx, agent, sessionID, true)
}

func fetchForSession(
	_ context.Context, _, sessionID string,
	_ bool,
) (*Usage, error) {
	if sessionID == "" {
		return nil, nil
	}

	// TODO: shell out to agentsview CLI once it supports:
	//   agentsview tokens --session <id> --agent <name> --json
	//   agentsview tokens --session <id> --agent <name> --json --turn last
	// For now, return nil so callers gracefully skip token collection.
	return nil, nil
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
	if u.InputTokens == 0 && u.OutputTokens == 0 {
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

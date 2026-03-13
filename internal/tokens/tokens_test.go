package tokens

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatSummary(t *testing.T) {
	tests := []struct {
		name  string
		usage Usage
		want  string
	}{
		{"zero", Usage{}, ""},
		{
			"small counts",
			Usage{InputTokens: 500, OutputTokens: 120},
			"500 in · 120 out",
		},
		{
			"thousands",
			Usage{InputTokens: 45200, OutputTokens: 3900},
			"45.2k in · 3.9k out",
		},
		{
			"millions",
			Usage{InputTokens: 2_500_000, OutputTokens: 15_000},
			"2.5M in · 15.0k out",
		},
		{
			"output only",
			Usage{OutputTokens: 800},
			"0 in · 800 out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.usage.FormatSummary()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseJSON(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		assert.Nil(t, ParseJSON(""))
	})

	t.Run("valid json", func(t *testing.T) {
		u := ParseJSON(`{"input_tokens":1000,"output_tokens":200}`)
		require.NotNil(t, u)
		assert.Equal(t, int64(1000), u.InputTokens)
		assert.Equal(t, int64(200), u.OutputTokens)
	})

	t.Run("all zeros", func(t *testing.T) {
		assert.Nil(t, ParseJSON(`{"input_tokens":0,"output_tokens":0}`))
	})

	t.Run("invalid json", func(t *testing.T) {
		assert.Nil(t, ParseJSON(`{invalid`))
	})

	t.Run("with cache fields", func(t *testing.T) {
		u := ParseJSON(`{
			"input_tokens": 5000,
			"output_tokens": 300,
			"cache_read_tokens": 4000,
			"cache_creation_tokens": 1000
		}`)
		require.NotNil(t, u)
		assert.Equal(t, int64(4000), u.CacheReadTokens)
		assert.Equal(t, int64(1000), u.CacheCreationTokens)
	})
}

func TestToJSON(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.Empty(t, ToJSON(nil))
	})

	t.Run("round trip", func(t *testing.T) {
		orig := &Usage{
			InputTokens:  5000,
			OutputTokens: 300,
		}
		s := ToJSON(orig)
		got := ParseJSON(s)
		require.NotNil(t, got)
		assert.Equal(t, orig.InputTokens, got.InputTokens)
		assert.Equal(t, orig.OutputTokens, got.OutputTokens)
	})
}

func TestFetchForSession_Stub(t *testing.T) {
	// Stub always returns nil until agentsview CLI support is added
	u, err := FetchForSession(context.Background(), "claude-code", "abc123")
	require.NoError(t, err)
	assert.Nil(t, u)
}

func TestFetchForSession_EmptySessionID(t *testing.T) {
	u, err := FetchForSession(context.Background(), "claude-code", "")
	require.NoError(t, err)
	assert.Nil(t, u)
}

func TestFetchForSessionTurn_Stub(t *testing.T) {
	u, err := FetchForSessionTurn(context.Background(), "claude-code", "abc123")
	require.NoError(t, err)
	assert.Nil(t, u)
}

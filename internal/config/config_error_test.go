package config

import (
	"errors"
	"fmt"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigParseError(t *testing.T) {
	t.Run("Error method", func(t *testing.T) {
		innerErr := errors.New("invalid toml")
		err := &ConfigParseError{
			Ref: "origin/main",
			Err: innerErr,
		}
		assert.Equal(t, "parse .roborev.toml at origin/main: invalid toml", err.Error())
	})

	t.Run("Unwrap method", func(t *testing.T) {
		innerErr := errors.New("invalid toml")
		err := &ConfigParseError{
			Ref: "origin/main",
			Err: innerErr,
		}
		assert.Equal(t, innerErr, err.Unwrap())
	})

	t.Run("IsConfigParseError with ConfigParseError", func(t *testing.T) {
		innerErr := errors.New("invalid toml")
		err := &ConfigParseError{
			Ref: "origin/main",
			Err: innerErr,
		}
		assert.True(t, IsConfigParseError(err))
	})

	t.Run("IsConfigParseError with wrapped ConfigParseError", func(t *testing.T) {
		innerErr := errors.New("invalid toml")
		cfgErr := &ConfigParseError{
			Ref: "origin/main",
			Err: innerErr,
		}
		wrappedErr := fmt.Errorf("wrapped: %w", cfgErr)
		assert.True(t, IsConfigParseError(wrappedErr))
	})

	t.Run("IsConfigParseError with toml.ParseError", func(t *testing.T) {
		// Create a TOML parse error
		_, err := toml.Decode("invalid", &Config{})
		require.Error(t, err)
		assert.True(t, IsConfigParseError(err))
	})

	t.Run("IsConfigParseError with other error", func(t *testing.T) {
		otherErr := errors.New("some other error")
		assert.False(t, IsConfigParseError(otherErr))
	})
}

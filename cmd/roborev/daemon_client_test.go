package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"
)

func TestIsTransportError(t *testing.T) {
	t.Run("url.Error wrapping OpError is transport error", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
		}}
		if !isTransportError(err) {
			t.Error("expected url.Error+OpError to be classified as transport error")
		}
	})

	t.Run("url.Error without OpError is not transport error", func(t *testing.T) {
		err := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: errors.New("some non-transport error")}
		if isTransportError(err) {
			t.Error("expected url.Error without net.OpError to NOT be transport error")
		}
	})

	t.Run("registerRepoError is not transport error", func(t *testing.T) {
		err := &registerRepoError{StatusCode: 500, Body: "internal error"}
		if isTransportError(err) {
			t.Error("expected registerRepoError to NOT be transport error")
		}
	})

	t.Run("plain error is not transport error", func(t *testing.T) {
		err := fmt.Errorf("something else")
		if isTransportError(err) {
			t.Error("expected plain error to NOT be transport error")
		}
	})

	t.Run("wrapped url.Error with OpError is transport error", func(t *testing.T) {
		inner := &url.Error{Op: "Post", URL: "http://127.0.0.1:7373", Err: &net.OpError{
			Op: "dial", Net: "tcp", Err: errors.New("connection refused"),
		}}
		err := fmt.Errorf("register failed: %w", inner)
		if !isTransportError(err) {
			t.Error("expected wrapped url.Error+OpError to be transport error")
		}
	})
}

func TestRegisterRepoError(t *testing.T) {
	baseErr := &registerRepoError{StatusCode: 500, Body: "internal server error"}
	err := fmt.Errorf("wrapped: %w", baseErr)

	var regErr *registerRepoError
	if !errors.As(err, &regErr) {
		t.Error("expected errors.As to match registerRepoError through wrapper")
	}
	if regErr.StatusCode != 500 {
		t.Errorf("expected StatusCode 500, got %d", regErr.StatusCode)
	}
}

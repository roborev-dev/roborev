package main

import (
	"testing"
	"time"
)

func TestParseSinceDuration(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		checkFn func(t *testing.T, got time.Time)
	}{
		{
			input: "7d",
			checkFn: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().AddDate(0, 0, -7)
				diff := got.Sub(expected)
				if diff < -time.Second || diff > time.Second {
					t.Errorf("7d: got %v, want ~%v", got, expected)
				}
			},
		},
		{
			input: "30d",
			checkFn: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().AddDate(0, 0, -30)
				diff := got.Sub(expected)
				if diff < -time.Second || diff > time.Second {
					t.Errorf("30d: got %v, want ~%v", got, expected)
				}
			},
		},
		{
			input: "2w",
			checkFn: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().AddDate(0, 0, -14)
				diff := got.Sub(expected)
				if diff < -time.Second || diff > time.Second {
					t.Errorf("2w: got %v, want ~%v", got, expected)
				}
			},
		},
		{
			input: "720h",
			checkFn: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().Add(-720 * time.Hour)
				diff := got.Sub(expected)
				if diff < -time.Second || diff > time.Second {
					t.Errorf("720h: got %v, want ~%v", got, expected)
				}
			},
		},
		{
			input: "",
			checkFn: func(t *testing.T, got time.Time) {
				t.Helper()
				expected := time.Now().AddDate(0, 0, -30)
				diff := got.Sub(expected)
				if diff < -time.Second || diff > time.Second {
					t.Errorf("empty: got %v, want ~%v (30d default)", got, expected)
				}
			},
		},
		{input: "invalid", wantErr: true},
		{input: "0d", wantErr: true},
		{input: "-5d", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseSinceDuration(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseSinceDuration(%q) expected error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSinceDuration(%q) unexpected error: %v", tt.input, err)
			}
			if tt.checkFn != nil {
				tt.checkFn(t, got)
			}
		})
	}
}

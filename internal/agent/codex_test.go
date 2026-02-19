package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCodex_buildArgs(t *testing.T) {
	a := NewCodexAgent("codex")

	tests := []struct {
		name             string
		agentic          bool
		autoApprove      bool
		wantFlags        []string
		wantMissingFlags []string
	}{
		{
			name:             "NonAgenticAutoApprove",
			agentic:          false,
			autoApprove:      true,
			wantFlags:        []string{codexAutoApproveFlag, "--json"},
			wantMissingFlags: []string{codexDangerousFlag},
		},
		{
			name:             "AgenticNoAutoApprove",
			agentic:          true,
			autoApprove:      false,
			wantFlags:        []string{codexDangerousFlag, "--json"},
			wantMissingFlags: []string{codexAutoApproveFlag},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := a.buildArgs("/repo", tt.agentic, tt.autoApprove)

			for _, flag := range tt.wantFlags {
				if !containsString(args, flag) {
					t.Errorf("buildArgs() missing expected flag %q, args: %v", flag, args)
				}
			}

			for _, flag := range tt.wantMissingFlags {
				if containsString(args, flag) {
					t.Errorf("buildArgs() contains unexpected flag %q, args: %v", flag, args)
				}
			}

			// Verify stdin marker "-" is at the end (after all flags)
			if args[len(args)-1] != "-" {
				t.Errorf("expected stdin marker '-' at end of args, got %v", args)
			}
		})
	}
}

func TestCodexSupportsDangerousFlagAllowsNonZeroHelp(t *testing.T) {
	cmdPath := writeTempCommand(t, "#!/bin/sh\necho \"usage "+codexDangerousFlag+"\"; exit 1\n")

	supported, err := codexSupportsDangerousFlag(context.Background(), cmdPath)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !supported {
		t.Fatalf("expected dangerous flag support, got false")
	}
}

func TestCodexReviewUnsafeMissingFlagErrors(t *testing.T) {
	withUnsafeAgents(t, true)

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage\"; exit 0; fi\nexit 0\n")

	a := NewCodexAgent(cmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported flag error, got %v", err)
	}
}

func TestCodexReviewAlwaysAddsAutoApprove(t *testing.T) {
	withUnsafeAgents(t, false)

	mock := mockAgentCLI(t, MockCLIOpts{
		HelpOutput:  "usage " + codexAutoApproveFlag,
		CaptureArgs: true,
		StdoutLines: []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
		},
	})

	a := NewCodexAgent(mock.CmdPath)
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	args, err := os.ReadFile(mock.ArgsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if !strings.Contains(string(args), codexAutoApproveFlag) {
		t.Fatalf("expected %s in args, got %s", codexAutoApproveFlag, strings.TrimSpace(string(args)))
	}
}

func TestCodexParseStreamJSON(t *testing.T) {
	a := NewCodexAgent("codex")

	const (
		jsonThreadStarted = `{"type":"thread.started","thread_id":"abc123"}`
		jsonTurnStarted   = `{"type":"turn.started"}`
		jsonTurnCompleted = `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}`
	)

	tests := []struct {
		name        string
		input       string
		want        string
		wantErr     error
		checkWriter bool
		extraCheck  func(*testing.T, error)
	}{
		{
			name: "AggregatesAgentMessages",
			input: strings.Join([]string{
				jsonThreadStarted,
				jsonTurnStarted,
				`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"First message"}}`,
				`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Final review result"}}`,
				jsonTurnCompleted,
			}, "\n") + "\n",
			want: "First message\nFinal review result",
		},
		{
			name: "AggregatesByItemID",
			input: strings.Join([]string{
				`{"type":"item.updated","item":{"id":"item_1","type":"agent_message","text":"Draft"}}`,
				`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Second message"}}`,
				`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Final first message"}}`,
			}, "\n") + "\n",
			want: "Final first message\nSecond message",
		},
		{
			name:  "EmptyStreamReturnsEmpty",
			input: strings.Join([]string{jsonThreadStarted, jsonTurnStarted, `{"type":"turn.completed","usage":{}}`}, "\n") + "\n",
			want:  "",
		},
		{
			name:    "TurnFailedReturnsError",
			input:   `{"type":"turn.failed","error":{"message":"something broke"}}` + "\n",
			wantErr: errCodexStreamFailed,
			extraCheck: func(t *testing.T, err error) {
				if errors.Is(err, errNoCodexJSON) {
					t.Errorf("got errNoCodexJSON, want only stream failure")
				}
			},
		},
		{
			name:    "ErrorEventReturnsError",
			input:   `{"type":"error","message":"stream error"}` + "\n",
			wantErr: errCodexStreamFailed,
			extraCheck: func(t *testing.T, err error) {
				if errors.Is(err, errNoCodexJSON) {
					t.Errorf("got errNoCodexJSON, want only stream failure")
				}
			},
		},
		{
			name: "IgnoresNonMessageItems",
			input: strings.Join([]string{
				`{"type":"item.started","item":{"id":"cmd1","type":"command_execution","command":"bash -lc ls"}}`,
				`{"type":"item.completed","item":{"id":"cmd1","type":"command_execution","command":"bash -lc ls","exit_code":0}}`,
				`{"type":"item.completed","item":{"id":"msg1","type":"agent_message","text":"Done reviewing."}}`,
			}, "\n") + "\n",
			want: "Done reviewing.",
		},
		{
			name:        "StreamsToWriter",
			input:       `{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}` + "\n",
			want:        "hello",
			checkWriter: true,
		},
		{
			name:    "NoValidJSONReturnsError",
			input:   "plain text output\nanother plain text line\n",
			wantErr: errNoCodexJSON,
		},
		{
			name: "JSONWithoutCodexEventTypeReturnsError",
			input: strings.Join([]string{
				`{"foo":"bar"}`,
				`{"type":""}`,
				`{"type":"foo.bar"}`,
			}, "\n") + "\n",
			wantErr: errNoCodexJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			var w *syncWriter
			if tt.checkWriter {
				w = newSyncWriter(&buf)
			}

			result, err := a.parseStreamJSON(strings.NewReader(tt.input), w)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("parseStreamJSON() error = %v, wantErr %v", err, tt.wantErr)
				}
				if result != "" {
					t.Errorf("parseStreamJSON() result = %q, want empty string on error", result)
				}
				if tt.extraCheck != nil {
					tt.extraCheck(t, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStreamJSON() unexpected error: %v", err)
			}

			if result != tt.want {
				t.Errorf("parseStreamJSON() = %q, want %q", result, tt.want)
			}

			if tt.checkWriter {
				if !strings.Contains(buf.String(), "agent_message") {
					t.Errorf("writer output = %q, expected to contain 'agent_message'", buf.String())
				}
			}
		})
	}
}

func TestCodexReviewPipesPromptViaStdin(t *testing.T) {
	withUnsafeAgents(t, false)

	mock := mockAgentCLI(t, MockCLIOpts{
		HelpOutput:   "usage " + codexAutoApproveFlag,
		CaptureStdin: true,
		StdoutLines: []string{
			`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
		},
	})

	a := NewCodexAgent(mock.CmdPath)
	testPrompt := "This is a test prompt with special chars: <>&\nand newlines"
	if _, err := a.Review(context.Background(), t.TempDir(), "deadbeef", testPrompt, nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	received, err := os.ReadFile(mock.StdinFile)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	if string(received) != testPrompt {
		t.Fatalf("prompt not piped correctly via stdin\nexpected: %q\ngot: %q", testPrompt, string(received))
	}
}

func TestCodexReviewNoValidJSONReturnsError(t *testing.T) {
	withUnsafeAgents(t, false)

	cmdPath := writeTempCommand(t, "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then echo \"usage "+codexAutoApproveFlag+"\"; exit 0; fi\necho \"plain text output\"\nexit 0\n")

	a := NewCodexAgent(cmdPath)
	_, err := a.Review(context.Background(), t.TempDir(), "deadbeef", "prompt", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "did not emit valid --json events") {
		t.Fatalf("expected compatibility error, got %v", err)
	}
	if !errors.Is(err, errNoCodexJSON) {
		t.Fatalf("expected wrapped errNoCodexJSON, got %v", err)
	}
}

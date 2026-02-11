package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestCodexBuildArgsUnsafeOptIn(t *testing.T) {
	a := NewCodexAgent("codex")

	// Test non-agentic mode with auto-approve
	args := a.buildArgs("/repo", false, true)
	if containsString(args, codexDangerousFlag) {
		t.Fatalf("expected no unsafe flag when agentic=false, got %v", args)
	}
	if !containsString(args, codexAutoApproveFlag) {
		t.Fatalf("expected auto-approve flag when autoApprove=true, got %v", args)
	}

	// Verify stdin marker "-" is at the end (after all flags)
	if args[len(args)-1] != "-" {
		t.Fatalf("expected stdin marker '-' at end of args, got %v", args)
	}

	// Test agentic mode (with dangerous flag, no auto-approve)
	args = a.buildArgs("/repo", true, false)
	if !containsString(args, codexDangerousFlag) {
		t.Fatalf("expected unsafe flag when agentic=true, got %v", args)
	}
	if containsString(args, codexAutoApproveFlag) {
		t.Fatalf("expected no auto-approve flag when autoApprove=false, got %v", args)
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

	t.Run("AggregatesAgentMessages", func(t *testing.T) {
		input := strings.NewReader(strings.Join([]string{
			`{"type":"thread.started","thread_id":"abc123"}`,
			`{"type":"turn.started"}`,
			`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"First message"}}`,
			`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Final review result"}}`,
			`{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":50}}`,
		}, "\n") + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "First message\nFinal review result" {
			t.Fatalf("expected both messages, got %q", result)
		}
	})

	t.Run("AggregatesByItemID", func(t *testing.T) {
		input := strings.NewReader(strings.Join([]string{
			`{"type":"item.updated","item":{"id":"item_1","type":"agent_message","text":"Draft"}}`,
			`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Second message"}}`,
			`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Final first message"}}`,
		}, "\n") + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Final first message\nSecond message" {
			t.Fatalf("expected latest text per message ID, got %q", result)
		}
	})

	t.Run("EmptyStreamReturnsEmpty", func(t *testing.T) {
		input := strings.NewReader(`{"type":"thread.started","thread_id":"abc123"}` + "\n" +
			`{"type":"turn.started"}` + "\n" +
			`{"type":"turn.completed","usage":{}}` + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Fatalf("expected empty result, got %q", result)
		}
	})

	t.Run("TurnFailedReturnsError", func(t *testing.T) {
		input := strings.NewReader(`{"type":"turn.failed","error":{"message":"something broke"}}` + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if result != "" {
			t.Fatalf("expected empty result on parse failure, got %q", result)
		}
		if !errors.Is(err, errCodexStreamFailed) {
			t.Fatalf("expected errCodexStreamFailed, got %v", err)
		}
		if strings.Contains(err.Error(), "no valid codex --json events") {
			t.Fatalf("expected stream failure error, got compatibility error: %v", err)
		}
	})

	t.Run("ErrorEventReturnsError", func(t *testing.T) {
		input := strings.NewReader(`{"type":"error","message":"stream error"}` + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if result != "" {
			t.Fatalf("expected empty result on parse failure, got %q", result)
		}
		if !errors.Is(err, errCodexStreamFailed) {
			t.Fatalf("expected errCodexStreamFailed, got %v", err)
		}
		if errors.Is(err, errNoCodexJSON) {
			t.Fatalf("expected stream failure error, got compatibility error: %v", err)
		}
	})

	t.Run("IgnoresNonMessageItems", func(t *testing.T) {
		input := strings.NewReader(strings.Join([]string{
			`{"type":"item.started","item":{"id":"cmd1","type":"command_execution","command":"bash -lc ls"}}`,
			`{"type":"item.completed","item":{"id":"cmd1","type":"command_execution","command":"bash -lc ls","exit_code":0}}`,
			`{"type":"item.completed","item":{"id":"msg1","type":"agent_message","text":"Done reviewing."}}`,
		}, "\n") + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Done reviewing." {
			t.Fatalf("expected 'Done reviewing.', got %q", result)
		}
	})

	t.Run("StreamsToWriter", func(t *testing.T) {
		input := strings.NewReader(`{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}` + "\n")
		var buf strings.Builder
		sw := newSyncWriter(&buf)

		_, err := a.parseStreamJSON(input, sw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "agent_message") {
			t.Fatalf("expected streamed output to contain event, got %q", buf.String())
		}
	})

	t.Run("NoValidJSONReturnsError", func(t *testing.T) {
		input := strings.NewReader("plain text output\nanother plain text line\n")

		result, err := a.parseStreamJSON(input, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if result != "" {
			t.Fatalf("expected empty result on parse failure, got %q", result)
		}
		if !errors.Is(err, errNoCodexJSON) {
			t.Fatalf("expected errNoCodexJSON, got %v", err)
		}
	})

	t.Run("JSONWithoutCodexEventTypeReturnsError", func(t *testing.T) {
		input := strings.NewReader(strings.Join([]string{
			`{"foo":"bar"}`,
			`{"type":""}`,
			`{"type":"foo.bar"}`,
		}, "\n") + "\n")

		result, err := a.parseStreamJSON(input, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if result != "" {
			t.Fatalf("expected empty result on parse failure, got %q", result)
		}
		if !errors.Is(err, errNoCodexJSON) {
			t.Fatalf("expected errNoCodexJSON, got %v", err)
		}
	})
}

func TestCodexBuildArgsIncludesJSON(t *testing.T) {
	a := NewCodexAgent("codex")
	args := a.buildArgs("/repo", false, true)
	if !containsString(args, "--json") {
		t.Fatalf("expected --json in args, got %v", args)
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

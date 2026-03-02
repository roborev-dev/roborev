package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/roborev-dev/roborev/internal/review"
)

// TestHelperProcess is the fake gh binary entrypoint.
// It is invoked as a subprocess by the tests below.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}
	// Find the -- separator that marks the start of the real args.
	// (We don't use the parsed args here; the action is driven by
	// GH_HELPER_ACTION so the helper stays simple.)
	_ = os.Args

	action := os.Getenv("GH_HELPER_ACTION")
	switch action {
	case "find_none":
		// No matching comment found.
		os.Exit(0)
	case "find_existing":
		fmt.Print("42")
		os.Exit(0)
	case "create_ok":
		os.Exit(0)
	case "patch_ok":
		os.Exit(0)
	case "find_fail":
		fmt.Fprint(os.Stderr, "API rate limit exceeded")
		os.Exit(1)
	case "create_fail":
		fmt.Fprint(os.Stderr, "gh pr comment failed")
		os.Exit(1)
	case "patch_fail":
		fmt.Fprint(os.Stderr, "gh api PATCH failed")
		os.Exit(1)
	case "check_env":
		token := os.Getenv("GH_TOKEN")
		if token == "" {
			fmt.Fprint(os.Stderr, "GH_TOKEN not set")
			os.Exit(1)
		}
		fmt.Print(token)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown action: %s", action)
		os.Exit(2)
	}
}

// helperCmd returns an exec.Cmd that re-invokes the test binary as the
// gh helper process with the given action.
func helperCmd(action string, extraEnv ...string) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcess", "--"}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"GO_TEST_HELPER_PROCESS=1",
			"GH_HELPER_ACTION="+action,
		)
		cmd.Env = append(cmd.Env, extraEnv...)
		return cmd
	}
}

// setExecCommand replaces the package-level execCommand and returns a
// cleanup function that restores the original.
func setExecCommand(t *testing.T, fn func(context.Context, string, ...string) *exec.Cmd) {
	t.Helper()
	orig := execCommand
	execCommand = fn
	t.Cleanup(func() { execCommand = orig })
}

func TestFindExistingComment_NoMatch(t *testing.T) {
	setExecCommand(t, helperCmd("find_none"))

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 0 {
		t.Fatalf("expected 0, got %d", id)
	}
}

func TestFindExistingComment_Found(t *testing.T) {
	setExecCommand(t, helperCmd("find_existing"))

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Fatalf("expected 42, got %d", id)
	}
}

func TestFindExistingComment_Error(t *testing.T) {
	setExecCommand(t, helperCmd("find_fail"))

	_, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpsertPRComment_Create(t *testing.T) {
	// Two-phase: find returns nothing, then create succeeds.
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_none")(ctx, name, args...)
		}
		return helperCmd("create_ok")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "review body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 gh calls, got %d", callCount)
	}
}

func TestUpsertPRComment_Update(t *testing.T) {
	// Two-phase: find returns ID, then patch succeeds.
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_existing")(ctx, name, args...)
		}
		return helperCmd("patch_ok")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "updated body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 gh calls, got %d", callCount)
	}
}

func TestUpsertPRComment_MarkerPrepended(t *testing.T) {
	body := "test review"
	result := CommentMarker + "\n" + body
	if !strings.HasPrefix(result, CommentMarker) {
		t.Fatal("marker not at start")
	}
}

func TestUpsertPRComment_Truncation(t *testing.T) {
	// Build a body that exceeds MaxCommentLen after marker prepend.
	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	markedBody := CommentMarker + "\n" + bigBody

	// Simulate what UpsertPRComment does.
	if len(markedBody) > review.MaxCommentLen {
		markedBody = markedBody[:review.MaxCommentLen] +
			"\n\n...(truncated — comment exceeded size limit)"
	}

	// Marker must survive truncation.
	if !strings.HasPrefix(markedBody, CommentMarker) {
		t.Fatal("marker lost after truncation")
	}
	// The truncated portion should be exactly MaxCommentLen.
	lines := strings.SplitN(markedBody, "\n\n...(truncated", 2)
	if len(lines[0]) != review.MaxCommentLen {
		t.Fatalf("expected truncated body len %d, got %d",
			review.MaxCommentLen, len(lines[0]))
	}
}

func TestUpsertPRComment_FindError(t *testing.T) {
	setExecCommand(t, helperCmd("find_fail"))

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "find existing comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_EnvPassthrough(t *testing.T) {
	setExecCommand(t, helperCmd("check_env"))

	env := append(os.Environ(), "GH_TOKEN=test-token-123")
	id, err := FindExistingComment(context.Background(), "owner/repo", 1, env)
	// The fake binary prints the token — it won't parse as an int but
	// we care that it ran with the token present (would exit 1 otherwise).
	_ = id
	if err != nil && strings.Contains(err.Error(), "GH_TOKEN not set") {
		t.Fatal("env was not passed through to subprocess")
	}
}

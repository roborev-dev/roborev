package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	case "patch_fail_403":
		fmt.Fprint(os.Stderr, "HTTP 403: Resource not accessible by integration")
		os.Exit(1)
	case "patch_fail_404":
		fmt.Fprint(os.Stderr, "HTTP 404: Not Found")
		os.Exit(1)
	case "find_multi_line":
		// Simulate --paginate producing multiple IDs across pages.
		fmt.Print("10\n20\n30\n")
		os.Exit(0)
	case "capture_stdin":
		// Read stdin and write it to a temp file so the test can inspect it.
		data, _ := io.ReadAll(os.Stdin)
		path := os.Getenv("GH_CAPTURE_FILE")
		if path != "" {
			_ = os.WriteFile(path, data, 0o644)
		}
		os.Exit(0)
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
	// Exercise UpsertPRComment and capture the stdin sent to "gh pr comment"
	// to verify the marker is prepended.
	captureFile := filepath.Join(t.TempDir(), "stdin.txt")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_none")(ctx, name, args...)
		}
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "test review", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	body := string(data)
	if !strings.HasPrefix(body, CommentMarker+"\n") {
		t.Fatalf("marker not at start of body: %q", body[:min(80, len(body))])
	}
}

func TestUpsertPRComment_Truncation(t *testing.T) {
	// Exercise UpsertPRComment with an oversized body and verify the
	// marker survives and the body is truncated.
	captureFile := filepath.Join(t.TempDir(), "stdin.txt")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_none")(ctx, name, args...)
		}
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	err := UpsertPRComment(context.Background(), "owner/repo", 1, bigBody, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	body := string(data)
	if !strings.HasPrefix(body, CommentMarker) {
		t.Fatal("marker lost after truncation")
	}
	if !strings.Contains(body, "truncated") {
		t.Fatal("expected truncation suffix")
	}
	// Verify total length does not exceed MaxCommentLen.
	if len(body) > review.MaxCommentLen {
		t.Fatalf("truncated body len %d exceeds MaxCommentLen %d",
			len(body), review.MaxCommentLen)
	}
	if !strings.Contains(body, "truncated") {
		t.Fatal("expected truncation suffix in body")
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

func TestFindExistingComment_MultiLineOutput(t *testing.T) {
	// When --paginate produces multiple IDs across pages, only the first
	// non-empty line should be used.
	setExecCommand(t, helperCmd("find_multi_line"))

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 10 {
		t.Fatalf("expected first ID 10, got %d", id)
	}
}

func TestUpsertPRComment_PATCHPayloadIsValidJSON(t *testing.T) {
	// Exercise the update path and verify the PATCH stdin is valid JSON
	// with a "body" key containing the marker.
	captureFile := filepath.Join(t.TempDir(), "patch.json")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_existing")(ctx, name, args...)
		}
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	// Include control characters (\v, \a) that strconv.Quote would escape
	// with Go-specific sequences invalid in JSON.
	inputBody := "body with\nnewlines\tand\ttabs\vvertical-tab\abell"
	err := UpsertPRComment(context.Background(), "owner/repo", 1, inputBody, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}

	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("PATCH payload is not valid JSON: %v\npayload: %s", err, string(data))
	}
	body, ok := payload["body"]
	if !ok {
		t.Fatal("PATCH payload missing 'body' key")
	}
	if !strings.HasPrefix(body, CommentMarker) {
		t.Fatalf("PATCH body missing marker: %q", body[:min(80, len(body))])
	}
	// Verify control characters round-tripped through JSON correctly.
	expectedBody := CommentMarker + "\n" + inputBody
	if body != expectedBody {
		t.Fatalf("body round-trip mismatch:\n got: %q\nwant: %q", body, expectedBody)
	}
}

func TestUpsertPRComment_CreateFail(t *testing.T) {
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_none")(ctx, name, args...)
		}
		return helperCmd("create_fail")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gh pr comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_PatchFail403FallsBackToCreate(t *testing.T) {
	// When PATCH fails with 403, UpsertPRComment should fall back
	// to creating a new comment.
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		switch callCount {
		case 1:
			return helperCmd("find_existing")(ctx, name, args...)
		case 2:
			return helperCmd("patch_fail_403")(ctx, name, args...)
		default:
			return helperCmd("create_ok")(ctx, name, args...)
		}
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 gh calls (find+patch+create), got %d", callCount)
	}
}

func TestUpsertPRComment_PatchFail404FallsBackToCreate(t *testing.T) {
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		switch callCount {
		case 1:
			return helperCmd("find_existing")(ctx, name, args...)
		case 2:
			return helperCmd("patch_fail_404")(ctx, name, args...)
		default:
			return helperCmd("create_ok")(ctx, name, args...)
		}
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 gh calls (find+patch+create), got %d", callCount)
	}
}

func TestUpsertPRComment_PatchFailNon403ReturnsError(t *testing.T) {
	// Non-ownership PATCH errors (e.g. network/rate-limit) should
	// propagate instead of silently creating a duplicate.
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_existing")(ctx, name, args...)
		}
		return helperCmd("patch_fail")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "patch comment") {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 gh calls (find+patch), got %d", callCount)
	}
}

func TestUpsertPRComment_TruncationUTF8Safe(t *testing.T) {
	// Place a 4-byte emoji (😀) so it straddles the actual cut
	// boundary (maxBody = MaxCommentLen - len(truncSuffix)).
	// After marker prepend the full body is:
	//   CommentMarker + "\n" + input
	// The cut point in the full body is at byte offset maxBody.
	const truncSuffix = "\n\n...(truncated — comment exceeded size limit)"
	maxBody := review.MaxCommentLen - len(truncSuffix)
	markerOverhead := len(CommentMarker) + 1 // marker + "\n"
	// We want the emoji to start 2 bytes before the cut point
	// so a naive byte slice would land inside the 4-byte emoji.
	paddingLen := maxBody - markerOverhead - 2
	input := strings.Repeat("x", paddingLen) + "😀" + strings.Repeat("y", 100)

	captureFile := filepath.Join(t.TempDir(), "stdin.txt")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_none")(ctx, name, args...)
		}
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	body := string(data)
	if !utf8.ValidString(body) {
		t.Fatal("truncated body is not valid UTF-8")
	}
	if len(body) > review.MaxCommentLen {
		t.Fatalf("truncated body len %d exceeds MaxCommentLen %d",
			len(body), review.MaxCommentLen)
	}
	if !strings.Contains(body, "truncated") {
		t.Fatal("expected truncation suffix")
	}
}

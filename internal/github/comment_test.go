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

func mockGHSequence(t *testing.T, actions ...string) {
	t.Helper()
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if callCount < len(actions) {
			action := actions[callCount]
			callCount++
			return helperCmd(action)(ctx, name, args...)
		}
		t.Fatalf("unexpected extra gh call: %v", args)
		return nil
	})
	t.Cleanup(func() {
		if callCount != len(actions) {
			t.Errorf("expected %d gh calls, got %d", len(actions), callCount)
		}
	})
}

// setupCaptureMock configures the exec mock to execute prefixActions and then capture stdin.
// It returns a function that can be called to read the captured data.
func setupCaptureMock(t *testing.T, prefixActions ...string) func() string {
	t.Helper()
	captureFile := filepath.Join(t.TempDir(), "capture.txt")

	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if callCount < len(prefixActions) {
			action := prefixActions[callCount]
			callCount++
			return helperCmd(action)(ctx, name, args...)
		}
		callCount++
		if callCount == len(prefixActions)+1 {
			return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
		}
		t.Fatalf("unexpected extra gh call: %v", args)
		return nil
	})

	t.Cleanup(func() {
		if callCount != len(prefixActions)+1 {
			t.Errorf("expected %d gh calls, got %d", len(prefixActions)+1, callCount)
		}
	})

	return func() string {
		t.Helper()
		data, err := os.ReadFile(captureFile)
		if err != nil {
			t.Fatalf("read capture file: %v", err)
		}
		return string(data)
	}
}

func assertTruncatedBody(t *testing.T, body string) {
	t.Helper()
	if !strings.HasPrefix(body, CommentMarker) {
		t.Fatal("marker lost after truncation")
	}
	if !strings.Contains(body, "truncated") {
		t.Fatal("expected truncation suffix")
	}
	if len(body) > review.MaxCommentLen {
		t.Fatalf("truncated body len %d exceeds MaxCommentLen %d", len(body), review.MaxCommentLen)
	}
}

func TestFindExistingComment_NoMatch(t *testing.T) {
	mockGHSequence(t, "find_none")

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 0 {
		t.Fatalf("expected 0, got %d", id)
	}
}

func TestFindExistingComment_Found(t *testing.T) {
	mockGHSequence(t, "find_existing")

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Fatalf("expected 42, got %d", id)
	}
}

func TestFindExistingComment_Error(t *testing.T) {
	mockGHSequence(t, "find_fail")

	_, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpsertPRComment_Create(t *testing.T) {
	mockGHSequence(t, "find_none", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "review body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_Update(t *testing.T) {
	mockGHSequence(t, "find_existing", "patch_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "updated body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_MarkerPrepended(t *testing.T) {
	readCapture := setupCaptureMock(t, "find_none")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "test review", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := readCapture()
	if !strings.HasPrefix(body, CommentMarker+"\n") {
		t.Fatalf("marker not at start of body: %q", body[:min(80, len(body))])
	}
}

func TestUpsertPRComment_Truncation(t *testing.T) {
	readCapture := setupCaptureMock(t, "find_none")

	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	err := UpsertPRComment(context.Background(), "owner/repo", 1, bigBody, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertTruncatedBody(t, readCapture())
}

func TestUpsertPRComment_FindError(t *testing.T) {
	mockGHSequence(t, "find_fail")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "find existing comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_EnvPassthrough(t *testing.T) {
	mockGHSequence(t, "check_env")

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
	// When --paginate produces multiple IDs across pages, the last
	// non-empty line (newest comment) should be used.
	mockGHSequence(t, "find_multi_line")

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 30 {
		t.Fatalf("expected last ID 30, got %d", id)
	}
}

func TestUpsertPRComment_PATCHPayloadIsValidJSON(t *testing.T) {
	readCapture := setupCaptureMock(t, "find_existing")

	// Include control characters (\v, \a) that strconv.Quote would escape
	// with Go-specific sequences invalid in JSON.
	inputBody := "body with\nnewlines\tand\ttabs\vvertical-tab\abell"
	err := UpsertPRComment(context.Background(), "owner/repo", 1, inputBody, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data := readCapture()

	var payload map[string]string
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
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
	mockGHSequence(t, "find_none", "create_fail")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "gh pr comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_PatchFail403FallsBackToCreate(t *testing.T) {
	mockGHSequence(t, "find_existing", "patch_fail_403", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_PatchFail404FallsBackToCreate(t *testing.T) {
	mockGHSequence(t, "find_existing", "patch_fail_404", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_PatchFailNon403ReturnsError(t *testing.T) {
	mockGHSequence(t, "find_existing", "patch_fail")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "patch comment") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpsertPRComment_MultipleIDs_PatchNewestFails403(t *testing.T) {
	mockGHSequence(t, "find_multi_line", "patch_fail_403", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreatePRComment_AlwaysCreates(t *testing.T) {
	readCapture := setupCaptureMock(t)

	err := CreatePRComment(context.Background(), "owner/repo", 1, "test body", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := readCapture()
	if !strings.HasPrefix(body, CommentMarker+"\n") {
		t.Fatalf("marker not at start of body: %q", body[:min(80, len(body))])
	}
	if !strings.Contains(body, "test body") {
		t.Fatal("body content missing")
	}
}

func TestCreatePRComment_Truncation(t *testing.T) {
	readCapture := setupCaptureMock(t)

	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	err := CreatePRComment(context.Background(), "owner/repo", 1, bigBody, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertTruncatedBody(t, readCapture())
}

func TestUpsertPRComment_TruncationUTF8Safe(t *testing.T) {
	const truncSuffix = "\n\n...(truncated — comment exceeded size limit)"
	maxBody := review.MaxCommentLen - len(truncSuffix)
	markerOverhead := len(CommentMarker) + 1 // marker + "\n"
	paddingLen := maxBody - markerOverhead - 2
	input := strings.Repeat("x", paddingLen) + "😀" + strings.Repeat("y", 100)

	readCapture := setupCaptureMock(t, "find_none")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := readCapture()
	if !utf8.ValidString(body) {
		t.Fatal("truncated body is not valid UTF-8")
	}
	assertTruncatedBody(t, body)
}

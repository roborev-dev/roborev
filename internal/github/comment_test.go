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
	"github.com/stretchr/testify/require"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TEST_HELPER_PROCESS") != "1" {
		return
	}

	_ = os.Args

	action := os.Getenv("GH_HELPER_ACTION")
	switch action {
	case "find_none":

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

		fmt.Print("10\n20\n30\n")
		os.Exit(0)
	case "capture_stdin":

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

func setExecCommand(t *testing.T, fn func(context.Context, string, ...string) *exec.Cmd) {
	t.Helper()
	orig := execCommand
	execCommand = fn
	t.Cleanup(func() { execCommand = orig })
}

// mockGHSequence sets up execCommand to return a different helperCmd
// action for each successive call, cycling through the given actions
// in order. It returns a pointer to the call count for assertions.
func mockGHSequence(t *testing.T, actions ...string) *int {
	t.Helper()
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		idx := callCount - 1
		if idx >= len(actions) {
			idx = len(actions) - 1
		}
		return helperCmd(actions[idx])(ctx, name, args...)
	})
	return &callCount
}

// setupCaptureMock sets up execCommand so that the first len(prefixActions)
// calls use the given actions, and subsequent calls use "capture_stdin"
// writing stdin to a temp file. It returns the capture file path and a
// pointer to the call count.
func setupCaptureMock(t *testing.T, prefixActions ...string) (captureFile string, callCount *int) {
	t.Helper()
	captureFile = filepath.Join(t.TempDir(), "stdin.txt")
	count := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		count++
		if count <= len(prefixActions) {
			return helperCmd(prefixActions[count-1])(ctx, name, args...)
		}
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})
	return captureFile, &count
}

// readCapturedBody reads the captured stdin from a file written by the
// "capture_stdin" helper process.
func readCapturedBody(t *testing.T, captureFile string) string {
	t.Helper()
	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file")
	return string(data)
}

// assertTruncatedBody verifies that body starts with CommentMarker,
// contains the truncation notice, does not exceed review.MaxCommentLen,
// and is valid UTF-8.
func assertTruncatedBody(t *testing.T, body string) {
	t.Helper()
	require.True(t, strings.HasPrefix(body, CommentMarker),
		"body should start with CommentMarker, got prefix: %q", body[:min(80, len(body))])
	require.Contains(t, body, "truncated",
		"truncated body should contain truncation notice")
	require.LessOrEqual(t, len(body), review.MaxCommentLen,
		"truncated body len %d exceeds MaxCommentLen %d", len(body), review.MaxCommentLen)
	require.True(t, utf8.ValidString(body),
		"truncated body must be valid UTF-8")
}

func TestFindExistingComment_NoMatch(t *testing.T) {
	setExecCommand(t, helperCmd("find_none"))

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 0, got %d", id)

}

func TestFindExistingComment_Found(t *testing.T) {
	setExecCommand(t, helperCmd("find_existing"))

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 42, got %d", id)

}

func TestFindExistingComment_Error(t *testing.T) {
	setExecCommand(t, helperCmd("find_fail"))

	_, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	require.Error(t, err, "expected comment lookup to fail with find command failure")

}

func TestUpsertPRComment_Create(t *testing.T) {
	callCount := mockGHSequence(t, "find_none", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "review body", nil)
	require.NoError(t, err)
	require.Equal(t, 2, *callCount, "expected 2 gh calls")
}

func TestUpsertPRComment_Update(t *testing.T) {
	callCount := mockGHSequence(t, "find_existing", "patch_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "updated body", nil)
	require.NoError(t, err)
	require.Equal(t, 2, *callCount, "expected 2 gh calls")
}

func TestUpsertPRComment_MarkerPrepended(t *testing.T) {
	captureFile, _ := setupCaptureMock(t, "find_none")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "test review", nil)
	require.NoError(t, err)
	body := readCapturedBody(t, captureFile)
	require.True(t, strings.HasPrefix(body, CommentMarker+"\n"),
		"marker not at start of body: %q", body[:min(80, len(body))])
}

func TestUpsertPRComment_Truncation(t *testing.T) {
	captureFile, _ := setupCaptureMock(t, "find_none")

	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	err := UpsertPRComment(context.Background(), "owner/repo", 1, bigBody, nil)
	require.NoError(t, err)
	body := readCapturedBody(t, captureFile)
	assertTruncatedBody(t, body)
}

func TestUpsertPRComment_FindError(t *testing.T) {
	setExecCommand(t, helperCmd("find_fail"))

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.Error(t, err, "expected UpsertPRComment to fail on find error")

	require.Contains(t, err.Error(), "find existing comment")
}

func TestUpsertPRComment_EnvPassthrough(t *testing.T) {
	setExecCommand(t, helperCmd("check_env"))

	env := append(os.Environ(), "GH_TOKEN=test-token-123")
	id, err := FindExistingComment(context.Background(), "owner/repo", 1, env)

	_ = id
	if err != nil && strings.Contains(err.Error(), "GH_TOKEN not set") {
		require.NoError(t, err)
	}
}

func TestFindExistingComment_MultiLineOutput(t *testing.T) {

	setExecCommand(t, helperCmd("find_multi_line"))

	id, err := FindExistingComment(context.Background(), "owner/repo", 1, nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected last ID 30, got %d", id)

}

func TestUpsertPRComment_PATCHPayloadIsValidJSON(t *testing.T) {
	captureFile, _ := setupCaptureMock(t, "find_existing")

	inputBody := "body with\nnewlines\tand\ttabs\vvertical-tab\abell"
	err := UpsertPRComment(context.Background(), "owner/repo", 1, inputBody, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file")

	var payload map[string]string
	require.NoError(t, json.Unmarshal(data, &payload),
		"PATCH payload is not valid JSON:\npayload: %s", string(data))
	body, ok := payload["body"]
	require.True(t, ok, "PATCH payload missing 'body' key")
	require.True(t, strings.HasPrefix(body, CommentMarker),
		"PATCH body missing marker: %q", body[:min(80, len(body))])

	expectedBody := CommentMarker + "\n" + inputBody
	require.Equal(t, expectedBody, body, "body round-trip mismatch")
}

func TestUpsertPRComment_CreateFail(t *testing.T) {
	callCount := mockGHSequence(t, "find_none", "create_fail")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gh pr comment")
	require.Equal(t, 2, *callCount, "expected 2 gh calls")
}

func TestUpsertPRComment_PatchFail403FallsBackToCreate(t *testing.T) {
	callCount := mockGHSequence(t, "find_existing", "patch_fail_403", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.NoError(t, err)
	require.Equal(t, 3, *callCount, "expected 3 gh calls (find+patch+create)")
}

func TestUpsertPRComment_PatchFail404FallsBackToCreate(t *testing.T) {
	callCount := mockGHSequence(t, "find_existing", "patch_fail_404", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.NoError(t, err)
	require.Equal(t, 3, *callCount, "expected 3 gh calls (find+patch+create)")
}

func TestUpsertPRComment_PatchFailNon403ReturnsError(t *testing.T) {
	callCount := mockGHSequence(t, "find_existing", "patch_fail")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.Error(t, err, "expected patch non-403 failure to bubble up")
	require.Contains(t, err.Error(), "patch comment")
	require.Equal(t, 2, *callCount, "expected 2 gh calls (find+patch)")
}

func TestUpsertPRComment_MultipleIDs_PatchNewestFails403(t *testing.T) {
	callCount := mockGHSequence(t, "find_multi_line", "patch_fail_403", "create_ok")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.NoError(t, err)
	require.Equal(t, 3, *callCount, "expected 3 gh calls (find+patch+create)")
}

func TestCreatePRComment_AlwaysCreates(t *testing.T) {
	captureFile, callCount := setupCaptureMock(t)

	err := CreatePRComment(context.Background(), "owner/repo", 1, "test body", nil)
	require.NoError(t, err)
	require.Equal(t, 1, *callCount, "expected 1 gh call (create only)")

	body := readCapturedBody(t, captureFile)
	require.True(t, strings.HasPrefix(body, CommentMarker+"\n"),
		"marker not at start of body: %q", body[:min(80, len(body))])
	require.Contains(t, body, "test body")
}

func TestCreatePRComment_Truncation(t *testing.T) {
	captureFile, _ := setupCaptureMock(t)

	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	err := CreatePRComment(context.Background(), "owner/repo", 1, bigBody, nil)
	require.NoError(t, err)
	body := readCapturedBody(t, captureFile)
	assertTruncatedBody(t, body)
}

func TestUpsertPRComment_TruncationUTF8Safe(t *testing.T) {
	const truncSuffix = "\n\n...(truncated — comment exceeded size limit)"
	maxBody := review.MaxCommentLen - len(truncSuffix)
	markerOverhead := len(CommentMarker) + 1

	paddingLen := maxBody - markerOverhead - 2
	input := strings.Repeat("x", paddingLen) + "\U0001f600" + strings.Repeat("y", 100)

	captureFile, _ := setupCaptureMock(t, "find_none")

	err := UpsertPRComment(context.Background(), "owner/repo", 1, input, nil)
	require.NoError(t, err)
	body := readCapturedBody(t, captureFile)
	assertTruncatedBody(t, body)
}

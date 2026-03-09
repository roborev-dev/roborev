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

	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_none")(ctx, name, args...)
		}
		return helperCmd("create_ok")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "review body", nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 2 gh calls, got %d", callCount)

}

func TestUpsertPRComment_Update(t *testing.T) {

	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_existing")(ctx, name, args...)
		}
		return helperCmd("patch_ok")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "updated body", nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 2 gh calls, got %d", callCount)

}

func TestUpsertPRComment_MarkerPrepended(t *testing.T) {

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
	require.NoError(t, err, "unexpected error: %v", err)
	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file: %v", err)
	body := string(data)
	if !strings.HasPrefix(body, CommentMarker+"\n") {
		require.NoError(t, err, "marker not at start of body: %q", body[:min(80, len(body))])
	}
}

func TestUpsertPRComment_Truncation(t *testing.T) {

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
	require.NoError(t, err, "unexpected error: %v", err)
	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file: %v", err)
	body := string(data)
	if !strings.HasPrefix(body, CommentMarker) {
		require.NoError(t, err)
	}
	if !strings.Contains(body, "truncated") {
		require.NoError(t, err)
	}

	if len(body) > review.MaxCommentLen {
		require.NoError(t, err, "truncated body len %d exceeds MaxCommentLen %d",
			len(body), review.MaxCommentLen)
	}
	if !strings.Contains(body, "truncated") {
		require.NoError(t, err)
	}
}

func TestUpsertPRComment_FindError(t *testing.T) {
	setExecCommand(t, helperCmd("find_fail"))

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.Error(t, err, "expected UpsertPRComment to fail on find error")

	if !strings.Contains(err.Error(), "find existing comment") {
		require.NoError(t, err, "unexpected error: %v", err)
	}
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

	captureFile := filepath.Join(t.TempDir(), "patch.json")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_existing")(ctx, name, args...)
		}
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	inputBody := "body with\nnewlines\tand\ttabs\vvertical-tab\abell"
	err := UpsertPRComment(context.Background(), "owner/repo", 1, inputBody, nil)
	require.NoError(t, err, "unexpected error: %v", err)

	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file: %v", err)

	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		require.NoError(t, err, "PATCH payload is not valid JSON: %v\npayload: %s", err, string(data))
	}
	body, ok := payload["body"]
	if !ok {
		require.NoError(t, err)
	}
	if !strings.HasPrefix(body, CommentMarker) {
		require.NoError(t, err, "PATCH body missing marker: %q", body[:min(80, len(body))])
	}

	expectedBody := CommentMarker + "\n" + inputBody
	require.NoError(t, err, "body round-trip mismatch:\n got: %q\nwant: %q", body, expectedBody)

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
	require.Error(t, err, "expected 403 patch failure fallback to create not to fail")

	if !strings.Contains(err.Error(), "gh pr comment") {
		require.NoError(t, err, "unexpected error: %v", err)
	}
}

func TestUpsertPRComment_PatchFail403FallsBackToCreate(t *testing.T) {

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
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 3 gh calls (find+patch+create), got %d", callCount)

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
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 3 gh calls (find+patch+create), got %d", callCount)

}

func TestUpsertPRComment_PatchFailNon403ReturnsError(t *testing.T) {

	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		if callCount == 1 {
			return helperCmd("find_existing")(ctx, name, args...)
		}
		return helperCmd("patch_fail")(ctx, name, args...)
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.Error(t, err, "expected patch non-403 failure to bubble up")

	if !strings.Contains(err.Error(), "patch comment") {
		require.NoError(t, err, "unexpected error: %v", err)
	}
	require.Equal(t, 2, callCount, "expected 2 gh calls (find+patch), got %d", callCount)

}

func TestUpsertPRComment_MultipleIDs_PatchNewestFails403(t *testing.T) {

	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		switch callCount {
		case 1:

			return helperCmd("find_multi_line")(ctx, name, args...)
		case 2:

			return helperCmd("patch_fail_403")(ctx, name, args...)
		default:

			return helperCmd("create_ok")(ctx, name, args...)
		}
	})

	err := UpsertPRComment(context.Background(), "owner/repo", 1, "body", nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 3 gh calls (find+patch+create), got %d", callCount)

}

func TestCreatePRComment_AlwaysCreates(t *testing.T) {

	captureFile := filepath.Join(t.TempDir(), "stdin.txt")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	err := CreatePRComment(context.Background(), "owner/repo", 1, "test body", nil)
	require.NoError(t, err, "unexpected error: %v", err)
	require.NoError(t, err, "expected 1 gh call (create only), got %d", callCount)

	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file: %v", err)
	body := string(data)
	if !strings.HasPrefix(body, CommentMarker+"\n") {
		require.NoError(t, err, "marker not at start of body: %q", body[:min(80, len(body))])
	}
	if !strings.Contains(body, "test body") {
		require.NoError(t, err)
	}
}

func TestCreatePRComment_Truncation(t *testing.T) {
	captureFile := filepath.Join(t.TempDir(), "stdin.txt")
	callCount := 0
	setExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		callCount++
		return helperCmd("capture_stdin", "GH_CAPTURE_FILE="+captureFile)(ctx, name, args...)
	})

	bigBody := strings.Repeat("x", review.MaxCommentLen+1000)
	err := CreatePRComment(context.Background(), "owner/repo", 1, bigBody, nil)
	require.NoError(t, err, "unexpected error: %v", err)
	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file: %v", err)
	body := string(data)
	if !strings.HasPrefix(body, CommentMarker) {
		require.NoError(t, err)
	}
	if !strings.Contains(body, "truncated") {
		require.NoError(t, err)
	}
	if len(body) > review.MaxCommentLen {
		require.NoError(t, err, "truncated body len %d exceeds MaxCommentLen %d",
			len(body), review.MaxCommentLen)
	}
}

func TestUpsertPRComment_TruncationUTF8Safe(t *testing.T) {

	const truncSuffix = "\n\n...(truncated — comment exceeded size limit)"
	maxBody := review.MaxCommentLen - len(truncSuffix)
	markerOverhead := len(CommentMarker) + 1

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
	require.NoError(t, err, "unexpected error: %v", err)
	data, err := os.ReadFile(captureFile)
	require.NoError(t, err, "read capture file: %v", err)
	body := string(data)
	if !utf8.ValidString(body) {
		require.NoError(t, err)
	}
	if len(body) > review.MaxCommentLen {
		require.NoError(t, err, "truncated body len %d exceeds MaxCommentLen %d",
			len(body), review.MaxCommentLen)
	}
	if !strings.Contains(body, "truncated") {
		require.NoError(t, err)
	}
}

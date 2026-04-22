package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicClassifierSkipReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"timeout", context.DeadlineExceeded, "classifier timed out"},
		{"wrapped timeout", errors.New("x: " + context.DeadlineExceeded.Error()), "classifier failed"},
		{"not registered", errors.New(`classifier "fake" not registered: no such agent`), "classifier unavailable"},
		{"not installed", errors.New(`classifier "claude-code" not installed (CLI not on PATH)`), "classifier unavailable"},
		{"not a schema agent", errors.New(`classify_agent "gemini" is not a SchemaAgent`), "classifier unavailable"},
		{"schema lost", errors.New(`classify_agent "claude-code" lost SchemaAgent capability after WithReasoning/WithModel`), "classifier unavailable"},
		{"exec stderr leak", errors.New(`/nix/store/abc/bin/claude: not found: /home/user/creds`), "classifier failed"},
		{"context canceled", context.Canceled, "classifier failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, publicClassifierSkipReason(tc.err))
		})
	}
}

func TestPublicClassifierSkipReason_WrappedDeadlineExceeded(t *testing.T) {
	// errors.Is should match wrapping the sentinel correctly.
	wrapped := &wrappedErr{inner: context.DeadlineExceeded}
	assert.Equal(t, "classifier timed out", publicClassifierSkipReason(wrapped))
}

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "outer: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

func TestComposeClassifyErrorDetail(t *testing.T) {
	primary := errors.New("exec: /usr/bin/claude: timeout after 30s")
	backupCfg := errors.New("classify_backup_agent \"gemini\" is not a SchemaAgent")

	t.Run("primary only", func(t *testing.T) {
		assert.Equal(t, primary.Error(),
			composeClassifyErrorDetail(primary, nil))
	})
	t.Run("primary and backup config error", func(t *testing.T) {
		got := composeClassifyErrorDetail(primary, backupCfg)
		assert.Contains(t, got, primary.Error(),
			"primary failure must be preserved")
		assert.Contains(t, got, backupCfg.Error(),
			"backup config error must be surfaced so operators "+
				"see why failover didn't run")
	})
	t.Run("nil primary", func(t *testing.T) {
		assert.Empty(t, composeClassifyErrorDetail(nil, nil))
		assert.Empty(t, composeClassifyErrorDetail(nil, backupCfg),
			"no primary error means no failure to report")
	})
}

func TestResolveClassifyDiff_UsesDiffContentWhenSet(t *testing.T) {
	prebuilt := "+already here\n"
	job := &storage.ReviewJob{
		ID:          1,
		GitRef:      "abc",
		RepoPath:    "/nonexistent",
		DiffContent: &prebuilt,
	}
	got := resolveClassifyDiff("worker-1", job)
	assert.Equal(t, prebuilt, got,
		"DiffContent must take precedence over the git fallback")
}

func TestResolveClassifyDiff_FetchesFromGitWhenEmpty(t *testing.T) {
	// Auto-design classify rows are enqueued without diff_content.
	// resolveClassifyDiff must fetch via git so the classifier sees
	// the actual change instead of an empty diff.
	repo := testutil.InitTestRepo(t)
	sha := repo.CommitFile("src/x.go", "package x\n\nfunc X() int { return 42 }\n",
		"feat: add X")

	job := &storage.ReviewJob{
		ID:       2,
		GitRef:   sha,
		RepoPath: repo.Path(),
		// DiffContent intentionally nil — this is the auto-design
		// classify row's enqueue-time state.
	}
	got := resolveClassifyDiff("worker-2", job)
	require.NotEmpty(t, got, "diff must be fetched from git when DiffContent is nil")
	assert.Contains(t, got, "+package x")
	assert.Contains(t, got, "+func X()")
}

func TestResolveClassifyDiff_SkipsFetchForDirty(t *testing.T) {
	// "dirty" is the synthetic ref used for uncommitted reviews — git
	// can't diff that as a single ref, so the fallback must short-
	// circuit instead of producing a misleading error log per call.
	job := &storage.ReviewJob{
		ID:       3,
		GitRef:   "dirty",
		RepoPath: "/somewhere",
	}
	assert.Empty(t, resolveClassifyDiff("worker-3", job))
}

func TestResolveClassifyDiff_SkipsFetchForEmptyRef(t *testing.T) {
	job := &storage.ReviewJob{
		ID:       4,
		GitRef:   "",
		RepoPath: "/somewhere",
	}
	assert.Empty(t, resolveClassifyDiff("worker-4", job))
}

// waitForEvent reads one event from ch within timeout.
func waitForEvent(t *testing.T, ch <-chan Event, timeout time.Duration) (Event, bool) {
	t.Helper()
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(timeout):
		return Event{}, false
	}
}

func TestApplyClassifyVerdict_SkipBroadcastsTerminalEvent(t *testing.T) {
	// The skip path must broadcast review.completed so CI batches
	// and other subscribers advance. Without this, a linked batch's
	// completed_jobs stays short by one until stale-batch reconciliation.
	tc := newWorkerTestContext(t, 1)

	_, err := tc.DB.GetOrCreateCommit(tc.Repo.ID, "aaaa", "Author", "s", time.Now())
	require.NoError(t, err)
	jobID, err := tc.DB.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     tc.Repo.ID,
		GitRef:     "aaaa",
		JobType:    storage.JobTypeClassify,
		ReviewType: "design",
	})
	require.NoError(t, err)
	require.NotZero(t, jobID)
	claimed, err := tc.DB.ClaimJob("worker-skip")
	require.NoError(t, err)
	require.Equal(t, jobID, claimed.ID)

	_, ch := tc.Broadcaster.Subscribe("")

	tc.Pool.applyClassifyVerdict("worker-skip", claimed, false, "trivial diff")

	ev, ok := waitForEvent(t, ch, 1*time.Second)
	require.True(t, ok, "expected review.completed broadcast after classify skip")
	assert.Equal(t, "review.completed", ev.Type)
	assert.Equal(t, claimed.ID, ev.JobID)
	assert.Equal(t, "aaaa", ev.SHA)
}

func TestApplyClassifyVerdict_PromoteDoesNotBroadcast(t *testing.T) {
	// Promote puts the row back to 'queued' — the follow-up design review
	// will emit its own terminal event when it finishes, so emitting one
	// here would double-count the batch completion.
	tc := newWorkerTestContext(t, 1)

	_, err := tc.DB.GetOrCreateCommit(tc.Repo.ID, "bbbb", "Author", "s", time.Now())
	require.NoError(t, err)
	jobID, err := tc.DB.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     tc.Repo.ID,
		GitRef:     "bbbb",
		JobType:    storage.JobTypeClassify,
		ReviewType: "design",
	})
	require.NoError(t, err)
	require.NotZero(t, jobID)
	claimed, err := tc.DB.ClaimJob("worker-promote")
	require.NoError(t, err)

	_, ch := tc.Broadcaster.Subscribe("")

	tc.Pool.applyClassifyVerdict("worker-promote", claimed, true, "worth reviewing")

	_, ok := waitForEvent(t, ch, 200*time.Millisecond)
	assert.False(t, ok, "promote path must not broadcast a terminal event")
}

func TestCompleteClassifyAsSkip_BroadcastsTerminalEvent(t *testing.T) {
	// Classifier-failure skip also needs to broadcast — otherwise CI
	// batches containing this row would wait on stale-batch reconciliation.
	tc := newWorkerTestContext(t, 1)

	_, err := tc.DB.GetOrCreateCommit(tc.Repo.ID, "cccc", "Author", "s", time.Now())
	require.NoError(t, err)
	jobID, err := tc.DB.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     tc.Repo.ID,
		GitRef:     "cccc",
		JobType:    storage.JobTypeClassify,
		ReviewType: "design",
	})
	require.NoError(t, err)
	require.NotZero(t, jobID)
	claimed, err := tc.DB.ClaimJob("worker-fail")
	require.NoError(t, err)

	_, ch := tc.Broadcaster.Subscribe("")

	tc.Pool.completeClassifyAsSkip("worker-fail", claimed, "classifier timed out", "exec: timeout")

	ev, ok := waitForEvent(t, ch, 1*time.Second)
	require.True(t, ok, "expected review.completed broadcast after classifier failure skip")
	assert.Equal(t, "review.completed", ev.Type)
	assert.Equal(t, claimed.ID, ev.JobID)
}

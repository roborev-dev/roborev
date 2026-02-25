package tui

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
	"github.com/roborev-dev/roborev/internal/worktree"
	godiff "github.com/sourcegraph/go-diff/diff"
)

// tui_actions.go contains action/mutation functions extracted from tui.go.
// These are TUI commands that perform side effects: clipboard operations,
// server API calls (addressed, cancel, rerun, comment, fix, apply patch),
// and git operations (commit, worktree management).

func formatClipboardContent(review *storage.Review) string {
	if review == nil || review.Output == "" {
		return ""
	}

	// Build header: "Review #ID /repo/path abc1234"
	// Always use job ID for consistency with queue and review screen display
	// ID priority: Job.ID → JobID → review.ID
	var id int64
	if review.Job != nil && review.Job.ID != 0 {
		id = review.Job.ID
	} else if review.JobID != 0 {
		id = review.JobID
	} else {
		id = review.ID
	}

	var header string
	if id != 0 {
		if review.Job != nil && review.Job.RepoPath != "" {
			// Include repo path and git ref when available
			gitRef := review.Job.GitRef
			// Truncate SHA if it's a full 40-char hex SHA (not a range or branch name)
			if fullSHAPattern.MatchString(gitRef) {
				gitRef = git.ShortSHA(gitRef)
			}
			header = fmt.Sprintf("Review #%d %s %s\n\n", id, review.Job.RepoPath, gitRef)
		} else {
			header = fmt.Sprintf("Review #%d\n\n", id)
		}
	}

	return header + review.Output
}

func (m model) copyToClipboard(review *storage.Review) tea.Cmd {
	view := m.currentView // Capture view at trigger time
	content := formatClipboardContent(review)
	return func() tea.Msg {
		if content == "" {
			return clipboardResultMsg{err: fmt.Errorf("no content to copy"), view: view}
		}
		err := m.clipboard.WriteText(content)
		return clipboardResultMsg{err: err, view: view}
	}
}

// postAddressed sends an addressed state change to the server.
// Translates "not found" to a context-specific error message.
func (m model) postAddressed(jobID int64, newState bool, notFoundMsg string) error {
	err := m.postJSON("/api/review/address", map[string]any{
		"job_id":    jobID,
		"addressed": newState,
	}, nil)
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("%s", notFoundMsg)
	}
	if err != nil {
		return fmt.Errorf("mark review: %w", err)
	}
	return nil
}

func (m model) addressReview(reviewID, jobID int64, newState, oldState bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		err := m.postAddressed(jobID, newState, "review not found")
		return addressedResultMsg{reviewID: reviewID, jobID: jobID, reviewView: true, oldState: oldState, newState: newState, seq: seq, err: err}
	}
}

// addressReviewInBackground updates addressed status by job ID.
// Used for optimistic updates from queue view - UI already updated, this syncs to server.
// On error, returns addressedResultMsg with oldState for rollback.
func (m model) addressReviewInBackground(jobID int64, newState, oldState bool, seq uint64) tea.Cmd {
	return func() tea.Msg {
		err := m.postAddressed(jobID, newState, "no review for this job")
		return addressedResultMsg{jobID: jobID, oldState: oldState, newState: newState, seq: seq, err: err}
	}
}

func (m model) toggleAddressedForJob(jobID int64, currentState *bool) tea.Cmd {
	return func() tea.Msg {
		newState := currentState == nil || !*currentState

		if err := m.postAddressed(jobID, newState, "no review for this job"); err != nil {
			return errMsg(err)
		}
		return addressedMsg(newState)
	}
}

// markParentAddressed marks the parent review job as addressed after a fix is applied.
func (m model) markParentAddressed(parentJobID int64) tea.Cmd {
	return func() tea.Msg {
		err := m.postAddressed(parentJobID, true, "parent review not found")
		if err != nil {
			return errMsg(err)
		}
		return nil
	}
}

// cancelJob sends a cancel request to the server
func (m model) cancelJob(jobID int64, oldStatus storage.JobStatus, oldFinishedAt *time.Time) tea.Cmd {
	return func() tea.Msg {
		err := m.postJSON("/api/job/cancel", map[string]any{"job_id": jobID}, nil)
		return cancelResultMsg{jobID: jobID, oldState: oldStatus, oldFinishedAt: oldFinishedAt, err: err}
	}
}

// rerunJob sends a rerun request to the server for failed/canceled jobs
func (m model) rerunJob(jobID int64, oldStatus storage.JobStatus, oldStartedAt, oldFinishedAt *time.Time, oldError string) tea.Cmd {
	return func() tea.Msg {
		err := m.postJSON("/api/job/rerun", map[string]any{"job_id": jobID}, nil)
		return rerunResultMsg{jobID: jobID, oldState: oldStatus, oldStartedAt: oldStartedAt, oldFinishedAt: oldFinishedAt, oldError: oldError, err: err}
	}
}

func (m model) submitComment(jobID int64, text string) tea.Cmd {
	return func() tea.Msg {
		commenter := os.Getenv("USER")
		if commenter == "" {
			commenter = "anonymous"
		}

		err := m.postJSON("/api/comment", map[string]any{
			"job_id":    jobID,
			"commenter": commenter,
			"comment":   strings.TrimSpace(text),
		}, nil)
		if err != nil {
			return commentResultMsg{jobID: jobID, err: fmt.Errorf("submit comment: %w", err)}
		}

		return commentResultMsg{jobID: jobID, err: nil}
	}
}

// triggerFix triggers a background fix job for a parent review.
func (m model) triggerFix(parentJobID int64, prompt, gitRef string) tea.Cmd {
	return func() tea.Msg {
		req := map[string]any{
			"parent_job_id": parentJobID,
		}
		if prompt != "" {
			req["prompt"] = prompt
		}
		if gitRef != "" {
			req["git_ref"] = gitRef
		}
		var job storage.ReviewJob
		err := m.postJSON("/api/job/fix", req, &job)
		if err != nil {
			return fixTriggerResultMsg{err: err}
		}
		return fixTriggerResultMsg{job: &job}
	}
}

// applyFixPatch fetches and applies the patch for a completed fix job.
// It resolves the target directory from the branch's worktree, or signals
// needWorktree if the branch is not checked out anywhere.
func (m model) applyFixPatch(jobID int64) tea.Cmd {
	return func() tea.Msg {
		patch, jobDetail, msg := m.fetchPatchAndJob(jobID)
		if msg != nil {
			return *msg
		}

		// Resolve the target directory: if the branch has its own worktree,
		// apply the patch there instead of the main repo path.
		targetDir, checkedOut, wtErr := git.WorktreePathForBranch(jobDetail.RepoPath, jobDetail.Branch)
		if wtErr != nil {
			return applyPatchResultMsg{jobID: jobID, err: wtErr}
		}
		if !checkedOut {
			return applyPatchResultMsg{
				jobID:        jobID,
				needWorktree: true,
				branch:       jobDetail.Branch,
			}
		}

		return m.checkApplyCommitPatch(jobID, jobDetail, targetDir, patch)
	}
}

// applyFixPatchInWorktree creates a temporary worktree for the branch, applies the
// patch there, commits, and removes the worktree. The commit persists on the branch.
func (m model) applyFixPatchInWorktree(jobID int64) tea.Cmd {
	return func() tea.Msg {
		patch, jobDetail, msg := m.fetchPatchAndJob(jobID)
		if msg != nil {
			return *msg
		}

		// Create a temporary worktree on the branch.
		wtDir, err := os.MkdirTemp("", "roborev-apply-")
		if err != nil {
			return applyPatchResultMsg{jobID: jobID, err: fmt.Errorf("create temp dir: %w", err)}
		}

		removeWorktree := func() {
			if err := exec.Command("git", "-C", jobDetail.RepoPath, "worktree", "remove", "--force", wtDir).Run(); err != nil {
				os.RemoveAll(wtDir)
				_ = exec.Command("git", "-C", jobDetail.RepoPath, "worktree", "prune").Run()
			}
		}

		cmd := exec.Command("git", "-C", jobDetail.RepoPath, "worktree", "add", wtDir, jobDetail.Branch)
		if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
			os.RemoveAll(wtDir)
			return applyPatchResultMsg{jobID: jobID,
				err: fmt.Errorf("git worktree add: %w: %s", cmdErr, out)}
		}

		result := m.checkApplyCommitPatch(jobID, jobDetail, wtDir, patch)

		// Keep the worktree if patch was applied but commit failed, so the user can recover.
		if result.commitFailed {
			result.worktreeDir = wtDir
		} else {
			removeWorktree()
		}

		return result
	}
}

// fetchPatchAndJob fetches the patch content and job details for a fix job.
// Returns nil msg on success; a non-nil msg should be returned to the TUI immediately.
func (m model) fetchPatchAndJob(jobID int64) (string, *storage.ReviewJob, *applyPatchResultMsg) {
	url := m.serverAddr + fmt.Sprintf("/api/job/patch?job_id=%d", jobID)
	resp, err := m.client.Get(url)
	if err != nil {
		return "", nil, &applyPatchResultMsg{jobID: jobID, err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, &applyPatchResultMsg{jobID: jobID, err: fmt.Errorf("no patch available")}
	}

	patchData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, &applyPatchResultMsg{jobID: jobID, err: err}
	}
	patch := string(patchData)
	if patch == "" {
		return "", nil, &applyPatchResultMsg{jobID: jobID, err: fmt.Errorf("empty patch")}
	}

	jobDetail, jErr := m.fetchJobByID(jobID)
	if jErr != nil {
		return "", nil, &applyPatchResultMsg{jobID: jobID, err: jErr}
	}

	return patch, jobDetail, nil
}

// checkApplyCommitPatch validates, applies, commits, and marks a patch as applied.
// Shared by both applyFixPatch (existing worktree) and applyFixPatchInWorktree (temp worktree).
func (m model) checkApplyCommitPatch(jobID int64, jobDetail *storage.ReviewJob, targetDir, patch string) applyPatchResultMsg {
	// Check for uncommitted changes in files the patch touches
	patchedFiles, pfErr := patchFiles(patch)
	if pfErr != nil {
		return applyPatchResultMsg{jobID: jobID, err: pfErr}
	}
	dirty, dirtyErr := dirtyPatchFiles(targetDir, patchedFiles)
	if dirtyErr != nil {
		return applyPatchResultMsg{jobID: jobID,
			err: fmt.Errorf("checking dirty files: %w", dirtyErr)}
	}
	if len(dirty) > 0 {
		return applyPatchResultMsg{jobID: jobID,
			err: fmt.Errorf("uncommitted changes in patch files: %s — stash or commit first", strings.Join(dirty, ", "))}
	}

	// Dry-run check — only trigger rebase on actual merge conflicts
	if err := worktree.CheckPatch(targetDir, patch); err != nil {
		var conflictErr *worktree.PatchConflictError
		if errors.As(err, &conflictErr) {
			return applyPatchResultMsg{jobID: jobID, rebase: true, err: err}
		}
		return applyPatchResultMsg{jobID: jobID, err: err}
	}

	// Apply the patch
	if err := worktree.ApplyPatch(targetDir, patch); err != nil {
		return applyPatchResultMsg{jobID: jobID, err: err}
	}

	var parentJobID int64
	if jobDetail.ParentJobID != nil {
		parentJobID = *jobDetail.ParentJobID
	}

	// Stage and commit
	commitMsg := fmt.Sprintf("fix: apply roborev fix job #%d", jobID)
	if parentJobID > 0 {
		ref := git.ShortSHA(jobDetail.GitRef)
		commitMsg = fmt.Sprintf("fix: apply roborev fix for %s (job #%d)", ref, jobID)
	}
	if err := commitPatch(targetDir, patch, commitMsg); err != nil {
		return applyPatchResultMsg{jobID: jobID, parentJobID: parentJobID, success: true,
			commitFailed: true, err: fmt.Errorf("patch applied but commit failed: %w", err)}
	}

	// Mark the fix job as applied on the server
	if err := m.postJSON("/api/job/applied", map[string]any{"job_id": jobID}, nil); err != nil {
		return applyPatchResultMsg{jobID: jobID, parentJobID: parentJobID, success: true,
			err: fmt.Errorf("patch applied and committed but failed to mark applied: %w", err)}
	}

	return applyPatchResultMsg{jobID: jobID, parentJobID: parentJobID, success: true}
}

// commitPatch stages only the files touched by patch and commits them.
func commitPatch(repoPath, patch, message string) error {
	files, err := patchFiles(patch)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no files found in patch")
	}
	args := append([]string{"-C", repoPath, "add", "--"}, files...)
	addCmd := exec.Command("git", args...)
	addCmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, out)
	}
	commitArgs := append(
		[]string{"-C", repoPath, "commit", "--only", "-m", message, "--"},
		files...,
	)
	commitCmd := exec.Command("git", commitArgs...)
	commitCmd.Env = append(os.Environ(), "GIT_LITERAL_PATHSPECS=1")
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, out)
	}
	return nil
}

// dirtyPatchFiles returns the subset of files that have uncommitted changes.
func dirtyPatchFiles(repoPath string, files []string) ([]string, error) {
	// git diff --name-only shows unstaged changes; --cached shows staged
	cmd := exec.Command("git", "-C", repoPath, "diff", "--name-only", "HEAD", "--")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	dirty := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			dirty[line] = true
		}
	}
	var overlap []string
	for _, f := range files {
		if dirty[f] {
			overlap = append(overlap, f)
		}
	}
	return overlap, nil
}

// patchFiles extracts the list of file paths touched by a unified diff.
func patchFiles(patch string) ([]string, error) {
	fileDiffs, err := godiff.ParseMultiFileDiff([]byte(patch))
	if err != nil {
		return nil, fmt.Errorf("parse patch: %w", err)
	}
	seen := map[string]bool{}
	addFile := func(name, prefix string) {
		name = strings.TrimPrefix(name, prefix)
		if name != "" && name != "/dev/null" {
			seen[name] = true
		}
	}
	for _, fd := range fileDiffs {
		addFile(fd.OrigName, "a/") // old path (stages deletion for renames)
		addFile(fd.NewName, "b/")  // new path (stages addition for renames)
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	return files, nil
}

// triggerRebase triggers a new fix job that re-applies a stale patch to the current HEAD.
// The server looks up the stale patch from the DB, avoiding large client-to-server transfers.
func (m model) triggerRebase(staleJobID int64) tea.Cmd {
	return func() tea.Msg {
		// Find the parent job ID (the original review this fix was for)
		staleJob, fetchErr := m.fetchJobByID(staleJobID)
		if fetchErr != nil {
			return fixTriggerResultMsg{err: fmt.Errorf("stale job %d not found: %w", staleJobID, fetchErr)}
		}

		// Use the original parent job ID if this was already a fix job
		parentJobID := staleJobID
		if staleJob.ParentJobID != nil {
			parentJobID = *staleJob.ParentJobID
		}

		// Let the server build the rebase prompt from the stale job's patch
		req := map[string]any{
			"parent_job_id": parentJobID,
			"stale_job_id":  staleJobID,
		}
		var newJob storage.ReviewJob
		if err := m.postJSON("/api/job/fix", req, &newJob); err != nil {
			return fixTriggerResultMsg{err: fmt.Errorf("trigger rebase: %w", err)}
		}
		// Mark the stale job as rebased now that the new job exists.
		// Skip if already rebased (e.g. retry via R on a rebased job).
		var warning string
		if staleJob.Status != storage.JobStatusRebased {
			if err := m.postJSON(
				"/api/job/rebased",
				map[string]any{"job_id": staleJobID},
				nil,
			); err != nil {
				warning = fmt.Sprintf(
					"rebase job #%d enqueued but failed to mark #%d as rebased: %v",
					newJob.ID, staleJobID, err,
				)
			}
		}
		return fixTriggerResultMsg{job: &newJob, warning: warning}
	}
}

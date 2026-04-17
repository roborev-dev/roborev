package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/roborev-dev/roborev/internal/agent"
	"github.com/roborev-dev/roborev/internal/config"
	gitpkg "github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/review/autotype"
	"github.com/roborev-dev/roborev/internal/storage"
)

// testClassifierVerdict overrides the classifier outcome for tests.
var testClassifierVerdict struct {
	mu     sync.Mutex
	set    bool
	yes    bool
	reason string
}

// SetTestClassifierVerdict overrides the classifier result for tests.
// Call with set=false (any args) to clear.
func SetTestClassifierVerdict(yes bool, reason string) {
	testClassifierVerdict.mu.Lock()
	defer testClassifierVerdict.mu.Unlock()
	testClassifierVerdict.set = reason != "" || yes
	testClassifierVerdict.yes = yes
	testClassifierVerdict.reason = reason
}

func getTestClassifierVerdict() (bool, string, bool) {
	testClassifierVerdict.mu.Lock()
	defer testClassifierVerdict.mu.Unlock()
	return testClassifierVerdict.yes, testClassifierVerdict.reason, testClassifierVerdict.set
}

// processClassifyJob runs the classifier for an ambiguous design-review
// decision and converts the same classify row in place via UPDATE.
func (wp *WorkerPool) processClassifyJob(ctx context.Context, workerID string, job *storage.ReviewJob) {
	cfg := wp.cfgGetter.Config()

	timeout := config.ResolveClassifierTimeout(job.RepoPath, cfg)
	classifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxBytes := config.ResolveClassifierMaxPromptSize(job.RepoPath, cfg)

	if yes, reason, set := getTestClassifierVerdict(); set {
		wp.applyClassifyVerdict(workerID, job, yes, reason)
		return
	}

	in := autotype.Input{
		RepoPath:     job.RepoPath,
		GitRef:       job.GitRef,
		Diff:         derefString(job.DiffContent),
		Message:      classifierCommitMessage(job.RepoPath, job.GitRef, job.CommitSubject),
		ChangedFiles: changedFilesForJob(job),
	}

	primary, err := config.ResolveClassifyAgent("", job.RepoPath, cfg)
	if err != nil {
		wp.completeClassifyAsSkip(workerID, job, "classifier config: "+err.Error())
		return
	}
	backup := config.ResolveBackupClassifyAgent(job.RepoPath, cfg)

	tryAgent := func(name, model string) (bool, string, error) {
		ag, err := agent.GetAvailable(name)
		if err != nil {
			return false, "", fmt.Errorf("classifier %q unavailable: %w", name, err)
		}
		sa, ok := ag.(agent.SchemaAgent)
		if !ok {
			return false, "", fmt.Errorf("classify_agent %q is not a SchemaAgent", name)
		}
		level := agent.ParseReasoningLevel(config.ResolveClassifyReasoning("", job.RepoPath, cfg))
		ag = sa.WithReasoning(level)
		if model != "" {
			ag = ag.WithModel(model)
		}
		sa, ok = ag.(agent.SchemaAgent)
		if !ok {
			return false, "", fmt.Errorf("classify_agent %q lost SchemaAgent capability after WithReasoning/WithModel", name)
		}
		return newClassifierAdapter(sa, maxBytes).Decide(classifyCtx, in)
	}

	primaryModel := config.ResolveClassifyModel("", job.RepoPath, cfg)
	yes, reason, err := tryAgent(primary, primaryModel)
	if err != nil && backup != "" && backup != primary {
		backupModel := config.ResolveBackupClassifyModel(job.RepoPath, cfg)
		log.Printf("[%s] classifier primary %q (model=%q) failed (%v); trying backup %q (model=%q)",
			workerID, primary, primaryModel, err, backup, backupModel)
		yes, reason, err = tryAgent(backup, backupModel)
	}
	if err != nil {
		wp.completeClassifyAsSkip(workerID, job, "classifier error: "+err.Error())
		return
	}
	wp.applyClassifyVerdict(workerID, job, yes, reason)
}

// applyClassifyVerdict converts the classify row in place — a separate
// INSERT for the follow-up design/skipped row would conflict with the
// auto-design partial unique index.
func (wp *WorkerPool) applyClassifyVerdict(workerID string, job *storage.ReviewJob, yes bool, reason string) {
	autoDesignMetrics.RecordClassifier(yes, false)
	if yes {
		if err := wp.db.PromoteClassifyToDesignReview(job.ID, workerID); err != nil {
			log.Printf("[%s] PromoteClassifyToDesignReview for %d: %v", workerID, job.ID, err)
		}
		return
	}
	if err := wp.db.MarkClassifyAsSkippedDesign(job.ID, workerID, reason); err != nil {
		log.Printf("[%s] MarkClassifyAsSkippedDesign for %d: %v", workerID, job.ID, err)
	}
}

// completeClassifyAsSkip is the failure path: the classifier couldn't
// produce a verdict, so we degrade to skip with a reason. Marks the
// classify row 'skipped' (not 'failed') so CI batch accounting stays
// accurate.
func (wp *WorkerPool) completeClassifyAsSkip(workerID string, job *storage.ReviewJob, reason string) {
	autoDesignMetrics.RecordClassifier(false, true)
	if err := wp.db.MarkClassifyAsSkippedDesign(job.ID, workerID, reason); err != nil {
		log.Printf("[%s] MarkClassifyAsSkippedDesign for failed classify %d: %v", workerID, job.ID, err)
	}
}

// classifierCommitMessage returns "subject\n\nbody" for the classifier prompt.
// Falls back to the provided subject if git is unreachable.
func classifierCommitMessage(repoPath, ref, fallbackSubject string) string {
	info, err := gitpkg.GetCommitInfo(repoPath, ref)
	if err != nil || info == nil {
		return fallbackSubject
	}
	if info.Body == "" {
		return info.Subject
	}
	return info.Subject + "\n\n" + info.Body
}

// changedFilesForJob returns the changed file paths for the job's commit.
// Returns nil for non-single-commit jobs (range/dirty/etc.) since the heuristic
// path-based rules don't apply uniformly.
func changedFilesForJob(job *storage.ReviewJob) []string {
	if job.CommitID == nil || job.GitRef == "" || job.GitRef == "dirty" {
		return nil
	}
	files, err := gitpkg.GetFilesChanged(job.RepoPath, job.GitRef)
	if err != nil {
		return nil
	}
	return files
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// insertSkippedDesignRow writes a row representing a design review we decided
// NOT to run. Dedup is enforced atomically by the auto-design unique index.
func (wp *WorkerPool) insertSkippedDesignRow(parent *storage.ReviewJob, reason string) error {
	return wp.db.InsertSkippedDesignJob(storage.InsertSkippedDesignJobParams{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		SkipReason: reason,
	})
}

// enqueueAutoDesignReview enqueues a design review if one does not already
// exist for this commit.
func (wp *WorkerPool) enqueueAutoDesignReview(parent *storage.ReviewJob) error {
	_, err := wp.db.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		JobType:    storage.JobTypeReview,
		ReviewType: "design",
	})
	return err
}

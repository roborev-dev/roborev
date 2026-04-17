package daemon

import (
	"context"
	"errors"
	"log"
	"sync/atomic"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/review/autotype"
	"github.com/roborev-dev/roborev/internal/storage"
)

// AutoDesignMetrics carries per-outcome counters for the auto-design router.
// Counters use atomics so producers (enqueue handler, CI poller, worker)
// don't contend on a mutex. Process-global because every producer needs to
// reach it without plumbing a reference through every code path.
type AutoDesignMetrics struct {
	triggeredHeuristic  atomic.Int64
	skippedHeuristic    atomic.Int64
	triggeredClassifier atomic.Int64
	skippedClassifier   atomic.Int64
	classifierFailed    atomic.Int64
}

// autoDesignMetrics is the process-global instance.
var autoDesignMetrics = &AutoDesignMetrics{}

// RecordHeuristic bumps either TriggeredHeuristic or SkippedHeuristic.
func (m *AutoDesignMetrics) RecordHeuristic(run bool) {
	if run {
		m.triggeredHeuristic.Add(1)
		return
	}
	m.skippedHeuristic.Add(1)
}

// RecordClassifier bumps a classifier counter. failed=true wins over run.
func (m *AutoDesignMetrics) RecordClassifier(run, failed bool) {
	if failed {
		m.classifierFailed.Add(1)
		return
	}
	if run {
		m.triggeredClassifier.Add(1)
		return
	}
	m.skippedClassifier.Add(1)
}

// Snapshot returns a copy of the current counter values.
func (m *AutoDesignMetrics) Snapshot() storage.AutoDesignStatus {
	return storage.AutoDesignStatus{
		TriggeredHeuristic:  m.triggeredHeuristic.Load(),
		SkippedHeuristic:    m.skippedHeuristic.Load(),
		TriggeredClassifier: m.triggeredClassifier.Load(),
		SkippedClassifier:   m.skippedClassifier.Load(),
		ClassifierFailed:    m.classifierFailed.Load(),
	}
}

// AutoDesignMetricsSnapshot returns the package-global metrics snapshot.
func AutoDesignMetricsSnapshot() storage.AutoDesignStatus {
	return autoDesignMetrics.Snapshot()
}

// autoDesignStatusForResponse returns a populated AutoDesignStatus when
// auto-design is effectively enabled (globally, or by at least one
// registered repo). Returns nil otherwise so the JSON omits the field.
func (s *Server) autoDesignStatusForResponse() *storage.AutoDesignStatus {
	cfg, _ := config.LoadGlobal()
	enabled := cfg != nil && cfg.AutoDesignReview.Enabled

	if !enabled {
		repos, err := s.db.ListRepos()
		if err == nil {
			for _, r := range repos {
				if config.ResolveAutoDesignEnabled(r.RootPath, cfg) {
					enabled = true
					break
				}
			}
		}
	}

	if !enabled {
		return nil
	}
	snap := autoDesignMetrics.Snapshot()
	snap.Enabled = true
	return &snap
}

// ResetAutoDesignMetricsForTest zeroes the global counters. Tests only.
func ResetAutoDesignMetricsForTest() {
	autoDesignMetrics.triggeredHeuristic.Store(0)
	autoDesignMetrics.skippedHeuristic.Store(0)
	autoDesignMetrics.triggeredClassifier.Store(0)
	autoDesignMetrics.skippedClassifier.Store(0)
	autoDesignMetrics.classifierFailed.Store(0)
}

// maybeDispatchAutoDesign decides whether to enqueue a follow-up design
// review (or a classify job) for the just-enqueued primary review job.
// Opportunistic: errors are logged but never bubble up to the caller's HTTP
// response.
func (s *Server) maybeDispatchAutoDesign(ctx context.Context, parent *storage.ReviewJob) error {
	cfg, _ := config.LoadGlobal()
	if !config.ResolveAutoDesignEnabled(parent.RepoPath, cfg) {
		return nil
	}

	if parent.CommitID != nil {
		c, err := s.db.GetCommitByID(*parent.CommitID)
		if err == nil && c != nil {
			if has, err := s.db.HasAutoDesignSlotForCommit(parent.RepoID, c.SHA); err == nil && has {
				return nil
			}
		}
	}

	h := config.ResolveAutoDesignHeuristics(parent.RepoPath, cfg)
	hh := autotype.Heuristics{
		MinDiffLines:           h.MinDiffLines,
		LargeDiffLines:         h.LargeDiffLines,
		LargeFileCount:         h.LargeFileCount,
		TriggerPaths:           h.TriggerPaths,
		SkipPaths:              h.SkipPaths,
		TriggerMessagePatterns: h.TriggerMessagePatterns,
		SkipMessagePatterns:    h.SkipMessagePatterns,
	}

	diff := derefString(parent.DiffContent)
	if diff == "" && parent.GitRef != "" && parent.JobType == storage.JobTypeReview {
		var err error
		diff, err = git.GetDiff(parent.RepoPath, parent.GitRef)
		if err != nil {
			log.Printf("auto-design: git.GetDiff(%s) failed, deferring to classifier: %v", parent.GitRef, err)
			diff = ""
		}
	}

	in := autotype.Input{
		RepoPath:     parent.RepoPath,
		GitRef:       parent.GitRef,
		Diff:         diff,
		Message:      classifierCommitMessage(parent.RepoPath, parent.GitRef, parent.CommitSubject),
		ChangedFiles: changedFilesForJob(parent),
	}

	d, err := autotype.Classify(ctx, in, hh, autotype.ErrOnClassifier{})
	switch {
	case err == nil && d.Method == autotype.MethodHeuristic:
		autoDesignMetrics.RecordHeuristic(d.Run)
		if d.Run {
			return s.enqueueDesignFollowUp(parent)
		}
		return s.insertSkippedDesign(parent, d.Reason)
	case errors.Is(err, autotype.ErrNeedsClassifier):
		return s.enqueueClassifyJob(parent)
	default:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("auto-design: context ended during heuristics (%v); skipping opportunistic work", err)
			return nil
		}
		log.Printf("auto-design: Classify error, skipping: %v", err)
		return s.insertSkippedDesign(parent, "auto-design: heuristic error")
	}
}

func (s *Server) enqueueClassifyJob(parent *storage.ReviewJob) error {
	_, err := s.db.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		JobType:    storage.JobTypeClassify,
		ReviewType: "design",
	})
	return err
}

func (s *Server) enqueueDesignFollowUp(parent *storage.ReviewJob) error {
	_, err := s.db.EnqueueAutoDesignJob(storage.EnqueueOpts{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		JobType:    storage.JobTypeReview,
		ReviewType: "design",
	})
	return err
}

func (s *Server) insertSkippedDesign(parent *storage.ReviewJob, reason string) error {
	return s.db.InsertSkippedDesignJob(storage.InsertSkippedDesignJobParams{
		RepoID:     parent.RepoID,
		CommitID:   parent.CommitIDValue(),
		GitRef:     parent.GitRef,
		Branch:     parent.Branch,
		SkipReason: reason,
	})
}

// Package autotype decides whether a commit warrants a design review, using
// cheap heuristics first and an optional classifier for ambiguous cases.
package autotype

import (
	"context"
	"errors"
	"fmt"
)

// ErrNeedsClassifier is returned by Classify when heuristics are inconclusive
// and the provided Classifier is ErrOnClassifier (used by callers that want
// to dispatch the classifier asynchronously rather than run it inline).
var ErrNeedsClassifier = errors.New("autotype: classifier required")

// ErrOnClassifier is a Classifier implementation that always returns
// ErrNeedsClassifier. Use from code paths that want to see "heuristic-only"
// decisions without blocking on a live agent.
type ErrOnClassifier struct{}

// Decide implements Classifier.
func (ErrOnClassifier) Decide(ctx context.Context, in Input) (bool, string, error) {
	return false, "", ErrNeedsClassifier
}

// Method identifies which layer produced a Decision.
type Method string

const (
	MethodHeuristic  Method = "heuristic"
	MethodClassifier Method = "classifier"
)

// Decision is the final verdict for a single commit.
type Decision struct {
	Run    bool
	Reason string
	Method Method
}

// Input is what the heuristics and classifier operate on.
type Input struct {
	RepoPath     string
	GitRef       string
	Diff         string
	Message      string
	ChangedFiles []string
}

// Classifier returns a yes/no + reason for ambiguous cases. Implementations
// typically wrap a SchemaAgent call.
type Classifier interface {
	Decide(ctx context.Context, in Input) (yes bool, reason string, err error)
}

// Classify runs the heuristics in fixed order (trigger rules first, then
// skip rules, then classifier fallback) and returns a Decision.
//
// Precedence:
//  1. TRIGGER — any trigger_paths hit, diff size large, file count large,
//     or trigger_message_patterns match → Run=true.
//  2. SKIP — diff below MinDiffLines, all files match skip_paths, or
//     skip_message_patterns match → Run=false.
//  3. CLASSIFIER — ambiguous → delegate to cls.Decide.
func Classify(ctx context.Context, in Input, h Heuristics, cls Classifier) (Decision, error) {
	for _, f := range in.ChangedFiles {
		ok, err := AnyMatch(h.TriggerPaths, f)
		if err != nil {
			return Decision{}, err
		}
		if ok {
			return Decision{
				Run:    true,
				Method: MethodHeuristic,
				Reason: fmt.Sprintf("touches %s (design-relevant)", f),
			}, nil
		}
	}

	lines := CountChangedLines(in.Diff)
	if h.LargeDiffLines > 0 && lines >= h.LargeDiffLines {
		return Decision{
			Run:    true,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("large change (%d lines)", lines),
		}, nil
	}
	if h.LargeFileCount > 0 && len(in.ChangedFiles) >= h.LargeFileCount {
		return Decision{
			Run:    true,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("wide-reaching change (%d files)", len(in.ChangedFiles)),
		}, nil
	}

	if m, err := MatchMessage(h.TriggerMessagePatterns, in.Message); err != nil {
		return Decision{}, err
	} else if m != "" {
		return Decision{
			Run:    true,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("commit message mentions %q", m),
		}, nil
	}

	if h.MinDiffLines > 0 && lines < h.MinDiffLines {
		return Decision{
			Run:    false,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("trivial diff (%d lines)", lines),
		}, nil
	}

	allSkipped, err := AllMatch(h.SkipPaths, in.ChangedFiles)
	if err != nil {
		return Decision{}, err
	}
	if allSkipped {
		return Decision{
			Run:    false,
			Method: MethodHeuristic,
			Reason: "doc/test-only change",
		}, nil
	}

	if m, err := MatchMessage(h.SkipMessagePatterns, in.Message); err != nil {
		return Decision{}, err
	} else if m != "" {
		return Decision{
			Run:    false,
			Method: MethodHeuristic,
			Reason: fmt.Sprintf("conventional marker: %q", m),
		}, nil
	}

	if cls == nil {
		return Decision{}, fmt.Errorf("classifier required for ambiguous input but none provided")
	}
	yes, reason, err := cls.Decide(ctx, in)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Run:    yes,
		Method: MethodClassifier,
		Reason: reason,
	}, nil
}

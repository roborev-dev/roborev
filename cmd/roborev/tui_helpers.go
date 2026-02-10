package main

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/roborev-dev/roborev/internal/storage"
)

// Filter type constants used in filterStack and popFilter/pushFilter.
const (
	filterTypeRepo   = "repo"
	filterTypeBranch = "branch"
)

// branchNone is the sentinel value for jobs with no branch information.
const branchNone = "(none)"

// mutateJob finds a job by ID and applies the mutation function.
// Returns true if the job was found and mutated.
func (m *tuiModel) mutateJob(id int64, fn func(*storage.ReviewJob)) bool {
	for i := range m.jobs {
		if m.jobs[i].ID == id {
			fn(&m.jobs[i])
			return true
		}
	}
	return false
}

// setJobAddressed updates the addressed state for a job by ID.
// Handles nil pointer by allocating if necessary.
func (m *tuiModel) setJobAddressed(jobID int64, state bool) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		if job.Addressed == nil {
			job.Addressed = new(bool)
		}
		*job.Addressed = state
	})
}

// setJobStatus updates the status for a job by ID.
func (m *tuiModel) setJobStatus(jobID int64, status storage.JobStatus) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.Status = status
	})
}

// setJobFinishedAt updates the FinishedAt for a job by ID.
func (m *tuiModel) setJobFinishedAt(jobID int64, finishedAt *time.Time) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.FinishedAt = finishedAt
	})
}

// setJobStartedAt updates the StartedAt for a job by ID.
func (m *tuiModel) setJobStartedAt(jobID int64, startedAt *time.Time) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.StartedAt = startedAt
	})
}

// setJobError updates the Error for a job by ID.
func (m *tuiModel) setJobError(jobID int64, errMsg string) {
	m.mutateJob(jobID, func(job *storage.ReviewJob) {
		job.Error = errMsg
	})
}

// wrapText wraps text to the specified width, preserving existing line breaks
// and breaking at word boundaries when possible. Uses runewidth for correct
// display width calculation with Unicode and wide characters.
func wrapText(text string, width int) []string {
	if width <= 0 {
		width = 100
	}

	var result []string
	for _, line := range strings.Split(text, "\n") {
		lineWidth := runewidth.StringWidth(line)
		if lineWidth <= width {
			result = append(result, line)
			continue
		}

		// Wrap long lines using display width
		for runewidth.StringWidth(line) > width {
			runes := []rune(line)
			breakPoint := 0
			currentWidth := 0

			// Walk runes up to the width limit
			for i, r := range runes {
				rw := runewidth.RuneWidth(r)
				if currentWidth+rw > width {
					break
				}
				currentWidth += rw
				breakPoint = i + 1
			}

			// Ensure forward progress: if the first rune is wider than width,
			// take at least one rune to avoid an infinite loop.
			if breakPoint == 0 {
				breakPoint = 1
			}

			// Try to find a space to break at (look back from breakPoint)
			bestBreak := breakPoint
			scanWidth := 0
			for i := breakPoint - 1; i >= 0; i-- {
				if runes[i] == ' ' {
					bestBreak = i
					break
				}
				scanWidth += runewidth.RuneWidth(runes[i])
				if scanWidth > width/2 {
					break // Don't look back too far
				}
			}

			result = append(result, string(runes[:bestBreak]))
			line = strings.TrimLeft(string(runes[bestBreak:]), " ")
		}
		if len(line) > 0 {
			result = append(result, line)
		}
	}

	return result
}

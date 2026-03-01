package tui

import (
	"fmt"
	"strings"

	"github.com/roborev-dev/roborev/internal/git"
	"github.com/roborev-dev/roborev/internal/storage"
)

func shortRef(ref string) string {
	if strings.Contains(ref, "..") {
		if len(ref) > 17 {
			return ref[:17]
		}
		return ref
	}
	return git.ShortSHA(ref)
}

func shortJobRef(job storage.ReviewJob) string {
	if job.CommitID == nil && job.DiffContent == nil {
		if job.GitRef == "prompt" {
			return "run"
		}
		return job.GitRef
	}
	return shortRef(job.GitRef)
}

func formatAgentLabel(agent string, model string) string {
	if model != "" {
		return fmt.Sprintf("%s: %s", agent, model)
	}
	return agent
}

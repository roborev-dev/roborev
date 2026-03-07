//go:build integration

package main

import (
	"net/http"
	"testing"
	"time"
)

func TestPostCommitTimesOutOnSlowDaemon(t *testing.T) {
	repo := newTestGitRepo(t)
	repo.CommitFile("file.txt", "content", "initial")

	handlerHit := make(chan struct{}, 1)

	// Handler stalls for 5s — much longer than the client
	// timeout, so the command can only return promptly if
	// the timeout fires. Bounded sleep keeps httptest
	// cleanup from hanging indefinitely.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/enqueue", func(
		w http.ResponseWriter, r *http.Request,
	) {
		select {
		case handlerHit <- struct{}{}:
		default:
		}
		time.Sleep(5 * time.Second)
	})
	daemonFromHandler(t, mux)

	orig := hookHTTPClient
	hookHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	t.Cleanup(func() { hookHTTPClient = orig })

	start := time.Now()
	_, _, err := executePostCommitCmd("--repo", repo.Dir)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("expected nil (fail open), got: %v", err)
	}

	select {
	case <-handlerHit:
		// Handler was reached — timeout path was exercised
	default:
		t.Fatal("handler was never reached; timeout not exercised")
	}

	if elapsed > 2*time.Second {
		t.Errorf(
			"command took %v; should return promptly via timeout",
			elapsed,
		)
	}
}

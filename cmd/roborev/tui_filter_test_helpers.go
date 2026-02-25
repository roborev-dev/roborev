package main

import (
	"github.com/roborev-dev/roborev/internal/storage"
	"testing"
)

type testModelOption func(*tuiModel)

func withCurrentView(v tuiView) testModelOption {
	return func(m *tuiModel) { m.currentView = v }
}

func withTestJobs(jobs ...storage.ReviewJob) testModelOption {
	return func(m *tuiModel) { m.jobs = jobs }
}

func withSelection(idx int, jobID int64) testModelOption {
	return func(m *tuiModel) {
		m.selectedIdx = idx
		m.selectedJobID = jobID
	}
}

func withActiveRepoFilter(f []string) testModelOption {
	return func(m *tuiModel) { m.activeRepoFilter = f }
}

func withActiveBranchFilter(f string) testModelOption {
	return func(m *tuiModel) { m.activeBranchFilter = f }
}

func withFilterStack(f ...string) testModelOption {
	return func(m *tuiModel) { m.filterStack = f }
}

func withFilterSelectedIdx(idx int) testModelOption {
	return func(m *tuiModel) { m.filterSelectedIdx = idx }
}

func withFilterTree(nodes []treeFilterNode) testModelOption {
	return func(m *tuiModel) {
		m.filterTree = nodes
		m.rebuildFilterFlatList()
	}
}

func initTestModel(opts ...testModelOption) tuiModel {
	m := newTuiModel("http://localhost", WithExternalIODisabled())
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func assertSearch(t *testing.T, m tuiModel, expected string) {
	t.Helper()
	if m.filterSearch != expected {
		t.Errorf("Expected search=%q, got %q", expected, m.filterSearch)
	}
}

func countLoading(m *tuiModel) int {
	n := 0
	for _, node := range m.filterTree {
		if node.loading {
			n++
		}
	}
	return n
}

func countLoaded(m *tuiModel) int {
	n := 0
	for _, node := range m.filterTree {
		if node.children != nil {
			n++
		}
	}
	return n
}

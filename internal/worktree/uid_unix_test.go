//go:build !windows

package worktree

import "os"

func isRoot() bool { return os.Getuid() == 0 }

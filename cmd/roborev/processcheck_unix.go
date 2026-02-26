//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// isPIDAliveForUpdateDefault returns true when pid exists.
// Uses signal 0 so no signal is delivered.
func isPIDAliveForUpdateDefault(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	return true
}

//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// isPIDAliveDefault returns true when pid exists.
// Uses signal 0 so no signal is delivered.
func isPIDAliveDefault(pid int) bool {
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
	// PID exists. If it's clearly not a roborev daemon anymore, treat as exited
	// (covers PID reuse after daemon shutdown).
	switch identifyPID(pid) {
	case updatePIDNotRoborev:
		return false
	case updatePIDRoborev:
		return true
	default:
		// Unknown identity -> conservative (assume still alive).
		return true
	}
}

func identifyPID(pid int) updatePIDIdentity {
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err == nil {
		cmdStr := normalizeCommandLine(string(cmdline))
		if cmdStr == "" {
			return updatePIDUnknown
		}
		if isRoborevDaemonCommand(cmdStr) {
			return updatePIDRoborev
		}
		return updatePIDNotRoborev
	}

	// Use -ww to request untruncated command output on BSD/macOS ps.
	cmd := exec.Command("ps", "-ww", "-p", strconv.Itoa(pid), "-o", "command=")
	output, err := cmd.Output()
	if err != nil {
		return updatePIDUnknown
	}
	cmdStr := normalizeCommandLine(string(output))
	if cmdStr == "" {
		return updatePIDUnknown
	}
	if isRoborevDaemonCommand(cmdStr) {
		return updatePIDRoborev
	}
	return updatePIDNotRoborev
}

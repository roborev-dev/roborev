package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
)

func acpTestEchoCommand(args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		cmdArgs := append([]string{"/c", "echo"}, args...)
		return exec.Command("cmd", cmdArgs...)
	}

	return exec.Command("echo", args...)
}

func acpTestSleepCommand(seconds int) (*exec.Cmd, error) {
	if seconds < 1 {
		seconds = 1
	}

	if runtime.GOOS == "windows" {
		powerShellCommand := fmt.Sprintf("Start-Sleep -Seconds %d", seconds)
		args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powerShellCommand}

		for _, bin := range []string{"pwsh", "powershell"} {
			if _, err := exec.LookPath(bin); err == nil {
				return exec.Command(bin, args...), nil
			}
		}
		return nil, fmt.Errorf("neither pwsh nor powershell found in PATH")
	}

	if _, err := exec.LookPath("sleep"); err != nil {
		return nil, fmt.Errorf("sleep command not found in PATH")
	}
	return exec.Command("sleep", strconv.Itoa(seconds)), nil
}

package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
)

func acpTestEchoCommand(args ...string) (string, []string) {
	if runtime.GOOS == "windows" {
		cmdArgs := append([]string{"/c", "echo"}, args...)
		return "cmd", cmdArgs
	}

	return "echo", args
}

func acpTestSleepCommand(seconds int) (string, []string, bool) {
	if seconds < 1 {
		seconds = 1
	}

	if runtime.GOOS == "windows" {
		powerShellCommand := fmt.Sprintf("Start-Sleep -Seconds %d", seconds)

		if _, err := exec.LookPath("pwsh"); err == nil {
			return "pwsh", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powerShellCommand}, true
		}
		if _, err := exec.LookPath("powershell"); err == nil {
			return "powershell", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powerShellCommand}, true
		}
		return "", nil, false
	}

	if _, err := exec.LookPath("sleep"); err != nil {
		return "", nil, false
	}
	return "sleep", []string{strconv.Itoa(seconds)}, true
}

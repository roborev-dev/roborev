package main

import "strings"

type updatePIDIdentity int

const (
	updatePIDUnknown updatePIDIdentity = iota
	updatePIDRoborev
	updatePIDNotRoborev
)

func normalizeCommandLineForUpdate(s string) string {
	// Strip NUL bytes first (common with UTF-16LE output and /proc cmdline).
	s = strings.ReplaceAll(s, "\x00", " ")
	// Strip common BOMs.
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	s = strings.TrimPrefix(s, "\xff\xfe")
	s = strings.TrimPrefix(s, "\xfe\xff")
	return strings.TrimSpace(s)
}

func isRoborevDaemonCommandForUpdate(cmdStr string) bool {
	cmdStr = normalizeCommandLineForUpdate(cmdStr)
	cmdLower := strings.ToLower(cmdStr)
	if !strings.Contains(cmdLower, "roborev") {
		return false
	}
	fields := strings.Fields(cmdLower)
	foundDaemon := false
	for _, field := range fields {
		if !foundDaemon {
			if field == "daemon" ||
				strings.HasSuffix(field, "/daemon") ||
				strings.HasSuffix(field, "\\daemon") {
				foundDaemon = true
			}
			continue
		}
		if strings.HasPrefix(field, "-") {
			continue
		}
		if looksLikeFlagValueForUpdate(field) {
			continue
		}
		return field == "run"
	}
	return false
}

func looksLikeFlagValueForUpdate(token string) bool {
	if strings.ContainsAny(token, "/\\") {
		return true
	}
	if strings.Contains(token, ":") {
		return true
	}
	if strings.Contains(token, "=") {
		return true
	}
	if len(token) > 0 && token[0] >= '0' && token[0] <= '9' {
		return true
	}
	if strings.Contains(token, ".") {
		return true
	}
	return false
}

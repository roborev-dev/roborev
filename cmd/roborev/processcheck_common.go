package main

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

type updatePIDIdentity int

const (
	updatePIDUnknown updatePIDIdentity = iota
	updatePIDRoborev
	updatePIDNotRoborev
)

var nonRunDaemonSubcommandsForUpdate = map[string]struct{}{
	"status":  {},
	"start":   {},
	"stop":    {},
	"restart": {},
	"logs":    {},
}

func normalizeCommandLineForUpdate(s string) string {
	// Strip NUL bytes first (common with UTF-16LE output and /proc cmdline).
	s = strings.ReplaceAll(s, "\x00", " ")
	// Strip common BOMs.
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	s = strings.TrimPrefix(s, "\xff\xfe")
	s = strings.TrimPrefix(s, "\xfe\xff")
	return strings.TrimSpace(s)
}

func normalizeCommandLineBytesForUpdate(raw []byte) string {
	return normalizeCommandLineForUpdate(decodeCommandLineBytesForUpdate(raw))
}

// parseWmicOutputForUpdate normalizes WMIC output and strips the
// "CommandLine" header when present.
func parseWmicOutputForUpdate(raw []byte) string {
	result := normalizeCommandLineBytesForUpdate(raw)
	lower := strings.ToLower(result)
	if strings.HasPrefix(lower, "commandline") {
		result = strings.TrimSpace(result[11:]) // len("commandline") == 11
	}
	return result
}

func decodeCommandLineBytesForUpdate(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if decoded, ok := decodeUTF16WithBOMForUpdate(raw); ok {
		return decoded
	}
	if decoded, ok := decodeLikelyUTF16ForUpdate(raw); ok {
		return decoded
	}
	return string(raw)
}

func decodeUTF16WithBOMForUpdate(raw []byte) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	switch {
	case raw[0] == 0xff && raw[1] == 0xfe:
		return decodeUTF16WithoutBOMForUpdate(raw[2:], binary.LittleEndian), true
	case raw[0] == 0xfe && raw[1] == 0xff:
		return decodeUTF16WithoutBOMForUpdate(raw[2:], binary.BigEndian), true
	default:
		return "", false
	}
}

func decodeLikelyUTF16ForUpdate(raw []byte) (string, bool) {
	sample := raw
	if len(sample) > 256 {
		sample = sample[:256]
	}
	var evenNulls, oddNulls int
	for i, b := range sample {
		if b != 0 {
			continue
		}
		if i%2 == 0 {
			evenNulls++
		} else {
			oddNulls++
		}
	}
	minNulls := len(sample) / 6 // ~16% NULs is strong UTF-16 signal.
	minNulls = max(minNulls, 4)
	switch {
	case oddNulls >= minNulls && evenNulls <= oddNulls/4:
		return decodeUTF16WithoutBOMForUpdate(raw, binary.LittleEndian), true
	case evenNulls >= minNulls && oddNulls <= evenNulls/4:
		return decodeUTF16WithoutBOMForUpdate(raw, binary.BigEndian), true
	default:
		return "", false
	}
}

func decodeUTF16WithoutBOMForUpdate(raw []byte, order binary.ByteOrder) string {
	if len(raw)%2 == 1 {
		raw = raw[:len(raw)-1]
	}
	if len(raw) == 0 {
		return ""
	}
	u16 := make([]uint16, len(raw)/2)
	for i := range u16 {
		u16[i] = order.Uint16(raw[i*2 : i*2+2])
	}
	return string(utf16.Decode(u16))
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
		field = trimCommandTokenQuotesForUpdate(field)
		if field == "" {
			continue
		}
		if !foundDaemon {
			if field == "daemon" ||
				strings.HasSuffix(field, "/daemon") ||
				strings.HasSuffix(field, "\\daemon") {
				foundDaemon = true
			}
			continue
		}
		if field == "run" {
			return true
		}
		if _, isNonRunSubcommand := nonRunDaemonSubcommandsForUpdate[field]; isNonRunSubcommand {
			return false
		}
	}
	return false
}

func trimCommandTokenQuotesForUpdate(token string) string {
	return strings.Trim(token, `"'`)
}

package agent

import (
	"encoding/json"
	"strings"
)

// ExtractSessionID returns the agent session/thread identifier carried by a
// streamed JSONL event. For Codex, thread_id is treated as the session ID.
func ExtractSessionID(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "{") {
		return ""
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		return ""
	}

	if sessionID := jsonFieldString(fields, "session_id"); sessionID != "" {
		return sessionID
	}
	if sessionID := jsonFieldString(fields, "sessionId"); sessionID != "" {
		return sessionID
	}

	if jsonFieldString(fields, "type") == "thread.started" {
		if threadID := jsonFieldString(fields, "thread_id"); threadID != "" {
			return threadID
		}
		if threadID := jsonFieldString(fields, "threadId"); threadID != "" {
			return threadID
		}
	}

	return ""
}

func jsonFieldString(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

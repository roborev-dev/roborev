import sys
import re

with open("cmd/roborev/tui_action_test.go", "r") as f:
    content = f.read()

# 1. Unify JSON Decoding + Extract HTTP Mock Endpoint Helper
helper1 = """
// expectJSONPost is a helper to mock expected POST requests and respond with JSON.
func expectJSONPost[Req any, Res any](t *testing.T, path string, expected Req, response Res) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if path != "" && r.URL.Path != path {
			t.Errorf("Expected path %s, got %s", path, r.URL.Path)
		}

		var req Req
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}
		if diff := cmp.Diff(expected, req); diff != "" {
			t.Fatalf("Request payload mismatch (-want +got):\n%s", diff)
		}
		json.NewEncoder(w).Encode(response)
	}
}
"""

if "expectJSONPost" not in content:
    content = content.replace('import (', 'import (\n\t"github.com/google/go-cmp/cmp"\n', 1)
    content += "\n" + helper1

s1 = """	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["job_id"].(float64) != 100 {
			t.Errorf("Expected job_id 100, got %v", req["job_id"])
		}
		if req["addressed"].(bool) != true {
			t.Errorf("Expected addressed true, got %v", req["addressed"])
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	})"""
r1 = """	_, m := mockServerModel(t, expectJSONPost(t, "", addressRequest{JobID: 100, Addressed: true}, map[string]bool{"success": true}))"""
content = content.replace(s1, r1)

s2 = """	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/review/address" {
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["job_id"].(float64) != 1 {
				t.Errorf("Expected job_id 1, got %v", req["job_id"])
			}
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
		} else {
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})"""
r2 = """	_, m := mockServerModel(t, expectJSONPost(t, "/api/review/address", addressRequest{JobID: 1, Addressed: true}, map[string]bool{"success": true}))"""
content = content.replace(s2, r2)

s3 = """	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/review/address" || r.Method != http.MethodPost {
			t.Fatalf("Unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req addressRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}
		if req.JobID != 42 {
			t.Errorf("Expected job_id=42, got %d", req.JobID)
		}
		if req.Addressed != true {
			t.Errorf("Expected addressed=true, got %v", req.Addressed)
		}
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	})"""
r3 = """	_, m := mockServerModel(t, expectJSONPost(t, "/api/review/address", addressRequest{JobID: 42, Addressed: true}, map[string]bool{"success": true}))"""
content = content.replace(s3, r3)

s4 = """	_, m := mockServerModel(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/job/cancel" {
			t.Errorf("Expected /api/job/cancel, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		var req struct {
			JobID int64 `json:"job_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.JobID != 42 {
			t.Errorf("Expected job_id=42, got %d", req.JobID)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})"""
r4 = """	type cancelRequest struct {
		JobID int64 `json:"job_id"`
	}
	_, m := mockServerModel(t, expectJSONPost(t, "/api/job/cancel", cancelRequest{JobID: 42}, map[string]any{"success": true}))"""
content = content.replace(s4, r4)

# 2. Implement Generic Message Asserter
helper2 = """
// assertMsgType is a helper to assert the type of a tea.Msg and return it.
func assertMsgType[T any](t *testing.T, msg tea.Msg) T {
	t.Helper()
	result, ok := msg.(T)
	if !ok {
		t.Fatalf("Expected %T, got %T: %v", new(T), msg, msg)
	}
	return result
}
"""
if "assertMsgType" not in content:
    content += "\n" + helper2

def replace_type_assertion(match):
    var_name = match.group(1)
    msg_type = match.group(2)
    return f"{var_name} := assertMsgType[{msg_type}](t, msg)"

content = re.sub(
    r'(\w+),\s*ok\s*:=\s*msg\.\(([a-zA-Z0-9_]+)\)\s*\n\s*if\s*!ok\s*\{\n\s*t\.Fatalf\("Expected .*?, got %T: %v",\s*msg,\s*msg\)\n\s*\}',
    replace_type_assertion,
    content
)

content = re.sub(
    r'result,\s*ok\s*:=\s*msg\.\(tuiAddressedResultMsg\)\s*\n\s*if\s*!ok\s*\{\n\s*t\.Fatalf\("Expected tuiAddressedResultMsg for 404, got %T: %v",\s*msg,\s*msg\)\n\s*\}',
    r'result := assertMsgType[tuiAddressedResultMsg](t, msg)',
    content
)

# 3. Add State Assertion Helpers
helper3 = """
// assertJobStats is a helper to assert the jobStats of a model.
func assertJobStats(t *testing.T, m tuiModel, addressed, unaddressed int) {
	t.Helper()
	if m.jobStats.Addressed != addressed {
		t.Errorf("expected Addressed=%d, got %d", addressed, m.jobStats.Addressed)
	}
	if m.jobStats.Unaddressed != unaddressed {
		t.Errorf("expected Unaddressed=%d, got %d", unaddressed, m.jobStats.Unaddressed)
	}
}
"""
if "assertJobStats" not in content:
    content += "\n" + helper3

# Replace jobStats assertions
content = content.replace(
    """	if m.jobStats.Addressed != 0 {
		t.Errorf("Expected Addressed=0 after rollback, got %d", m.jobStats.Addressed)
	}
	if m.jobStats.Unaddressed != 1 {
		t.Errorf("Expected Unaddressed=1 after rollback, got %d", m.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m, 0, 1)"""
)

content = content.replace(
    """	if m.jobStats.Addressed != 1 || m.jobStats.Unaddressed != 0 {
		t.Fatalf("after toggle: Addressed=%d Unaddressed=%d",
			m.jobStats.Addressed, m.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m, 1, 0)"""
)

content = content.replace(
    """	if m.jobStats.Addressed != 1 || m.jobStats.Unaddressed != 0 {
		t.Fatalf("after poll: Addressed=%d Unaddressed=%d",
			m.jobStats.Addressed, m.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m, 1, 0)"""
)

content = content.replace(
    """	if m.jobStats.Addressed != 0 {
		t.Errorf("after rollback: expected Addressed=0, got %d",
			m.jobStats.Addressed)
	}
	if m.jobStats.Unaddressed != 1 {
		t.Errorf("after rollback: expected Unaddressed=1, got %d",
			m.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m, 0, 1)"""
)

content = content.replace(
    """	if m2.jobStats.Addressed != 1 {
		t.Errorf("expected Addressed=1, got %d",
			m2.jobStats.Addressed)
	}
	if m2.jobStats.Unaddressed != 1 {
		t.Errorf("expected Unaddressed=1, got %d",
			m2.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m2, 1, 1)"""
)

content = content.replace(
    """	if m2.jobStats.Addressed != 1 {
		t.Errorf("expected Addressed=1, got %d",
			m2.jobStats.Addressed)
	}
	if m2.jobStats.Unaddressed != 0 {
		t.Errorf("expected Unaddressed=0, got %d",
			m2.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m2, 1, 0)"""
)

content = content.replace(
    """	if m.jobStats.Addressed != 1 {
		t.Errorf("after confirm poll: expected Addressed=1, got %d",
			m.jobStats.Addressed)
	}
	if m.jobStats.Unaddressed != 0 {
		t.Errorf("after confirm poll: expected Unaddressed=0, got %d",
			m.jobStats.Unaddressed)
	}""",
    """	assertJobStats(t, m, 1, 0)"""
)

with open("cmd/roborev/tui_action_test.go", "w") as f:
    f.write(content)

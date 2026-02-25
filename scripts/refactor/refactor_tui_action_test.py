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
			t.Errorf("Failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if diff := cmp.Diff(expected, req); diff != "" {
			t.Errorf("Request payload mismatch (-want +got):\n%s", diff)
			http.Error(w, "payload mismatch", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(response)
	}
}
"""

if "expectJSONPost" not in content:
    content = content.replace('import (', 'import (\n\t"github.com/google/go-cmp/cmp"\n', 1)
    content += "\n" + helper1

# Replace TestTUIAddressReviewSuccess
content = re.sub(
    r'_, m := mockServerModel\(t, func\(w http.ResponseWriter, r \*http.Request\) \{\n.*?json\.NewEncoder\(w\)\.Encode\(map\[string\]bool\{"success": true\}\)\n\t\}\)',
    r'_, m := mockServerModel(t, expectJSONPost(t, "", addressRequest{JobID: 100, Addressed: true}, map[string]bool{"success": true}))',
    content,
    flags=re.DOTALL
)

# Replace TestTUIToggleAddressedForJobSuccess
content = re.sub(
    r'_, m := mockServerModel\(t, func\(w http.ResponseWriter, r \*http.Request\) \{\n\t\tif r\.URL\.Path == "/api/review/address".*?json\.NewEncoder\(w\)\.Encode\(map\[string\]bool\{"success": true\}\)\n\t\t\} else \{\n\t\t\tt\.Errorf.*?\n\t\t\}\n\t\}\)',
    r'_, m := mockServerModel(t, expectJSONPost(t, "/api/review/address", addressRequest{JobID: 1, Addressed: true}, map[string]bool{"success": true}))',
    content,
    flags=re.DOTALL
)

# Replace TestTUIAddressReviewInBackgroundSuccess
content = re.sub(
    r'_, m := mockServerModel\(t, func\(w http.ResponseWriter, r \*http.Request\) \{\n\t\tif r\.URL\.Path != "/api/review/address" \|\| r\.Method != http\.MethodPost \{\n\t\t\tt\.Fatalf.*?\n\t\t\}\n\t\tvar req addressRequest\n\t\tif err := json\.NewDecoder\(r\.Body\)\.Decode\(&req\); err != nil \{\n\t\t\tt\.Fatalf.*?\n\t\t\}\n\t\tif req\.JobID != 42 \{\n\t\t\tt\.Errorf.*?\n\t\t\}\n\t\tif req\.Addressed != true \{\n\t\t\tt\.Errorf.*?\n\t\t\}\n\t\tjson\.NewEncoder\(w\)\.Encode\(map\[string\]bool\{"success": true\}\)\n\t\}\)',
    r'_, m := mockServerModel(t, expectJSONPost(t, "/api/review/address", addressRequest{JobID: 42, Addressed: true}, map[string]bool{"success": true}))',
    content,
    flags=re.DOTALL
)

# Replace TestTUICancelJobSuccess
content = re.sub(
    r'_, m := mockServerModel\(t, func\(w http.ResponseWriter, r \*http.Request\) \{\n\t\tif r\.URL\.Path != "/api/job/cancel" \{\n\t\t\tt\.Errorf\("Expected /api/job/cancel, got %s", r\.URL\.Path\)\n\t\t\}\n\t\tif r\.Method != http\.MethodPost \{\n\t\t\tt\.Errorf\("Expected POST, got %s", r\.Method\)\n\t\t\}\n\t\tvar req struct \{\n\t\t\tJobID int64 `json:"job_id"`\n\t\t\}\n\t\tjson\.NewDecoder\(r\.Body\)\.Decode\(&req\)\n\t\tif req\.JobID != 42 \{\n\t\t\tt\.Errorf\("Expected job_id=42, got %d", req\.JobID\)\n\t\t\}\n\t\tjson\.NewEncoder\(w\)\.Encode\(map\[string\]any\{"success": true\}\)\n\t\}\)',
    r'''type cancelRequest struct {
		JobID int64 `json:"job_id"`
	}
	_, m := mockServerModel(t, expectJSONPost(t, "/api/job/cancel", cancelRequest{JobID: 42}, map[string]any{"success": true}))''',
    content,
    flags=re.DOTALL
)

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

# Replace type assertions
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
content = re.sub(
    r'if m\.jobStats\.Addressed != 0 \{\n\s*t\.Errorf\("Expected Addressed=0 after rollback, got %d", m\.jobStats\.Addressed\)\n\s*\}\n\s*if m\.jobStats\.Unaddressed != 1 \{\n\s*t\.Errorf\("Expected Unaddressed=1 after rollback, got %d", m\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m, 0, 1)',
    content
)

content = re.sub(
    r'if m\.jobStats\.Addressed != 1 \|\| m\.jobStats\.Unaddressed != 0 \{\n\s*t\.Fatalf\("after toggle: Addressed=%d Unaddressed=%d",\n\s*m\.jobStats\.Addressed, m\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m, 1, 0)',
    content
)
content = re.sub(
    r'if m\.jobStats\.Addressed != 1 \|\| m\.jobStats\.Unaddressed != 0 \{\n\s*t\.Fatalf\("after poll: Addressed=%d Unaddressed=%d",\n\s*m\.jobStats\.Addressed, m\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m, 1, 0)',
    content
)
content = re.sub(
    r'if m\.jobStats\.Addressed != 0 \{\n\s*t\.Errorf\("after rollback: expected Addressed=0, got %d",\n\s*m\.jobStats\.Addressed\)\n\s*\}\n\s*if m\.jobStats\.Unaddressed != 1 \{\n\s*t\.Errorf\("after rollback: expected Unaddressed=1, got %d",\n\s*m\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m, 0, 1)',
    content
)

content = re.sub(
    r'if m2\.jobStats\.Addressed != 1 \{\n\s*t\.Errorf\("expected Addressed=1, got %d",\n\s*m2\.jobStats\.Addressed\)\n\s*\}\n\s*if m2\.jobStats\.Unaddressed != 1 \{\n\s*t\.Errorf\("expected Unaddressed=1, got %d",\n\s*m2\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m2, 1, 1)',
    content
)

content = re.sub(
    r'if m2\.jobStats\.Addressed != 1 \{\n\s*t\.Errorf\("expected Addressed=1, got %d",\n\s*m2\.jobStats\.Addressed\)\n\s*\}\n\s*if m2\.jobStats\.Unaddressed != 0 \{\n\s*t\.Errorf\("expected Unaddressed=0, got %d",\n\s*m2\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m2, 1, 0)',
    content
)

content = re.sub(
    r'if m\.jobStats\.Addressed != 1 \{\n\s*t\.Errorf\("after confirm poll: expected Addressed=1, got %d",\n\s*m\.jobStats\.Addressed\)\n\s*\}\n\s*if m\.jobStats\.Unaddressed != 0 \{\n\s*t\.Errorf\("after confirm poll: expected Unaddressed=0, got %d",\n\s*m\.jobStats\.Unaddressed\)\n\s*\}',
    r'assertJobStats(t, m, 1, 0)',
    content
)

with open("cmd/roborev/tui_action_test.go", "w") as f:
    f.write(content)

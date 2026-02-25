import os
import glob

def process_file(file_path):
    with open(file_path, "r") as f:
        content = f.read()

    # In analyze_test.go
    old_assert = '''func assertContains(t *testing.T, s, substr, msg string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%s: expected string to contain %q", msg, substr)
	}
}'''
    new_assert = '''func assertContains(t *testing.T, s, substr, msg string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("%s: expected string to contain %q\\nDocument content:\\n%s", msg, substr, s)
	}
}'''
    content = content.replace(old_assert, new_assert)

    # In agent_test_helpers.go
    old_assert2 = '''func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected string to contain %q, got %q", substr, s)
	}
}'''
    new_assert2 = '''func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected string to contain %q\\nDocument content:\\n%s", substr, s)
	}
}'''
    content = content.replace(old_assert2, new_assert2)

    # In synthesize_test.go
    old_assert3 = '''func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q", substr)
	}
}'''
    new_assert3 = '''func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q\\nDocument content:\\n%s", substr, s)
	}
}'''
    content = content.replace(old_assert3, new_assert3)
    
    # Check for assertNotContains
    old_assert_not = '''func assertNotContains(t *testing.T, doc, substring, msg string) {
	t.Helper()
	if strings.Contains(doc, substring) {
		t.Errorf("%s: expected NOT to find %q in document:\\n%s", msg, substring, doc)
	}
}'''
    # Wait, test_helpers_test.go already has `\n%s` for doc, so we are good there.
    
    with open(file_path, "w") as f:
        f.write(content)

for f in ["cmd/roborev/analyze_test.go", "internal/agent/agent_test_helpers.go", "internal/review/synthesize_test.go"]:
    if os.path.exists(f):
        process_file(f)
        
print("Updated assertions.")

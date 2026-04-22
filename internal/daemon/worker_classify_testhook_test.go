package daemon

// SetTestClassifierVerdict overrides the classifier result for tests.
// Call with yes=false and reason="" to clear the override.
//
// This helper lives in a _test.go file so it is only compiled into the
// test binary. Production builds do not expose a setter for the
// testClassifierVerdict hook, so in-process agents or external
// integrations cannot bypass the configured classifier and force an
// auto-design routing decision.
func SetTestClassifierVerdict(yes bool, reason string) {
	testClassifierVerdict.mu.Lock()
	defer testClassifierVerdict.mu.Unlock()
	testClassifierVerdict.set = reason != "" || yes
	testClassifierVerdict.yes = yes
	testClassifierVerdict.reason = reason
}

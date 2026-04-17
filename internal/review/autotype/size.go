package autotype

import "strings"

// CountChangedLines counts `+` and `-` lines in a unified diff, excluding
// the `+++` and `---` file marker lines. Useful as a cheap proxy for
// "diff size" when deciding whether a change is trivial or large.
func CountChangedLines(diff string) int {
	if diff == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if strings.HasPrefix(line, "+++") {
				continue
			}
			n++
		case '-':
			if strings.HasPrefix(line, "---") {
				continue
			}
			n++
		}
	}
	return n
}

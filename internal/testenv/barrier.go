package testenv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProdLogBarrier records the state of production log files before
// tests run, and provides a Check method that fails hard if any
// test activity leaked into production logs.
type ProdLogBarrier struct {
	pid         int
	realDataDir string
	// Byte offsets at barrier creation time.
	activitySize int64
	errorsSize   int64
	// Snapshot of daemon.<pid>.json at barrier creation time.
	runtimeExisted bool
	runtimeSize    int64
	runtimeMtime   time.Time
}

// DefaultProdDataDir returns the default production data directory
// (~/.roborev). This is resolved from the user's home directory,
// ignoring ROBOREV_DATA_DIR so it always points to the real dir.
func DefaultProdDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".roborev")
}

// NewProdLogBarrier snapshots the current production data directory
// state. Call Check() after m.Run() to detect test pollution.
// realDataDir should be the default data directory (e.g. ~/.roborev)
// resolved BEFORE ROBOREV_DATA_DIR is overridden for tests.
func NewProdLogBarrier(realDataDir string) *ProdLogBarrier {
	b := &ProdLogBarrier{
		pid:         os.Getpid(),
		realDataDir: realDataDir,
	}
	b.activitySize = fileSize(
		filepath.Join(realDataDir, "activity.log"),
	)
	b.errorsSize = fileSize(
		filepath.Join(realDataDir, "errors.log"),
	)
	runtimePath := filepath.Join(
		realDataDir,
		fmt.Sprintf("daemon.%d.json", b.pid),
	)
	if info, err := os.Stat(runtimePath); err == nil {
		b.runtimeExisted = true
		b.runtimeSize = info.Size()
		b.runtimeMtime = info.ModTime()
	}
	return b
}

// Check verifies no test pollution reached production files.
// Returns a non-empty error message if pollution is detected.
func (b *ProdLogBarrier) Check() string {
	var violations []string

	// 1. Check for daemon runtime file with our PID.
	runtimePath := filepath.Join(
		b.realDataDir,
		fmt.Sprintf("daemon.%d.json", b.pid),
	)
	if info, err := os.Stat(runtimePath); err == nil {
		if !b.runtimeExisted {
			violations = append(violations,
				fmt.Sprintf(
					"test wrote daemon.%d.json to prod data dir",
					b.pid,
				),
			)
		} else if info.Size() != b.runtimeSize ||
			!info.ModTime().Equal(b.runtimeMtime) {
			violations = append(violations,
				fmt.Sprintf(
					"test modified daemon.%d.json in prod data dir"+
						" (size %d→%d, mtime %s→%s)",
					b.pid, b.runtimeSize, info.Size(),
					b.runtimeMtime.Format(time.RFC3339Nano),
					info.ModTime().Format(time.RFC3339Nano),
				),
			)
		}
	} else if b.runtimeExisted {
		violations = append(violations,
			fmt.Sprintf(
				"test deleted daemon.%d.json from prod data dir",
				b.pid,
			),
		)
	}

	// 2. Scan new lines in activity.log for test markers.
	if markers := b.scanNewLines(
		filepath.Join(b.realDataDir, "activity.log"),
		b.activitySize,
	); len(markers) > 0 {
		violations = append(violations,
			fmt.Sprintf(
				"test pollution in prod activity.log: %s",
				strings.Join(markers, "; "),
			),
		)
	}

	// 3. Scan new lines in errors.log for test markers.
	if markers := b.scanNewLines(
		filepath.Join(b.realDataDir, "errors.log"),
		b.errorsSize,
	); len(markers) > 0 {
		violations = append(violations,
			fmt.Sprintf(
				"test pollution in prod errors.log: %s",
				strings.Join(markers, "; "),
			),
		)
	}

	if len(violations) == 0 {
		return ""
	}
	return "PROD LOG BARRIER FAILED:\n  " +
		strings.Join(violations, "\n  ")
}

// scanNewLines reads lines appended after startOffset and returns
// descriptions of any lines that look like test pollution.
func (b *ProdLogBarrier) scanNewLines(
	path string, startOffset int64,
) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil // file gone or unreadable — not our problem
	}
	defer f.Close()

	if _, err := f.Seek(startOffset, 0); err != nil {
		return nil
	}

	pidStr := strconv.Itoa(b.pid)
	var markers []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	// 1MB buffer to handle large log lines without silent truncation.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		for _, m := range testMarkers(line, pidStr) {
			if !seen[m] {
				seen[m] = true
				markers = append(markers, m)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		markers = append(markers,
			fmt.Sprintf("scan error (barrier may be incomplete): %v", err))
	}
	return markers
}

// testMarkers returns marker descriptions if the line looks like
// test pollution. Checks for known patterns that only appear in
// test-generated log entries.
func testMarkers(line, pidStr string) []string {
	var out []string

	// daemon.started with our PID
	if strings.Contains(line, `"pid":"`+pidStr+`"`) &&
		strings.Contains(line, "daemon.started") {
		out = append(out, "daemon.started with test PID "+pidStr)
	}

	// Explicit test event markers
	if strings.Contains(line, `"event":"test"`) {
		out = append(out, `event:"test" entry`)
	}

	// dev version daemon start
	if strings.Contains(line, `"version":"dev"`) &&
		strings.Contains(line, "daemon.started") {
		out = append(out, `daemon.started with version:"dev"`)
	}

	return out
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

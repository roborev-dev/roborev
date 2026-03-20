package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveEntry struct {
	Name     string
	Content  string
	TypeFlag byte
	LinkName string
	Mode     int64
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type testReporter struct {
	steps    bytes.Buffer
	progress []int64
}

func (r *testReporter) Stepf(format string, args ...any) {
	_, _ = fmt.Fprintf(&r.steps, format, args...)
}

func (r *testReporter) Progress(downloaded, total int64) {
	r.progress = append(r.progress, downloaded, total)
}

func TestSanitizeTarPath(t *testing.T) {
	destDir := t.TempDir()

	tests := []struct {
		name     string
		path     string
		wantErr  bool
		targetOS string
	}{
		{"normal file", "roborev", false, ""},
		{"nested file", "bin/roborev", false, ""},
		{"absolute path Unix", "/etc/passwd", true, ""},
		{"path traversal with ..", "../../../etc/passwd", true, ""},
		{"path traversal mid-path", "foo/../../../etc/passwd", true, ""},
		{"hidden traversal", "foo/bar/../../..", true, ""},
		{"dot only", ".", false, ""},
		{"double dot only", "..", true, ""},
		{"empty path", "", false, ""},
		{"absolute path Windows", "C:\\Windows\\System32", true, "windows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skipUnlessTargetOS(t, tt.targetOS)

			_, err := sanitizeTarPath(destDir, tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestExtractTarGzPathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "malicious.tar.gz")
	extractDir := filepath.Join(tmpDir, "extract")
	outsideFile := filepath.Join(tmpDir, "pwned")

	createTestArchive(t, archivePath, []archiveEntry{
		{Name: "../pwned", Content: "owned"},
	})

	err := extractTarGz(archivePath, extractDir)
	require.Error(t, err)
	requirePathMissing(t, outsideFile)
}

func TestExtractTarGzSymlinkSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "symlink.tar.gz")
	extractDir := filepath.Join(tmpDir, "extract")

	createTestArchive(t, archivePath, []archiveEntry{
		{Name: "evil-link", TypeFlag: tar.TypeSymlink, LinkName: "/etc/passwd"},
		{Name: "normal.txt", Content: "test"},
	})

	require.NoError(t, extractTarGz(archivePath, extractDir))
	requirePathExists(t, filepath.Join(extractDir, "normal.txt"))
	requirePathMissing(t, filepath.Join(extractDir, "evil-link"))
}

func TestExtractChecksum(t *testing.T) {
	longHash := "abc123def456789012345678901234567890123456789012345678901234abcd"
	upperHash := strings.ToUpper(longHash)
	mixedHash := "AbC123DeF456789012345678901234567890123456789012345678901234aBcD"

	tests := []struct {
		name      string
		body      string
		assetName string
		want      string
	}{
		{
			name:      "standard sha256sum format",
			body:      fmt.Sprintf("%s  %s", longHash, "roborev_darwin_arm64.tar.gz"),
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      longHash,
		},
		{
			name:      "uppercase checksum",
			body:      fmt.Sprintf("%s  %s", upperHash, "roborev_linux_amd64.tar.gz"),
			assetName: "roborev_linux_amd64.tar.gz",
			want:      longHash,
		},
		{
			name:      "mixed case checksum",
			body:      fmt.Sprintf("%s  %s", mixedHash, "roborev_darwin_amd64.tar.gz"),
			assetName: "roborev_darwin_amd64.tar.gz",
			want:      longHash,
		},
		{
			name:      "colon format",
			body:      fmt.Sprintf("%s: %s", "roborev_darwin_arm64.tar.gz", longHash),
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      longHash,
		},
		{
			name: "multiline with target in middle",
			body: `abc123aef456789012345678901234567890123456789012345678901234abca  roborev_linux_amd64.tar.gz
abc123bef456789012345678901234567890123456789012345678901234abcb  roborev_darwin_arm64.tar.gz
abc123cef456789012345678901234567890123456789012345678901234abcc  roborev_darwin_amd64.tar.gz`,
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      "abc123bef456789012345678901234567890123456789012345678901234abcb",
		},
		{
			name:      "no match",
			body:      fmt.Sprintf("%s  %s", longHash, "roborev_linux_amd64.tar.gz"),
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      "",
		},
		{
			name:      "empty body",
			body:      "",
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractChecksum(tt.body, tt.assetName))
		})
	}
}

func TestExtractBaseSemver(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"0.4.0", "0.4.0"},
		{"1.2.3", "1.2.3"},
		{"v0.4.0", "0.4.0"},
		{"v1.2.3", "1.2.3"},
		{"0.4.0-5-gabcdef", "0.4.0"},
		{"v0.4.0-5-gabcdef", "0.4.0"},
		{"0.4.0-15-g1234567", "0.4.0"},
		{"1.2.3-100-gdeadbeef", "1.2.3"},
		{"0.4.0-dev", "0.4.0"},
		{"0.4.0-rc1", "0.4.0"},
		{"0.4.0-beta.1", "0.4.0"},
		{"v1.0.0-alpha", "1.0.0"},
		{"dev", ""},
		{"abc1234", ""},
		{"88be010", ""},
		{"abc1234-dirty", ""},
		{"", ""},
		{"1.2.3+meta", "1.2.3"},
		{"v1.2.3+build.42", "1.2.3"},
		{"1.0.0-rc1+build", "1.0.0"},
		{"0", ""},
		{"v", ""},
		{"vdev", ""},
		{"1.0", "1.0"},
		{"1.0.0.0", "1.0.0.0"},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.want, extractBaseSemver(tt.version))
		})
	}
}

func TestIsDevBuildVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"0.16.1", false},
		{"v0.16.1", false},
		{"1.0.0", false},
		{"v1.0.0", false},
		{"0.16.1-2-g75d300a", true},
		{"v0.16.1-2-g75d300a", true},
		{"0.4.0-5-gabcdef", true},
		{"1.2.3-100-gdeadbeef", true},
		{"0.16.1-2-g75d300a-dirty", true},
		{"v0.16.1-2-g75d300a-dirty", true},
		{"0.4.0-5-gabcdef-dirty", true},
		{"dev", true},
		{"abc1234", true},
		{"88be010", true},
		{"0.16.1-rc1", false},
		{"v1.0.0-beta.1", false},
		{"0.4.0-alpha", false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.want, isDevBuildVersion(tt.version))
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name   string
		v1, v2 string
		want   bool
	}{
		{"minor downgrade", "1.0.0", "0.9.0", true},
		{"minor upgrade", "1.1.0", "1.0.0", true},
		{"patch upgrade", "1.0.1", "1.0.0", true},
		{"major upgrade", "2.0.0", "1.9.9", true},
		{"same version", "1.0.0", "1.0.0", false},
		{"older version", "0.9.0", "1.0.0", false},
		{"v prefix upgrade", "v1.0.0", "v0.9.0", true},
		{"mixed v prefix upgrade 1", "v1.0.0", "0.9.0", true},
		{"mixed v prefix upgrade 2", "1.0.0", "v0.9.0", true},
		{"pure hash 1", "0.4.2", "88be010", false},
		{"dev keyword", "0.4.2", "dev", false},
		{"dirty hash", "0.4.2", "abc1234-dirty", false},
		{"pure hash v prefix", "v0.4.2", "88be010", false},
		{"bad version 1", "badversion", "0.4.0", false},
		{"bad version 2", "abc123", "0.4.0", false},
		{"git describe same base", "0.4.0", "0.4.0-5-gabcdef", false},
		{"git describe same base v prefix", "v0.4.0", "v0.4.0-5-gabcdef", false},
		{"git describe same base v prefix 2", "0.4.0", "v0.4.0-15-g1234567", false},
		{"git describe newer major", "0.5.0", "0.4.0-5-gabcdef", true},
		{"git describe newer major v prefix", "v0.5.0", "v0.4.0-5-gabcdef", true},
		{"git describe newer patch", "0.4.1", "0.4.0-5-gabcdef", true},
		{"git describe newer major 2", "1.0.0", "0.4.0-5-gabcdef", true},
		{"git describe older minor", "0.3.0", "0.4.0-5-gabcdef", false},
		{"git describe older major", "0.4.0", "0.5.0-5-gabcdef", false},
		{"prerelease same base", "0.4.0", "0.4.0-rc1", false},
		{"prerelease newer minor", "0.5.0", "0.4.0-rc1", true},
		{"prerelease dev same base", "0.4.0", "0.4.0-dev", false},
		{"prerelease dev newer minor", "0.5.0", "0.4.0-dev", true},
		{"build meta newer", "1.2.4", "1.2.3+meta", true},
		{"build meta same", "1.2.3", "1.2.3+meta", false},
		{"build meta diff", "1.2.3+build1", "1.2.3+build2", false},
		{"build meta older", "1.3.0+meta", "1.2.0", true},
		{"two part newer", "1.1", "1.0", true},
		{"four part newer", "1.0.0.1", "1.0.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isNewer(tt.v1, tt.v2))
		})
	}
}

func TestParsedVersionCompare(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{"same three-part", "1.2.3", "1.2.3", 0},
		{"two-part less than three-part with patch", "1.2", "1.2.1", -1},
		{"four-part greater", "1.2.3.4", "1.2.3.3", 1},
		{"git describe compares by base", "1.2.3-4-gabcd", "1.2.3", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := parseVersion(tt.left)
			right := parseVersion(tt.right)
			require.NotEmpty(t, left.parts)
			require.NotEmpty(t, right.parts)
			assert.Equal(t, tt.want, left.Compare(right))
		})
	}
}

func TestUpdaterCheckForUpdateSkipsNetworkWithFreshCache(t *testing.T) {
	cacheDir := t.TempDir()
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	writeCachedCheck(t, cacheDir, cachedCheck{
		CheckedAt: now.Add(-15 * time.Minute),
		Version:   "v1.2.3",
	})

	requests := 0
	updater := NewUpdater(Deps{
		Client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				return nil, fmt.Errorf("unexpected request to %s", req.URL.String())
			}),
		},
		Now:      func() time.Time { return now },
		Version:  "v1.2.3",
		GOOS:     "darwin",
		GOARCH:   "arm64",
		CacheDir: func() string { return cacheDir },
	})

	info, err := updater.CheckForUpdate(false)
	require.NoError(t, err)
	require.Nil(t, info)
	assert.Equal(t, 0, requests)
}

func TestUpdaterCheckForUpdateFallsBackToReleaseBodyChecksum(t *testing.T) {
	const releaseVersion = "v1.3.0"
	const assetName = "roborev_1.3.0_darwin_arm64.tar.gz"
	const checksum = "abc123def456789012345678901234567890123456789012345678901234abcd"

	releaseBody, err := json.Marshal(Release{
		TagName: releaseVersion,
		Body:    fmt.Sprintf("%s  %s", checksum, assetName),
		Assets: []Asset{
			{Name: assetName, Size: 42, BrowserDownloadURL: "https://downloads.example/" + assetName},
			{Name: "SHA256SUMS", Size: 12, BrowserDownloadURL: "https://downloads.example/SHA256SUMS"},
		},
	})
	require.NoError(t, err)

	updater := NewUpdater(Deps{
		Client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case githubAPIURL:
					return newHTTPResponse(http.StatusOK, string(releaseBody)), nil
				case "https://downloads.example/SHA256SUMS":
					return newHTTPResponse(http.StatusInternalServerError, "boom"), nil
				default:
					return nil, fmt.Errorf("unexpected request to %s", req.URL.String())
				}
			}),
		},
		Now:      func() time.Time { return time.Unix(0, 0) },
		Version:  "v1.2.0",
		GOOS:     "darwin",
		GOARCH:   "arm64",
		CacheDir: t.TempDir,
	})

	info, err := updater.CheckForUpdate(true)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, "v1.2.0", info.CurrentVersion)
	assert.Equal(t, releaseVersion, info.LatestVersion)
	assert.Equal(t, assetName, info.AssetName)
	assert.Equal(t, checksum, info.Checksum)
	assert.False(t, info.IsDevBuild)
}

func TestUpdaterPerformUpdateInstallsBinary(t *testing.T) {
	binaryName := "roborev"
	if runtime.GOOS == "windows" {
		binaryName = "roborev.exe"
	}

	archiveData := createTestArchiveBytes(t, []archiveEntry{
		{Name: binaryName, Content: "new-binary", Mode: 0755},
	})
	sum := sha256.Sum256(archiveData)
	expectedChecksum := hex.EncodeToString(sum[:])

	binDir := t.TempDir()
	currentBinary := filepath.Join(binDir, binaryName)
	require.NoError(t, os.WriteFile(currentBinary, []byte("old-binary"), 0755))

	updater := NewUpdater(Deps{
		Client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "https://downloads.example/"+binaryName+".tar.gz", req.URL.String())
				return newBinaryResponse(http.StatusOK, archiveData), nil
			}),
		},
		Version:    "v1.2.0",
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Executable: func() (string, error) { return currentBinary, nil },
		CacheDir:   t.TempDir,
	})

	reporter := &testReporter{}
	err := updater.PerformUpdate(&UpdateInfo{
		AssetName:   binaryName + ".tar.gz",
		DownloadURL: "https://downloads.example/" + binaryName + ".tar.gz",
		Size:        int64(len(archiveData)),
		Checksum:    expectedChecksum,
	}, reporter)
	require.NoError(t, err)

	installed, readErr := os.ReadFile(currentBinary)
	require.NoError(t, readErr)
	assert.Equal(t, "new-binary", string(installed))
	requirePathMissing(t, currentBinary+".old")
	assert.Contains(t, reporter.steps.String(), "Downloading")
	assert.Contains(t, reporter.steps.String(), "Verifying checksum... OK")
	assert.Contains(t, reporter.steps.String(), "Extracting...")
	assert.Contains(t, reporter.steps.String(), "Installing "+binaryName+"... OK")
	assert.NotEmpty(t, reporter.progress)
}

func TestFindAssets(t *testing.T) {
	standardAssets := []Asset{
		{Name: "roborev_linux_amd64.tar.gz", Size: 1000, BrowserDownloadURL: "https://example.com/linux_amd64"},
		{Name: "roborev_darwin_arm64.tar.gz", Size: 2000, BrowserDownloadURL: "https://example.com/darwin_arm64"},
		{Name: "SHA256SUMS", Size: 500, BrowserDownloadURL: "https://example.com/checksums"},
		{Name: "roborev_darwin_amd64.tar.gz", Size: 3000, BrowserDownloadURL: "https://example.com/darwin_amd64"},
		{Name: "roborev_windows_amd64.zip", Size: 4000, BrowserDownloadURL: "https://example.com/windows"},
	}

	noChecksumsAssets := []Asset{
		{Name: "roborev_linux_amd64.tar.gz", Size: 1000, BrowserDownloadURL: "https://example.com/linux"},
		{Name: "roborev_darwin_arm64.tar.gz", Size: 2000, BrowserDownloadURL: "https://example.com/darwin"},
	}

	tests := []struct {
		name          string
		assets        []Asset
		assetName     string
		wantAsset     *Asset
		wantChecksums *Asset
	}{
		{
			name:          "find darwin_arm64 (second in list)",
			assets:        standardAssets,
			assetName:     "roborev_darwin_arm64.tar.gz",
			wantAsset:     &Asset{BrowserDownloadURL: "https://example.com/darwin_arm64", Size: 2000},
			wantChecksums: &Asset{BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "find linux_amd64 (first in list)",
			assets:        standardAssets,
			assetName:     "roborev_linux_amd64.tar.gz",
			wantAsset:     &Asset{BrowserDownloadURL: "https://example.com/linux_amd64", Size: 1000},
			wantChecksums: &Asset{BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "find darwin_amd64 (after checksums)",
			assets:        standardAssets,
			assetName:     "roborev_darwin_amd64.tar.gz",
			wantAsset:     &Asset{BrowserDownloadURL: "https://example.com/darwin_amd64", Size: 3000},
			wantChecksums: &Asset{BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "asset not found",
			assets:        standardAssets,
			assetName:     "roborev_freebsd_amd64.tar.gz",
			wantAsset:     nil,
			wantChecksums: &Asset{BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "no checksums file",
			assets:        noChecksumsAssets,
			assetName:     "roborev_darwin_arm64.tar.gz",
			wantAsset:     &Asset{BrowserDownloadURL: "https://example.com/darwin", Size: 2000},
			wantChecksums: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, checksums := findAssets(tt.assets, tt.assetName)
			checkAsset(t, asset, tt.wantAsset)
			checkAsset(t, checksums, tt.wantChecksums)
		})
	}
}

func createTestArchive(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, createTestArchiveBytes(t, entries), 0644))
}

func createTestArchiveBytes(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for _, entry := range entries {
		mode := entry.Mode
		if mode == 0 {
			mode = 0644
		}
		typeFlag := entry.TypeFlag
		if typeFlag == 0 {
			typeFlag = tar.TypeReg
		}
		header := &tar.Header{
			Name:     entry.Name,
			Mode:     mode,
			Size:     int64(len(entry.Content)),
			Typeflag: typeFlag,
			Linkname: entry.LinkName,
		}
		require.NoError(t, tw.WriteHeader(header))
		if len(entry.Content) > 0 {
			_, err := tw.Write([]byte(entry.Content))
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

func checkAsset(t *testing.T, got *Asset, want *Asset) {
	t.Helper()
	if want == nil {
		require.Nil(t, got)
		return
	}

	require.NotNil(t, got)
	assert.Equal(t, want.BrowserDownloadURL, got.BrowserDownloadURL)
	assert.Equal(t, want.Size, got.Size)
}

func skipUnlessTargetOS(t *testing.T, target string) {
	t.Helper()
	switch target {
	case "windows":
		if runtime.GOOS != "windows" {
			t.Skip("Windows-only test")
		}
	case "!windows":
		if runtime.GOOS == "windows" {
			t.Skip("Unix-only test")
		}
	}
}

func requirePathExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.NoError(t, err)
}

func requirePathMissing(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	require.Error(t, err)
	require.True(t, os.IsNotExist(err), "expected %s to be absent, got %v", path, err)
}

func writeCachedCheck(t *testing.T, cacheDir string, cached cachedCheck) {
	t.Helper()
	data, err := json.Marshal(cached)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, cacheFileName), data, 0644))
}

func newHTTPResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func newBinaryResponse(statusCode int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

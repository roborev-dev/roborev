package update

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizeTarPath(t *testing.T) {
	destDir := t.TempDir()

	tests := []struct {
		name     string
		path     string
		wantErr  bool
		targetOS string // "" for all, "windows" for Windows-only, "!windows" for Unix-only
	}{
		{"normal file", "roborev", false, ""},
		{"nested file", "bin/roborev", false, ""},
		{"absolute path Unix", "/etc/passwd", true, ""}, // Rejected on all platforms (explicit / check)
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
			if tt.targetOS == "windows" && runtime.GOOS != "windows" {
				t.Skip("Windows-only test")
			}
			if tt.targetOS == "!windows" && runtime.GOOS == "windows" {
				t.Skip("Unix-only test")
			}
			_, err := sanitizeTarPath(destDir, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("sanitizeTarPath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

type archiveEntry struct {
	Name     string
	Content  string
	TypeFlag byte
	LinkName string
	Mode     int64
}

func createTestArchive(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gzw := gzip.NewWriter(f)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	for _, e := range entries {
		mode := e.Mode
		if mode == 0 {
			mode = 0644
		}
		h := &tar.Header{
			Name:     e.Name,
			Mode:     mode,
			Size:     int64(len(e.Content)),
			Typeflag: e.TypeFlag,
			Linkname: e.LinkName,
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(e.Content) > 0 {
			if _, err := tw.Write([]byte(e.Content)); err != nil {
				t.Fatal(err)
			}
		}
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

	// Extract should fail
	err := extractTarGz(archivePath, extractDir)
	if err == nil {
		t.Error("extractTarGz should fail with path traversal attempt")
	}

	// Verify the file wasn't created outside
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Error("Malicious file was created outside extract dir")
	}
}

func TestExtractTarGzSymlinkSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "symlink.tar.gz")
	extractDir := filepath.Join(tmpDir, "extract")

	createTestArchive(t, archivePath, []archiveEntry{
		{Name: "evil-link", TypeFlag: tar.TypeSymlink, LinkName: "/etc/passwd"},
		{Name: "normal.txt", Content: "test"},
	})

	// Extract should succeed (symlinks are skipped)
	if err := extractTarGz(archivePath, extractDir); err != nil {
		t.Fatalf("extractTarGz failed: %v", err)
	}

	// Normal file should exist
	if _, err := os.Stat(filepath.Join(extractDir, "normal.txt")); err != nil {
		t.Error("Normal file should have been extracted")
	}

	// Symlink should not exist
	if _, err := os.Lstat(filepath.Join(extractDir, "evil-link")); !os.IsNotExist(err) {
		t.Error("Symlink should have been skipped")
	}
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
			got := extractChecksum(tt.body, tt.assetName)
			if got != tt.want {
				t.Errorf("extractChecksum() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractBaseSemver(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		// Clean semver
		{"0.4.0", "0.4.0"},
		{"1.2.3", "1.2.3"},
		{"v0.4.0", "0.4.0"},
		{"v1.2.3", "1.2.3"},
		// Git describe format (dev builds)
		{"0.4.0-5-gabcdef", "0.4.0"},
		{"v0.4.0-5-gabcdef", "0.4.0"},
		{"0.4.0-15-g1234567", "0.4.0"},
		{"1.2.3-100-gdeadbeef", "1.2.3"},
		// Prerelease tags
		{"0.4.0-dev", "0.4.0"},
		{"0.4.0-rc1", "0.4.0"},
		{"0.4.0-beta.1", "0.4.0"},
		{"v1.0.0-alpha", "1.0.0"},
		// Pure dev/hash versions (no semver)
		{"dev", ""},
		{"abc1234", ""},
		{"88be010", ""},
		{"abc1234-dirty", ""},
		{"", ""},
		// Build metadata
		{"1.2.3+meta", "1.2.3"},
		{"v1.2.3+build.42", "1.2.3"},
		{"1.0.0-rc1+build", "1.0.0"},
		// Edge cases
		{"0", ""},              // No dots
		{"v", ""},              // Just v
		{"vdev", ""},           // v followed by non-digit
		{"1.0", "1.0"},         // Two-part version
		{"1.0.0.0", "1.0.0.0"}, // Four-part version
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := extractBaseSemver(tt.version)
			if got != tt.want {
				t.Errorf("extractBaseSemver(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestIsDevBuildVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		// Official releases - NOT dev builds
		{"0.16.1", false},
		{"v0.16.1", false},
		{"1.0.0", false},
		{"v1.0.0", false},

		// Git describe format - ARE dev builds
		{"0.16.1-2-g75d300a", true},
		{"v0.16.1-2-g75d300a", true},
		{"0.4.0-5-gabcdef", true},
		{"1.2.3-100-gdeadbeef", true},

		// Git describe with -dirty suffix - ARE dev builds
		{"0.16.1-2-g75d300a-dirty", true},
		{"v0.16.1-2-g75d300a-dirty", true},
		{"0.4.0-5-gabcdef-dirty", true},

		// Pure hash/dev - ARE dev builds
		{"dev", true},
		{"abc1234", true},
		{"88be010", true},

		// Prerelease tags - NOT dev builds (intentional releases)
		{"0.16.1-rc1", false},
		{"v1.0.0-beta.1", false},
		{"0.4.0-alpha", false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := isDevBuildVersion(tt.version)
			if got != tt.want {
				t.Errorf("isDevBuildVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		name   string
		v1, v2 string
		want   bool
	}{
		// Basic semver comparisons
		{"minor downgrade", "1.0.0", "0.9.0", true},
		{"minor upgrade", "1.1.0", "1.0.0", true},
		{"patch upgrade", "1.0.1", "1.0.0", true},
		{"major upgrade", "2.0.0", "1.9.9", true},
		{"same version", "1.0.0", "1.0.0", false},
		{"older version", "0.9.0", "1.0.0", false},
		{"v prefix upgrade", "v1.0.0", "v0.9.0", true},
		{"mixed v prefix upgrade 1", "v1.0.0", "0.9.0", true},
		{"mixed v prefix upgrade 2", "1.0.0", "v0.9.0", true},

		// Pure hash dev versions - skip update notification (can't determine relationship)
		{"pure hash 1", "0.4.2", "88be010", false},
		{"dev keyword", "0.4.2", "dev", false},
		{"dirty hash", "0.4.2", "abc1234-dirty", false},
		{"pure hash v prefix", "v0.4.2", "88be010", false},

		// Non-semver release version - not newer
		{"bad version 1", "badversion", "0.4.0", false},
		{"bad version 2", "abc123", "0.4.0", false},

		// Git describe format - KEY TEST CASES for dev builds
		// Dev build v0.4.0-5-gabcdef should NOT show update for v0.4.0
		{"git describe same base", "0.4.0", "0.4.0-5-gabcdef", false},
		{"git describe same base v prefix", "v0.4.0", "v0.4.0-5-gabcdef", false},
		{"git describe same base v prefix 2", "0.4.0", "v0.4.0-15-g1234567", false},

		// Dev build v0.4.0-5-gabcdef SHOULD show update for v0.5.0
		{"git describe newer major", "0.5.0", "0.4.0-5-gabcdef", true},
		{"git describe newer major v prefix", "v0.5.0", "v0.4.0-5-gabcdef", true},
		{"git describe newer patch", "0.4.1", "0.4.0-5-gabcdef", true},
		{"git describe newer major 2", "1.0.0", "0.4.0-5-gabcdef", true},

		// Dev build v0.4.0-5-gabcdef should NOT show update for v0.3.0
		{"git describe older minor", "0.3.0", "0.4.0-5-gabcdef", false},
		{"git describe older major", "0.4.0", "0.5.0-5-gabcdef", false},

		// Prerelease versions
		{"prerelease same base", "0.4.0", "0.4.0-rc1", false},
		{"prerelease newer minor", "0.5.0", "0.4.0-rc1", true},
		{"prerelease dev same base", "0.4.0", "0.4.0-dev", false},
		{"prerelease dev newer minor", "0.5.0", "0.4.0-dev", true},

		// Build metadata (+meta) suffix handling
		{"build meta newer", "1.2.4", "1.2.3+meta", true},
		{"build meta same", "1.2.3", "1.2.3+meta", false},
		{"build meta diff", "1.2.3+build1", "1.2.3+build2", false},
		{"build meta older", "1.3.0+meta", "1.2.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNewer(tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
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

func checkAsset(t *testing.T, got *Asset, want *Asset) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("expected nil asset, got %v", got)
		}
		return
	}
	if got == nil {
		t.Fatal("expected non-nil asset")
	}
	if got.BrowserDownloadURL != want.BrowserDownloadURL {
		t.Errorf("got URL %q, want %q", got.BrowserDownloadURL, want.BrowserDownloadURL)
	}
	if got.Size != want.Size {
		t.Errorf("got Size %d, want %d", got.Size, want.Size)
	}
}

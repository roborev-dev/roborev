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
		name    string
		version string
		want    string
	}{
		// Clean semver
		{name: "clean semver 1", version: "0.4.0", want: "0.4.0"},
		{name: "clean semver 2", version: "1.2.3", want: "1.2.3"},
		{name: "clean semver with v 1", version: "v0.4.0", want: "0.4.0"},
		{name: "clean semver with v 2", version: "v1.2.3", want: "1.2.3"},
		// Git describe format (dev builds)
		{name: "git describe 1", version: "0.4.0-5-gabcdef", want: "0.4.0"},
		{name: "git describe 2", version: "v0.4.0-5-gabcdef", want: "0.4.0"},
		{name: "git describe 3", version: "0.4.0-15-g1234567", want: "0.4.0"},
		{name: "git describe 4", version: "1.2.3-100-gdeadbeef", want: "1.2.3"},
		// Prerelease tags
		{name: "prerelease dev", version: "0.4.0-dev", want: "0.4.0"},
		{name: "prerelease rc", version: "0.4.0-rc1", want: "0.4.0"},
		{name: "prerelease beta", version: "0.4.0-beta.1", want: "0.4.0"},
		{name: "prerelease alpha", version: "v1.0.0-alpha", want: "1.0.0"},
		// Pure dev/hash versions (no semver)
		{name: "pure dev keyword", version: "dev", want: ""},
		{name: "pure hash 1", version: "abc1234", want: ""},
		{name: "pure hash 2", version: "88be010", want: ""},
		{name: "dirty hash", version: "abc1234-dirty", want: ""},
		{name: "empty string", version: "", want: ""},
		// Build metadata
		{name: "build metadata 1", version: "1.2.3+meta", want: "1.2.3"},
		{name: "build metadata 2", version: "v1.2.3+build.42", want: "1.2.3"},
		{name: "build metadata 3", version: "1.0.0-rc1+build", want: "1.0.0"},
		// Edge cases
		{name: "no dots", version: "0", want: ""},                        // No dots
		{name: "just v", version: "v", want: ""},                         // Just v
		{name: "v followed by non-digit", version: "vdev", want: ""},     // v followed by non-digit
		{name: "two-part version", version: "1.0", want: "1.0"},          // Two-part version
		{name: "four-part version", version: "1.0.0.0", want: "1.0.0.0"}, // Four-part version
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBaseSemver(tt.version)
			if got != tt.want {
				t.Errorf("extractBaseSemver(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestIsDevBuildVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		// Official releases - NOT dev builds
		{name: "official release 1", version: "0.16.1", want: false},
		{name: "official release 2", version: "v0.16.1", want: false},
		{name: "official release 3", version: "1.0.0", want: false},
		{name: "official release 4", version: "v1.0.0", want: false},

		// Git describe format - ARE dev builds
		{name: "git describe 1", version: "0.16.1-2-g75d300a", want: true},
		{name: "git describe 2", version: "v0.16.1-2-g75d300a", want: true},
		{name: "git describe 3", version: "0.4.0-5-gabcdef", want: true},
		{name: "git describe 4", version: "1.2.3-100-gdeadbeef", want: true},

		// Git describe with -dirty suffix - ARE dev builds
		{name: "git describe dirty 1", version: "0.16.1-2-g75d300a-dirty", want: true},
		{name: "git describe dirty 2", version: "v0.16.1-2-g75d300a-dirty", want: true},
		{name: "git describe dirty 3", version: "0.4.0-5-gabcdef-dirty", want: true},

		// Pure hash/dev - ARE dev builds
		{name: "pure dev keyword", version: "dev", want: true},
		{name: "pure hash 1", version: "abc1234", want: true},
		{name: "pure hash 2", version: "88be010", want: true},

		// Prerelease tags - NOT dev builds (intentional releases)
		{name: "prerelease rc", version: "0.16.1-rc1", want: false},
		{name: "prerelease beta", version: "v1.0.0-beta.1", want: false},
		{name: "prerelease alpha", version: "0.4.0-alpha", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		{name: "minor downgrade", v1: "1.0.0", v2: "0.9.0", want: true},
		{name: "minor upgrade", v1: "1.1.0", v2: "1.0.0", want: true},
		{name: "patch upgrade", v1: "1.0.1", v2: "1.0.0", want: true},
		{name: "major upgrade", v1: "2.0.0", v2: "1.9.9", want: true},
		{name: "same version", v1: "1.0.0", v2: "1.0.0", want: false},
		{name: "older version", v1: "0.9.0", v2: "1.0.0", want: false},
		{name: "v prefix upgrade", v1: "v1.0.0", v2: "v0.9.0", want: true},
		{name: "mixed v prefix upgrade 1", v1: "v1.0.0", v2: "0.9.0", want: true},
		{name: "mixed v prefix upgrade 2", v1: "1.0.0", v2: "v0.9.0", want: true},

		// Pure hash dev versions - skip update notification (can't determine relationship)
		{name: "pure hash 1", v1: "0.4.2", v2: "88be010", want: false},
		{name: "dev keyword", v1: "0.4.2", v2: "dev", want: false},
		{name: "dirty hash", v1: "0.4.2", v2: "abc1234-dirty", want: false},
		{name: "pure hash v prefix", v1: "v0.4.2", v2: "88be010", want: false},

		// Non-semver release version - not newer
		{name: "bad version 1", v1: "badversion", v2: "0.4.0", want: false},
		{name: "bad version 2", v1: "abc123", v2: "0.4.0", want: false},

		// Git describe format - KEY TEST CASES for dev builds
		// Dev build v0.4.0-5-gabcdef should NOT show update for v0.4.0
		{name: "git describe same base", v1: "0.4.0", v2: "0.4.0-5-gabcdef", want: false},
		{name: "git describe same base v prefix", v1: "v0.4.0", v2: "v0.4.0-5-gabcdef", want: false},
		{name: "git describe same base v prefix 2", v1: "0.4.0", v2: "v0.4.0-15-g1234567", want: false},

		// Dev build v0.4.0-5-gabcdef SHOULD show update for v0.5.0
		{name: "git describe newer major", v1: "0.5.0", v2: "0.4.0-5-gabcdef", want: true},
		{name: "git describe newer major v prefix", v1: "v0.5.0", v2: "v0.4.0-5-gabcdef", want: true},
		{name: "git describe newer patch", v1: "0.4.1", v2: "0.4.0-5-gabcdef", want: true},
		{name: "git describe newer major 2", v1: "1.0.0", v2: "0.4.0-5-gabcdef", want: true},

		// Dev build v0.4.0-5-gabcdef should NOT show update for v0.3.0
		{name: "git describe older minor", v1: "0.3.0", v2: "0.4.0-5-gabcdef", want: false},
		{name: "git describe older major", v1: "0.4.0", v2: "0.5.0-5-gabcdef", want: false},

		// Prerelease versions
		{name: "prerelease same base", v1: "0.4.0", v2: "0.4.0-rc1", want: false},
		{name: "prerelease newer minor", v1: "0.5.0", v2: "0.4.0-rc1", want: true},
		{name: "prerelease dev same base", v1: "0.4.0", v2: "0.4.0-dev", want: false},
		{name: "prerelease dev newer minor", v1: "0.5.0", v2: "0.4.0-dev", want: true},

		// Build metadata (+meta) suffix handling
		{name: "build meta newer", v1: "1.2.4", v2: "1.2.3+meta", want: true},
		{name: "build meta same", v1: "1.2.3", v2: "1.2.3+meta", want: false},
		{name: "build meta diff", v1: "1.2.3+build1", v2: "1.2.3+build2", want: false},
		{name: "build meta older", v1: "1.3.0+meta", v2: "1.2.0", want: true},
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
			wantAsset:     &Asset{Name: "roborev_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_arm64", Size: 2000},
			wantChecksums: &Asset{Name: "SHA256SUMS", BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "find linux_amd64 (first in list)",
			assets:        standardAssets,
			assetName:     "roborev_linux_amd64.tar.gz",
			wantAsset:     &Asset{Name: "roborev_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux_amd64", Size: 1000},
			wantChecksums: &Asset{Name: "SHA256SUMS", BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "find darwin_amd64 (after checksums)",
			assets:        standardAssets,
			assetName:     "roborev_darwin_amd64.tar.gz",
			wantAsset:     &Asset{Name: "roborev_darwin_amd64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_amd64", Size: 3000},
			wantChecksums: &Asset{Name: "SHA256SUMS", BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "asset not found",
			assets:        standardAssets,
			assetName:     "roborev_freebsd_amd64.tar.gz",
			wantAsset:     nil,
			wantChecksums: &Asset{Name: "SHA256SUMS", BrowserDownloadURL: "https://example.com/checksums", Size: 500},
		},
		{
			name:          "no checksums file",
			assets:        noChecksumsAssets,
			assetName:     "roborev_darwin_arm64.tar.gz",
			wantAsset:     &Asset{Name: "roborev_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin", Size: 2000},
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
			t.Errorf("expected nil asset, got %+v", got)
		}
		return
	}
	if got == nil {
		t.Fatal("expected non-nil asset")
	}
	if *got != *want {
		t.Errorf("got asset %+v, want %+v", *got, *want)
	}
}

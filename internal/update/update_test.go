package update

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSanitizeTarPath(t *testing.T) {
	destDir := t.TempDir()

	tests := []struct {
		name      string
		path      string
		wantErr   bool
		skipOnWin bool // Skip on Windows (e.g., Unix-style absolute paths)
	}{
		{"normal file", "roborev", false, false},
		{"nested file", "bin/roborev", false, false},
		{"absolute path Unix", "/etc/passwd", true, false}, // Rejected on all platforms (explicit / check)
		{"path traversal with ..", "../../../etc/passwd", true, false},
		{"path traversal mid-path", "foo/../../../etc/passwd", true, false},
		{"hidden traversal", "foo/bar/../../..", true, false},
		{"dot only", ".", false, false},
		{"double dot only", "..", true, false},
		{"empty path", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.skipOnWin && runtime.GOOS == "windows" {
				t.Skip("Unix-style absolute path not applicable on Windows")
			}
			_, err := sanitizeTarPath(destDir, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("sanitizeTarPath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}

	// Windows-specific absolute path test
	if runtime.GOOS == "windows" {
		t.Run("absolute path Windows", func(t *testing.T) {
			_, err := sanitizeTarPath(destDir, "C:\\Windows\\System32")
			if err == nil {
				t.Error("sanitizeTarPath(\"C:\\\\Windows\\\\System32\") expected error, got nil")
			}
		})
	}
}

func TestExtractTarGzPathTraversal(t *testing.T) {
	// Create a malicious tar.gz with path traversal
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "malicious.tar.gz")
	extractDir := filepath.Join(tmpDir, "extract")
	outsideFile := filepath.Join(tmpDir, "pwned")

	// Create archive with path traversal attempt
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)

	// Add a malicious entry that tries to escape
	header := &tar.Header{
		Name: "../pwned",
		Mode: 0644,
		Size: 5,
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("owned")); err != nil {
		t.Fatal(err)
	}

	tw.Close()
	gzw.Close()
	f.Close()

	// Extract should fail
	err = extractTarGz(archivePath, extractDir)
	if err == nil {
		t.Error("extractTarGz should fail with path traversal attempt")
	}

	// Verify the file wasn't created outside
	if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
		t.Error("Malicious file was created outside extract dir")
	}
}

func TestExtractTarGzSymlinkSkipped(t *testing.T) {
	// Create a tar.gz with a symlink
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "symlink.tar.gz")
	extractDir := filepath.Join(tmpDir, "extract")

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)

	// Add a symlink entry
	header := &tar.Header{
		Name:     "evil-link",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}

	// Add a normal file
	header = &tar.Header{
		Name: "normal.txt",
		Mode: 0644,
		Size: 4,
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}

	tw.Close()
	gzw.Close()
	f.Close()

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
	tests := []struct {
		name      string
		body      string
		assetName string
		want      string
	}{
		{
			name:      "standard sha256sum format",
			body:      "abc123def456789012345678901234567890123456789012345678901234abcd  roborev_darwin_arm64.tar.gz",
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      "abc123def456789012345678901234567890123456789012345678901234abcd",
		},
		{
			name:      "uppercase checksum",
			body:      "ABC123DEF456789012345678901234567890123456789012345678901234ABCD  roborev_linux_amd64.tar.gz",
			assetName: "roborev_linux_amd64.tar.gz",
			want:      "abc123def456789012345678901234567890123456789012345678901234abcd",
		},
		{
			name:      "mixed case checksum",
			body:      "AbC123DeF456789012345678901234567890123456789012345678901234aBcD  roborev_darwin_amd64.tar.gz",
			assetName: "roborev_darwin_amd64.tar.gz",
			want:      "abc123def456789012345678901234567890123456789012345678901234abcd",
		},
		{
			name:      "colon format",
			body:      "roborev_darwin_arm64.tar.gz: abc123def456789012345678901234567890123456789012345678901234abcd",
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      "abc123def456789012345678901234567890123456789012345678901234abcd",
		},
		{
			name:      "multiline with target in middle",
			body:      "abc123def456789012345678901234567890123456789012345678901234aaaa  roborev_linux_amd64.tar.gz\nabc123def456789012345678901234567890123456789012345678901234bbbb  roborev_darwin_arm64.tar.gz\nabc123def456789012345678901234567890123456789012345678901234cccc  roborev_darwin_amd64.tar.gz",
			assetName: "roborev_darwin_arm64.tar.gz",
			want:      "abc123def456789012345678901234567890123456789012345678901234bbbb",
		},
		{
			name:      "no match",
			body:      "abc123def456789012345678901234567890123456789012345678901234abcd  roborev_linux_amd64.tar.gz",
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
		// Edge cases
		{"0", ""},           // No dots
		{"v", ""},           // Just v
		{"vdev", ""},        // v followed by non-digit
		{"1.0", "1.0"},      // Two-part version
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
		v1, v2 string
		want   bool
	}{
		// Basic semver comparisons
		{"1.0.0", "0.9.0", true},
		{"1.1.0", "1.0.0", true},
		{"1.0.1", "1.0.0", true},
		{"2.0.0", "1.9.9", true},
		{"1.0.0", "1.0.0", false},
		{"0.9.0", "1.0.0", false},
		{"v1.0.0", "v0.9.0", true},
		{"v1.0.0", "0.9.0", true},
		{"1.0.0", "v0.9.0", true},

		// Pure hash dev versions - skip update notification (can't determine relationship)
		{"0.4.2", "88be010", false},
		{"0.4.2", "dev", false},
		{"0.4.2", "abc1234-dirty", false},
		{"v0.4.2", "88be010", false},

		// Non-semver release version - not newer
		{"badversion", "0.4.0", false},
		{"abc123", "0.4.0", false},

		// Git describe format - KEY TEST CASES for dev builds
		// Dev build v0.4.0-5-gabcdef should NOT show update for v0.4.0
		{"0.4.0", "0.4.0-5-gabcdef", false},
		{"v0.4.0", "v0.4.0-5-gabcdef", false},
		{"0.4.0", "v0.4.0-15-g1234567", false},

		// Dev build v0.4.0-5-gabcdef SHOULD show update for v0.5.0
		{"0.5.0", "0.4.0-5-gabcdef", true},
		{"v0.5.0", "v0.4.0-5-gabcdef", true},
		{"0.4.1", "0.4.0-5-gabcdef", true},
		{"1.0.0", "0.4.0-5-gabcdef", true},

		// Dev build v0.4.0-5-gabcdef should NOT show update for v0.3.0
		{"0.3.0", "0.4.0-5-gabcdef", false},
		{"0.4.0", "0.5.0-5-gabcdef", false},

		// Prerelease versions
		{"0.4.0", "0.4.0-rc1", false},
		{"0.5.0", "0.4.0-rc1", true},
		{"0.4.0", "0.4.0-dev", false},
		{"0.5.0", "0.4.0-dev", true},
	}

	for _, tt := range tests {
		t.Run(tt.v1+"_vs_"+tt.v2, func(t *testing.T) {
			got := isNewer(tt.v1, tt.v2)
			if got != tt.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}

func TestFindAssets(t *testing.T) {
	// Test that findAssets correctly selects assets from a list with multiple items
	// This specifically tests the loop variable pointer bug fix
	assets := []Asset{
		{Name: "roborev_linux_amd64.tar.gz", Size: 1000, BrowserDownloadURL: "https://example.com/linux_amd64"},
		{Name: "roborev_darwin_arm64.tar.gz", Size: 2000, BrowserDownloadURL: "https://example.com/darwin_arm64"},
		{Name: "SHA256SUMS", Size: 500, BrowserDownloadURL: "https://example.com/checksums"},
		{Name: "roborev_darwin_amd64.tar.gz", Size: 3000, BrowserDownloadURL: "https://example.com/darwin_amd64"},
		{Name: "roborev_windows_amd64.zip", Size: 4000, BrowserDownloadURL: "https://example.com/windows"},
	}

	tests := []struct {
		name              string
		assetName         string
		wantAssetURL      string
		wantAssetSize     int64
		wantChecksumsURL  string
		wantAssetNil      bool
		wantChecksumsNil  bool
	}{
		{
			name:             "find darwin_arm64 (second in list)",
			assetName:        "roborev_darwin_arm64.tar.gz",
			wantAssetURL:     "https://example.com/darwin_arm64",
			wantAssetSize:    2000,
			wantChecksumsURL: "https://example.com/checksums",
		},
		{
			name:             "find linux_amd64 (first in list)",
			assetName:        "roborev_linux_amd64.tar.gz",
			wantAssetURL:     "https://example.com/linux_amd64",
			wantAssetSize:    1000,
			wantChecksumsURL: "https://example.com/checksums",
		},
		{
			name:             "find darwin_amd64 (after checksums)",
			assetName:        "roborev_darwin_amd64.tar.gz",
			wantAssetURL:     "https://example.com/darwin_amd64",
			wantAssetSize:    3000,
			wantChecksumsURL: "https://example.com/checksums",
		},
		{
			name:         "asset not found",
			assetName:    "roborev_freebsd_amd64.tar.gz",
			wantAssetNil: true,
			wantChecksumsURL: "https://example.com/checksums",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, checksums := findAssets(assets, tt.assetName)

			if tt.wantAssetNil {
				if asset != nil {
					t.Errorf("expected asset to be nil, got %+v", asset)
				}
			} else {
				if asset == nil {
					t.Fatal("expected asset to be non-nil")
				}
				if asset.BrowserDownloadURL != tt.wantAssetURL {
					t.Errorf("asset URL = %q, want %q", asset.BrowserDownloadURL, tt.wantAssetURL)
				}
				if asset.Size != tt.wantAssetSize {
					t.Errorf("asset size = %d, want %d", asset.Size, tt.wantAssetSize)
				}
			}

			if tt.wantChecksumsNil {
				if checksums != nil {
					t.Errorf("expected checksums to be nil, got %+v", checksums)
				}
			} else {
				if checksums == nil {
					t.Fatal("expected checksums to be non-nil")
				}
				if checksums.BrowserDownloadURL != tt.wantChecksumsURL {
					t.Errorf("checksums URL = %q, want %q", checksums.BrowserDownloadURL, tt.wantChecksumsURL)
				}
			}
		})
	}
}

func TestFindAssetsNoChecksums(t *testing.T) {
	// Test case where there's no checksums file
	assets := []Asset{
		{Name: "roborev_linux_amd64.tar.gz", Size: 1000, BrowserDownloadURL: "https://example.com/linux"},
		{Name: "roborev_darwin_arm64.tar.gz", Size: 2000, BrowserDownloadURL: "https://example.com/darwin"},
	}

	asset, checksums := findAssets(assets, "roborev_darwin_arm64.tar.gz")

	if asset == nil {
		t.Fatal("expected asset to be non-nil")
	}
	if asset.BrowserDownloadURL != "https://example.com/darwin" {
		t.Errorf("asset URL = %q, want %q", asset.BrowserDownloadURL, "https://example.com/darwin")
	}
	if checksums != nil {
		t.Errorf("expected checksums to be nil, got %+v", checksums)
	}
}

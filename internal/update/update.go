package update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/roborev-dev/roborev/internal/config"
	"github.com/roborev-dev/roborev/internal/version"
)

const (
	githubAPIURL      = "https://api.github.com/repos/roborev-dev/roborev/releases/latest"
	cacheFileName     = "update_check.json"
	cacheDuration     = 1 * time.Hour
	devCacheDuration  = 15 * time.Minute // Shorter cache for dev builds
)

// Release represents a GitHub release
type Release struct {
	TagName string  `json:"tag_name"`
	Body    string  `json:"body"`
	Assets  []Asset `json:"assets"`
}

// Asset represents a release asset
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// UpdateInfo contains information about an available update
type UpdateInfo struct {
	CurrentVersion string
	LatestVersion  string
	DownloadURL    string
	AssetName      string
	Size           int64
	Checksum       string // SHA256 if available
	IsDevBuild     bool   // True if running a dev build (hash version)
}

// findAssets locates the platform-specific binary and checksums file from release assets
func findAssets(assets []Asset, assetName string) (asset *Asset, checksumsAsset *Asset) {
	for i := range assets {
		a := &assets[i]
		if a.Name == assetName {
			asset = a
		}
		if a.Name == "SHA256SUMS" || a.Name == "checksums.txt" {
			checksumsAsset = a
		}
	}
	return asset, checksumsAsset
}

// cachedCheck stores the last update check result
type cachedCheck struct {
	CheckedAt time.Time `json:"checked_at"`
	Version   string    `json:"version"`
}

// CheckForUpdate checks if a newer version is available
// Uses a 1-hour cache to avoid hitting GitHub API too often
func CheckForUpdate(forceCheck bool) (*UpdateInfo, error) {
	currentVersion := strings.TrimPrefix(version.Version, "v")
	isDevBuild := isDevBuildVersion(currentVersion)

	// Check cache first (unless forced)
	// Use shorter cache for dev builds so they learn about releases sooner
	cacheWindow := cacheDuration
	if isDevBuild {
		cacheWindow = devCacheDuration
	}
	if !forceCheck {
		if cached, err := loadCache(); err == nil {
			if time.Since(cached.CheckedAt) < cacheWindow {
				latestVersion := strings.TrimPrefix(cached.Version, "v")
				if !isDevBuild && !isNewer(latestVersion, currentVersion) {
					return nil, nil // Up to date (cached)
				}
				// For dev builds, return cached version (notification only needs version)
				// Full download info is fetched when actually updating (forceCheck=true)
				if isDevBuild {
					return &UpdateInfo{
						CurrentVersion: version.Version,
						LatestVersion:  cached.Version,
						IsDevBuild:     true,
					}, nil
				}
				// Cache says update available, fetch fresh info for download URLs
			}
		}
	}

	// Fetch latest release from GitHub
	release, err := fetchLatestRelease()
	if err != nil {
		return nil, fmt.Errorf("check for updates: %w", err)
	}

	// Save to cache
	saveCache(release.TagName)

	latestVersion := strings.TrimPrefix(release.TagName, "v")

	// For dev builds, always notify about the latest release
	// For regular builds, only notify if there's a newer version
	if !isDevBuild && !isNewer(latestVersion, currentVersion) {
		return nil, nil // Up to date
	}

	// Find the right asset for this platform
	// Asset naming: roborev_<version>_<os>_<arch>.tar.gz (e.g., roborev_0.3.0_darwin_arm64.tar.gz)
	assetName := fmt.Sprintf("roborev_%s_%s_%s.tar.gz", latestVersion, runtime.GOOS, runtime.GOARCH)
	asset, checksumsAsset := findAssets(release.Assets, assetName)
	if asset == nil {
		return nil, fmt.Errorf("no release asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	// Get checksum - first try checksums file, then release body
	var checksum string
	if checksumsAsset != nil {
		checksum, _ = fetchChecksumFromFile(checksumsAsset.BrowserDownloadURL, assetName)
	}
	if checksum == "" {
		// Fall back to release body
		checksum = extractChecksum(release.Body, assetName)
	}

	return &UpdateInfo{
		CurrentVersion: version.Version,
		LatestVersion:  release.TagName,
		DownloadURL:    asset.BrowserDownloadURL,
		AssetName:      asset.Name,
		Size:           asset.Size,
		Checksum:       checksum,
		IsDevBuild:     isDevBuild,
	}, nil
}

// PerformUpdate downloads and installs the update
func PerformUpdate(info *UpdateInfo, progressFn func(downloaded, total int64)) error {
	// Security: require checksum verification
	if info.Checksum == "" {
		return fmt.Errorf("no checksum available for %s - refusing to install unverified binary", info.AssetName)
	}

	// 1. Download to temp file
	fmt.Printf("Downloading %s...\n", info.AssetName)
	tempDir, err := os.MkdirTemp("", "roborev-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, info.AssetName)
	checksum, err := downloadFile(info.DownloadURL, archivePath, info.Size, progressFn)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// 2. Verify checksum (required)
	fmt.Printf("Verifying checksum... ")
	if !strings.EqualFold(checksum, info.Checksum) {
		fmt.Println("FAILED")
		return fmt.Errorf("checksum mismatch: expected %s, got %s", info.Checksum, checksum)
	}
	fmt.Println("OK")

	// 3. Extract archive
	fmt.Println("Extracting...")
	extractDir := filepath.Join(tempDir, "extracted")
	if err := extractTarGz(archivePath, extractDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// 4. Find current binary locations
	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find current executable: %w", err)
	}
	currentExe, err = filepath.EvalSymlinks(currentExe)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}
	binDir := filepath.Dir(currentExe)

	// 5. Install new binary
	binaries := []string{"roborev"}
	if runtime.GOOS == "windows" {
		binaries = []string{"roborev.exe"}
	}

	for _, binary := range binaries {
		srcPath := filepath.Join(extractDir, binary)
		dstPath := filepath.Join(binDir, binary)
		backupPath := dstPath + ".old"

		// Check if source exists
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			continue // Skip if not in archive
		}

		fmt.Printf("Installing %s... ", binary)

		// Clean up any old backup from previous update
		os.Remove(backupPath)

		// Backup existing
		if _, err := os.Stat(dstPath); err == nil {
			if err := os.Rename(dstPath, backupPath); err != nil {
				// On Windows, renaming a running executable may fail
				if runtime.GOOS == "windows" {
					return fmt.Errorf("cannot update %s while it is running - please stop the daemon and try again: %w", binary, err)
				}
				return fmt.Errorf("backup %s: %w", binary, err)
			}
		}

		// Copy new binary
		if err := copyFile(srcPath, dstPath); err != nil {
			// Try to restore backup
			os.Rename(backupPath, dstPath)
			return fmt.Errorf("install %s: %w", binary, err)
		}

		// Set executable permission (no-op on Windows)
		if runtime.GOOS != "windows" {
			if err := os.Chmod(dstPath, 0755); err != nil {
				return fmt.Errorf("chmod %s: %w", binary, err)
			}
		}

		// Try to remove backup (may fail on Windows if daemon was running)
		// The .old file will be cleaned up on next update
		os.Remove(backupPath)

		fmt.Println("OK")
	}

	return nil
}

// RestartDaemon stops and starts the daemon
func RestartDaemon() error {
	// The CLI will handle the actual restart via `roborev daemon restart`
	// Since we're in a library, we just return nil
	return nil
}

// GetCacheDir returns the roborev cache directory
func GetCacheDir() string {
	return config.DataDir()
}

func fetchLatestRelease() (*Release, error) {
	req, err := http.NewRequest("GET", githubAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "roborev/"+version.Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

func downloadFile(url, dest string, totalSize int64, progressFn func(downloaded, total int64)) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer out.Close()

	// Calculate checksum while downloading
	hasher := sha256.New()
	writer := io.MultiWriter(out, hasher)

	// Download with progress
	var downloaded int64
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			_, writeErr := writer.Write(buf[:n])
			if writeErr != nil {
				return "", writeErr
			}
			downloaded += int64(n)
			if progressFn != nil {
				progressFn(downloaded, totalSize)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func extractTarGz(archivePath, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	// Get absolute path of destDir for security checks
	absDestDir, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("resolve dest dir: %w", err)
	}

	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Security: sanitize and validate the path
		target, err := sanitizeTarPath(absDestDir, header.Name)
		if err != nil {
			return fmt.Errorf("invalid tar entry %q: %w", header.Name, err)
		}

		// Security: skip symlinks and hardlinks to prevent attacks
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			outFile, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
			if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		}
	}

	return nil
}

// sanitizeTarPath validates and sanitizes a tar entry path to prevent directory traversal
func sanitizeTarPath(destDir, name string) (string, error) {
	// Reject Unix-style absolute paths explicitly (before Clean converts / to \\ on Windows)
	// This ensures consistent behavior across platforms for tar entries created on Unix
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute path not allowed")
	}

	// Clean the path to remove . and .. components
	cleanName := filepath.Clean(name)

	// Reject absolute paths (Windows drive letters, UNC paths, etc.)
	if filepath.IsAbs(cleanName) {
		return "", fmt.Errorf("absolute path not allowed")
	}

	// Reject paths that try to escape with ..
	if strings.HasPrefix(cleanName, "..") || strings.Contains(cleanName, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("path traversal not allowed")
	}

	// Build the target path
	target := filepath.Join(destDir, cleanName)

	// Final check: ensure the target is within destDir
	// This catches any edge cases the above checks might miss
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	absDestDir, err := filepath.Abs(destDir)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absTarget, absDestDir+string(filepath.Separator)) && absTarget != absDestDir {
		return "", fmt.Errorf("path escapes destination directory")
	}

	return target, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Close()
}

// fetchChecksumFromFile downloads a checksums file and extracts the checksum for assetName
func fetchChecksumFromFile(url, assetName string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch checksums: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return extractChecksum(string(body), assetName), nil
}

func extractChecksum(releaseBody, assetName string) string {
	// Look for checksum in release notes or checksums file
	// Format: "checksum  assetname" (standard sha256sum output) or "assetname: checksum"
	lines := strings.Split(releaseBody, "\n")
	// Case-insensitive regex for SHA256 hex (64 chars)
	re := regexp.MustCompile(`(?i)[a-f0-9]{64}`)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, assetName) {
			if match := re.FindString(line); match != "" {
				return strings.ToLower(match) // Normalize to lowercase
			}
		}
	}
	return ""
}

func loadCache() (*cachedCheck, error) {
	cachePath := filepath.Join(GetCacheDir(), cacheFileName)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, err
	}
	var cached cachedCheck
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}
	return &cached, nil
}

func saveCache(version string) {
	cached := cachedCheck{
		CheckedAt: time.Now(),
		Version:   version,
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return
	}
	cachePath := filepath.Join(GetCacheDir(), cacheFileName)
	os.MkdirAll(filepath.Dir(cachePath), 0755)
	os.WriteFile(cachePath, data, 0644)
}

// extractBaseSemver extracts the base semver from a version string.
// Handles formats like:
//   - "0.4.0" -> "0.4.0"
//   - "v0.4.0" -> "0.4.0"
//   - "0.4.0-5-gabcdef" -> "0.4.0" (git describe format)
//   - "0.4.0-dev" -> "0.4.0"
//   - "abc1234" -> "" (no semver)
//   - "dev" -> "" (no semver)
func extractBaseSemver(v string) string {
	v = strings.TrimPrefix(v, "v")
	if len(v) == 0 || v[0] < '0' || v[0] > '9' {
		return ""
	}
	if !strings.Contains(v, ".") {
		return ""
	}
	// Extract up to the first hyphen (for git describe or prerelease tags)
	if idx := strings.Index(v, "-"); idx > 0 {
		v = v[:idx]
	}
	return v
}

// gitDescribePattern matches git describe format: v0.16.1-2-gabcdef or v0.16.1-2-gabcdef-dirty
// The -N-gHASH suffix indicates N commits after the tag, with optional -dirty suffix
var gitDescribePattern = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$`)

// isDevBuildVersion returns true if the version is a dev build.
// Dev builds are either:
// - Pure hashes with no semver (e.g., "9c2baf2", "dev")
// - Git describe format with commits after tag (e.g., "v0.16.1-2-gabcdef")
func isDevBuildVersion(v string) bool {
	v = strings.TrimPrefix(v, "v")
	// Pure hash or "dev" - no semver base
	if extractBaseSemver(v) == "" {
		return true
	}
	// Git describe format: has commits after a tag
	return gitDescribePattern.MatchString(v)
}

// isNewer returns true if v1 is newer than v2
// Assumes semver format: major.minor.patch
// Handles git describe format (v0.4.0-5-gabcdef) by extracting base version.
// Returns false for pure dev builds (hashes like "9c2baf2") since we can't
// determine their relationship to releases - skip update notifications for these.
func isNewer(v1, v2 string) bool {
	base1 := extractBaseSemver(v1)
	base2 := extractBaseSemver(v2)

	// If current version has no semver base (pure hash dev build), skip update notification
	// We can't determine if they're ahead or behind releases
	if base2 == "" {
		return false
	}
	// If release version has no semver base, something is wrong - not newer
	if base1 == "" {
		return false
	}

	parts1 := strings.Split(base1, ".")
	parts2 := strings.Split(base2, ".")

	for i := 0; i < 3; i++ {
		var n1, n2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &n1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &n2)
		}
		if n1 > n2 {
			return true
		}
		if n1 < n2 {
			return false
		}
	}
	return false
}

// FormatSize formats bytes as human-readable string
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

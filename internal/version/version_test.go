package version

import (
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetVersionFromVCSUsesModuleVersion(t *testing.T) {
	originalReader := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v1.2.3"},
		}, true
	}
	t.Cleanup(func() {
		readBuildInfo = originalReader
	})

	require.Equal(t, "v1.2.3", getVersionFromVCS())
}

func TestGetVersionFromVCSUsesShortRevisionAndDirtySuffix(t *testing.T) {
	originalReader := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{
				{Key: settingRevision, Value: "abcdef1234567890"},
				{Key: settingModified, Value: "true"},
			},
		}, true
	}
	t.Cleanup(func() {
		readBuildInfo = originalReader
	})

	require.Equal(t, "abcdef1-dirty", getVersionFromVCS())
}

func TestGetVersionFromVCSReturnsDefaultWithoutBuildInfo(t *testing.T) {
	originalReader := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return nil, false
	}
	t.Cleanup(func() {
		readBuildInfo = originalReader
	})

	require.Equal(t, defaultVersion, getVersionFromVCS())
}

func TestFullReturnsVersionWhenBuildInfoUnavailable(t *testing.T) {
	originalReader := readBuildInfo
	originalVersion := Version
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return nil, false
	}
	Version = "v9.9.9"
	t.Cleanup(func() {
		readBuildInfo = originalReader
		Version = originalVersion
	})

	require.Equal(t, "v9.9.9", Full())
}

func TestFullAppendsVCSTimeWhenPresent(t *testing.T) {
	originalReader := readBuildInfo
	originalVersion := Version
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{
			Settings: []debug.BuildSetting{
				{Key: settingVCSTime, Value: "2026-03-19T12:34:56Z"},
			},
		}, true
	}
	Version = "abcdef1"
	t.Cleanup(func() {
		readBuildInfo = originalReader
		Version = originalVersion
	})

	require.Equal(t, "abcdef1 2026-03-19T12:34:56Z", Full())
}

package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// MaxUnixPathLen is the platform socket path length limit.
// macOS/BSD: 104, Linux: 108.
var MaxUnixPathLen = func() int {
	if runtime.GOOS == "darwin" {
		return 104
	}
	return 108
}()

// DaemonEndpoint encapsulates the transport type and address for the daemon.
type DaemonEndpoint struct {
	Network string // "tcp" or "unix"
	Address string // "127.0.0.1:7373" or "/tmp/roborev-1000/daemon.sock"
}

// ParseEndpoint parses a server_addr config value into a DaemonEndpoint.
func ParseEndpoint(serverAddr string) (DaemonEndpoint, error) {
	if serverAddr == "" {
		return DaemonEndpoint{Network: "tcp", Address: "127.0.0.1:7373"}, nil
	}

	if after, ok := strings.CutPrefix(serverAddr, "http://"); ok {
		return parseTCPEndpoint(after)
	}

	if strings.HasPrefix(serverAddr, "unix://") {
		return parseUnixEndpoint(serverAddr)
	}

	return parseTCPEndpoint(serverAddr)
}

func parseTCPEndpoint(addr string) (DaemonEndpoint, error) {
	if !isLoopbackAddr(addr) {
		return DaemonEndpoint{}, fmt.Errorf(
			"daemon address %q must use a loopback host (127.0.0.1, localhost, or [::1])", addr)
	}
	return DaemonEndpoint{Network: "tcp", Address: addr}, nil
}

func parseUnixEndpoint(raw string) (DaemonEndpoint, error) {
	path := strings.TrimPrefix(raw, "unix://")

	if path == "" {
		path = DefaultSocketPath()
		// Fall through to validate the auto-generated path too
	}

	if !filepath.IsAbs(path) {
		return DaemonEndpoint{}, fmt.Errorf(
			"unix socket path %q must be absolute", path)
	}

	if strings.ContainsRune(path, 0) {
		return DaemonEndpoint{}, fmt.Errorf(
			"unix socket path contains null byte")
	}

	if len(path) >= MaxUnixPathLen {
		return DaemonEndpoint{}, fmt.Errorf(
			"unix socket path %q (%d bytes) exceeds platform limit of %d bytes",
			path, len(path), MaxUnixPathLen)
	}

	return DaemonEndpoint{Network: "unix", Address: path}, nil
}

// DefaultSocketPath returns the auto-generated socket path under os.TempDir().
func DefaultSocketPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("roborev-%d", os.Getuid()), "daemon.sock")
}

// IsUnix returns true if this endpoint uses a Unix domain socket.
func (e DaemonEndpoint) IsUnix() bool {
	return e.Network == "unix"
}

// BaseURL returns the HTTP base URL for constructing API requests.
func (e DaemonEndpoint) BaseURL() string {
	if e.IsUnix() {
		return "http://localhost"
	}
	return "http://" + e.Address
}

// HTTPClient returns an http.Client configured for this endpoint's transport.
func (e DaemonEndpoint) HTTPClient(timeout time.Duration) *http.Client {
	if e.IsUnix() {
		return &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", e.Address)
				},
				DisableKeepAlives: true,
				Proxy:             nil, // Unix sockets are local; never proxy
			},
		}
	}
	return &http.Client{Timeout: timeout}
}

// Listener creates a net.Listener bound to this endpoint.
func (e DaemonEndpoint) Listener() (net.Listener, error) {
	return net.Listen(e.Network, e.Address)
}

// String returns a human-readable representation for logging.
func (e DaemonEndpoint) String() string {
	return e.Network + ":" + e.Address
}

// ConfigAddr returns a ParseEndpoint-compatible string suitable for
// persisting in config or runtime metadata files.
func (e DaemonEndpoint) ConfigAddr() string {
	if e.IsUnix() {
		return "unix://" + e.Address
	}
	return e.Address
}

// Port returns the TCP port, or 0 for Unix sockets.
func (e DaemonEndpoint) Port() int {
	if e.IsUnix() {
		return 0
	}
	_, portStr, err := net.SplitHostPort(e.Address)
	if err != nil {
		return 0
	}
	port := 0
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	return port
}

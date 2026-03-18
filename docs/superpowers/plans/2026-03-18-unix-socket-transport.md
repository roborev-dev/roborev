# Unix Socket Transport Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Unix domain socket support alongside TCP loopback for the daemon HTTP API, controlled via `server_addr` config.

**Architecture:** Introduce a `DaemonEndpoint` type (`internal/daemon/endpoint.go`) that encapsulates transport (TCP/Unix) and address. Thread it through RuntimeInfo, server, client, probes, CLI, and TUI -- replacing raw `host:port` strings. TCP remains the default.

**Tech Stack:** Go stdlib (`net`, `net/http`, `os`), no new dependencies.

**Spec:** `docs/plans/2026-03-18-unix-socket-transport-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/daemon/endpoint.go` | **New.** `DaemonEndpoint` type, `ParseEndpoint`, `DefaultSocketPath`, `MaxUnixPathLen` |
| `internal/daemon/endpoint_test.go` | **New.** Tests for parsing, validation, listener, HTTP round-trip |
| `internal/daemon/runtime.go` | Add `Network` to `RuntimeInfo`, `Endpoint()` method, change `WriteRuntime`/probe/kill signatures |
| `internal/daemon/runtime_test.go` | Update for new signatures, add compat test |
| `internal/daemon/server.go` | `Start()` uses `DaemonEndpoint`, Unix listener setup, socket cleanup |
| `internal/daemon/server_test.go` | Update `waitForServerReady` tests |
| `internal/daemon/client.go` | `addr` -> `baseURL`, `NewHTTPClient(DaemonEndpoint)` |
| `cmd/roborev/main.go` | `--server` flag default to `""`, add `PersistentPreRunE` |
| `cmd/roborev/daemon_lifecycle.go` | `getDaemonEndpoint()`, remove `probeDaemonServerURL`, update `ensureDaemon`/`startDaemon` |
| `cmd/roborev/daemon_client.go` | Replace raw `http.Client{}` with `getDaemonHTTPClient()` |
| `cmd/roborev/stream.go` | Use endpoint for URL and transport |
| `cmd/roborev/*.go` (16 command files) | Mechanical: `getDaemonAddr()` -> `getDaemonEndpoint().BaseURL()`, raw clients -> `getDaemonHTTPClient()` |
| `cmd/roborev/tui_cmd.go` | Parse `--addr` via `ParseEndpoint`, pass `DaemonEndpoint` to TUI |
| `cmd/roborev/tui/tui.go` | `serverAddr` -> `endpoint`, `newModel(DaemonEndpoint)` |
| `cmd/roborev/tui/api.go` | `m.serverAddr` -> `m.endpoint.BaseURL()` |
| `cmd/roborev/tui/fetch.go` | `tryReconnect` returns `DaemonEndpoint` |
| `cmd/roborev/tui/handlers_msg.go` | `handleReconnectMsg` updates endpoint + client |

---

### Task 1: DaemonEndpoint type and tests

**Files:**
- Create: `internal/daemon/endpoint.go`
- Create: `internal/daemon/endpoint_test.go`

- [ ] **Step 1: Write endpoint_test.go with ParseEndpoint table tests**

```go
package daemon

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		network string
		wantErr string
	}{
		{"empty defaults to TCP", "", "tcp", ""},
		{"bare host:port", "127.0.0.1:7373", "tcp", ""},
		{"ipv6 loopback", "[::1]:7373", "tcp", ""},
		{"localhost", "localhost:7373", "tcp", ""},
		{"http prefix stripped", "http://127.0.0.1:7373", "tcp", ""},
		{"unix auto", "unix://", "unix", ""},
		{"unix explicit path", "unix:///tmp/test.sock", "unix", ""},
		{"non-loopback rejected", "192.168.1.1:7373", "", "loopback"},
		{"relative unix rejected", "unix://relative.sock", "", "absolute"},
		{"http non-loopback rejected", "http://192.168.1.1:7373", "", "loopback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep, err := ParseEndpoint(tt.input)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.network, ep.Network)
			if tt.network == "tcp" {
				assert.NotEmpty(t, ep.Address)
			}
		})
	}
}

func TestParseEndpoint_UnixPathTooLong(t *testing.T) {
	long := "unix:///" + strings.Repeat("a", MaxUnixPathLen)
	_, err := ParseEndpoint(long)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestParseEndpoint_UnixNullByte(t *testing.T) {
	_, err := ParseEndpoint("unix:///tmp/bad\x00.sock")
	require.Error(t, err)
}

func TestDefaultSocketPath(t *testing.T) {
	path := DefaultSocketPath()
	assert.True(t, len(path) < MaxUnixPathLen,
		"default socket path %q (%d bytes) exceeds limit %d", path, len(path), MaxUnixPathLen)
	assert.Contains(t, path, "roborev-")
	assert.True(t, strings.HasSuffix(path, "daemon.sock"))
}

func TestDaemonEndpoint_BaseURL(t *testing.T) {
	assert := assert.New(t)

	tcp := DaemonEndpoint{Network: "tcp", Address: "127.0.0.1:7373"}
	assert.Equal("http://127.0.0.1:7373", tcp.BaseURL())

	unix := DaemonEndpoint{Network: "unix", Address: "/tmp/test.sock"}
	assert.Equal("http://localhost", unix.BaseURL())
}

func TestDaemonEndpoint_String(t *testing.T) {
	assert := assert.New(t)

	tcp := DaemonEndpoint{Network: "tcp", Address: "127.0.0.1:7373"}
	assert.Equal("tcp:127.0.0.1:7373", tcp.String())

	unix := DaemonEndpoint{Network: "unix", Address: "/tmp/test.sock"}
	assert.Equal("unix:/tmp/test.sock", unix.String())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop -c go test ./internal/daemon/ -run TestParseEndpoint -v`
Expected: FAIL (types not defined yet)

- [ ] **Step 3: Write endpoint.go**

```go
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

	// Strip http:// prefix for backwards compatibility with old --server flag format.
	if strings.HasPrefix(serverAddr, "http://") {
		serverAddr = strings.TrimPrefix(serverAddr, "http://")
		return parseTCPEndpoint(serverAddr)
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
		return DaemonEndpoint{Network: "unix", Address: DefaultSocketPath()}, nil
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
// For Unix sockets, the host is "localhost" (ignored by the custom dialer).
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
			},
		}
	}
	return &http.Client{Timeout: timeout}
}

// Listener creates a net.Listener bound to this endpoint.
// For Unix sockets, the caller must ensure the parent directory exists
// and any stale socket file is removed before calling this.
func (e DaemonEndpoint) Listener() (net.Listener, error) {
	return net.Listen(e.Network, e.Address)
}

// String returns a human-readable representation for logging.
func (e DaemonEndpoint) String() string {
	return e.Network + ":" + e.Address
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
	fmt.Sscanf(portStr, "%d", &port)
	return port
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `nix develop -c go test ./internal/daemon/ -run "TestParseEndpoint|TestDefaultSocketPath|TestDaemonEndpoint" -v`
Expected: PASS

- [ ] **Step 5: Add Listener and HTTPClient round-trip tests**

Append to `endpoint_test.go`:

```go
func TestDaemonEndpoint_Listener_TCP(t *testing.T) {
	ep := DaemonEndpoint{Network: "tcp", Address: "127.0.0.1:0"}
	ln, err := ep.Listener()
	require.NoError(t, err)
	defer ln.Close()
	assert.Contains(t, ln.Addr().String(), "127.0.0.1:")
}

func TestDaemonEndpoint_Listener_Unix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets not supported on Windows")
	}
	// Use short path to stay under socket limit
	sockPath := filepath.Join("/tmp", fmt.Sprintf("roborev-test-%d.sock", os.Getpid()))
	t.Cleanup(func() { os.Remove(sockPath) })

	ep := DaemonEndpoint{Network: "unix", Address: sockPath}
	ln, err := ep.Listener()
	require.NoError(t, err)
	defer ln.Close()
	assert.Equal(t, sockPath, ln.Addr().String())
}

func TestDaemonEndpoint_HTTPClient_UnixRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets not supported on Windows")
	}
	sockPath := filepath.Join("/tmp", fmt.Sprintf("roborev-test-%d.sock", os.Getpid()))
	t.Cleanup(func() { os.Remove(sockPath) })

	// Start server on Unix socket
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	defer ln.Close()

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})}
	go srv.Serve(ln)
	defer srv.Close()

	// Connect via DaemonEndpoint
	ep := DaemonEndpoint{Network: "unix", Address: sockPath}
	client := ep.HTTPClient(2 * time.Second)
	resp, err := client.Get(ep.BaseURL() + "/test")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
```

- [ ] **Step 6: Run all endpoint tests**

Run: `nix develop -c go test ./internal/daemon/ -run "TestParseEndpoint|TestDefaultSocketPath|TestDaemonEndpoint" -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/endpoint.go internal/daemon/endpoint_test.go
git commit -m "Add DaemonEndpoint type with ParseEndpoint, Listener, HTTPClient"
```

---

### Task 2: RuntimeInfo changes and WriteRuntime

**Files:**
- Modify: `internal/daemon/runtime.go`
- Modify: `internal/daemon/runtime_test.go`

- [ ] **Step 1: Write test for RuntimeInfo.Endpoint() and backwards compat**

Add to `runtime_test.go`:

```go
func TestRuntimeInfo_Endpoint(t *testing.T) {
	assert := assert.New(t)

	// TCP with explicit network
	info := RuntimeInfo{PID: 1, Addr: "127.0.0.1:7373", Port: 7373, Network: "tcp"}
	ep := info.Endpoint()
	assert.Equal("tcp", ep.Network)
	assert.Equal("127.0.0.1:7373", ep.Address)

	// Unix
	info = RuntimeInfo{PID: 1, Addr: "/tmp/test.sock", Port: 0, Network: "unix"}
	ep = info.Endpoint()
	assert.Equal("unix", ep.Network)
	assert.Equal("/tmp/test.sock", ep.Address)

	// Empty network defaults to TCP (backwards compat)
	info = RuntimeInfo{PID: 1, Addr: "127.0.0.1:7373", Port: 7373, Network: ""}
	ep = info.Endpoint()
	assert.Equal("tcp", ep.Network)
}

func TestRuntimeInfo_BackwardsCompat_NoNetworkField(t *testing.T) {
	// Simulate old JSON without "network" field
	data := []byte(`{"pid": 1234, "addr": "127.0.0.1:7373", "port": 7373, "version": "0.47.0"}`)
	var info RuntimeInfo
	require.NoError(t, json.Unmarshal(data, &info))
	assert.Equal(t, "", info.Network)
	ep := info.Endpoint()
	assert.Equal(t, "tcp", ep.Network)
	assert.Equal(t, "127.0.0.1:7373", ep.Address)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `nix develop -c go test ./internal/daemon/ -run "TestRuntimeInfo_Endpoint|TestRuntimeInfo_BackwardsCompat" -v`
Expected: FAIL (Endpoint method and Network field don't exist)

- [ ] **Step 3: Add Network field and Endpoint() to RuntimeInfo**

In `runtime.go`, update `RuntimeInfo`:

```go
type RuntimeInfo struct {
	PID        int    `json:"pid"`
	Addr       string `json:"addr"`
	Port       int    `json:"port"`
	Network    string `json:"network"` // "tcp" or "unix"; empty treated as "tcp" on read
	Version    string `json:"version"`
	SourcePath string `json:"-"`
}

func (r *RuntimeInfo) Endpoint() DaemonEndpoint {
	network := r.Network
	if network == "" {
		network = "tcp"
	}
	return DaemonEndpoint{Network: network, Address: r.Addr}
}
```

- [ ] **Step 4: Update WriteRuntime signature**

Change `WriteRuntime` to accept `DaemonEndpoint`:

```go
func WriteRuntime(ep DaemonEndpoint, version string) error {
	info := RuntimeInfo{
		PID:     os.Getpid(),
		Addr:    ep.Address,
		Port:    ep.Port(),
		Network: ep.Network,
		Version: version,
	}
	// ... rest unchanged
}
```

- [ ] **Step 5: Update the WriteRuntime call in server.go**

In `server.go:203`, change:

```go
// Before:
if err := WriteRuntime(addr, port, version.Version); err != nil {

// After:
if err := WriteRuntime(ep, version.Version); err != nil {
```

(The `ep` variable will be fully wired in Task 4. For now, construct it inline to keep the build green.)

Temporary in `server.go:203`:

```go
if err := WriteRuntime(DaemonEndpoint{Network: "tcp", Address: addr}, version.Version); err != nil {
```

- [ ] **Step 6: Fix any other callers of WriteRuntime in the codebase**

Run: `nix develop -c go build ./...`
Expected: PASS (or fix compile errors from signature change)

- [ ] **Step 7: Run tests**

Run: `nix develop -c go test ./internal/daemon/ -run "TestRuntimeInfo" -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/runtime.go internal/daemon/runtime_test.go internal/daemon/server.go
git commit -m "Add Network field to RuntimeInfo, Endpoint() method, update WriteRuntime signature"
```

---

### Task 3: Update probe and kill functions to use DaemonEndpoint

**Files:**
- Modify: `internal/daemon/runtime.go`
- Modify: `internal/daemon/runtime_test.go`

- [ ] **Step 1: Change ProbeDaemon signature**

```go
// Before:
func ProbeDaemon(addr string, timeout time.Duration) (*PingInfo, error)

// After:
func ProbeDaemon(ep DaemonEndpoint, timeout time.Duration) (*PingInfo, error) {
	if ep.Address == "" {
		return nil, fmt.Errorf("empty daemon address")
	}

	// TCP endpoints must be loopback (validated at parse time, but defense in depth)
	if !ep.IsUnix() && !isLoopbackAddr(ep.Address) {
		return nil, fmt.Errorf("non-loopback daemon address: %s", ep.Address)
	}

	client := ep.HTTPClient(timeout)
	baseURL := ep.BaseURL()
	if info, shouldFallback, err := probeDaemonPing(client, baseURL); !shouldFallback {
		return info, err
	}

	return probeLegacyDaemonStatus(client, baseURL)
}
```

- [ ] **Step 2: Update probeDaemonPing and probeLegacyDaemonStatus to use baseURL**

```go
// Before:
func probeDaemonPing(client *http.Client, addr string) (*PingInfo, bool, error) {
	resp, err := client.Get(fmt.Sprintf("http://%s/api/ping", addr))

// After:
func probeDaemonPing(client *http.Client, baseURL string) (*PingInfo, bool, error) {
	resp, err := client.Get(baseURL + "/api/ping")
```

Same pattern for `probeLegacyDaemonStatus`.

- [ ] **Step 3: Change IsDaemonAlive signature**

```go
// Before:
func IsDaemonAlive(addr string) bool

// After:
func IsDaemonAlive(ep DaemonEndpoint) bool {
	if ep.Address == "" {
		return false
	}
	for attempt := range 2 {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if _, err := ProbeDaemon(ep, 1*time.Second); err == nil {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Update all callers of IsDaemonAlive and ProbeDaemon**

In `runtime.go`:
- `GetAnyRunningDaemon()`: `IsDaemonAlive(info.Addr)` -> `IsDaemonAlive(info.Endpoint())`
- `KillDaemon()`: Use `info.Endpoint()` for both HTTP shutdown and alive checks
- `CleanupZombieDaemons()`: `IsDaemonAlive(info.Addr)` -> `IsDaemonAlive(info.Endpoint())`. For Unix socket endpoints, also add PID-based liveness check: if the PID from RuntimeInfo is dead, skip the HTTP probe and go straight to cleanup (removes socket file + runtime file). This avoids slow timeouts on stale Unix sockets where the process has crashed.

```go
// In CleanupZombieDaemons, before the IsDaemonAlive call:
ep := info.Endpoint()
if ep.IsUnix() && info.PID > 0 && !isProcessAlive(info.PID) {
	// Process is dead -- remove stale socket and runtime file directly
	os.Remove(ep.Address)
	if info.SourcePath != "" {
		os.Remove(info.SourcePath)
	} else {
		RemoveRuntimeForPID(info.PID)
	}
	cleaned++
	continue
}
if IsDaemonAlive(ep) {
	continue
}
```

In `server.go`:
- `Start()` line 141: `IsDaemonAlive(info.Addr)` -> `IsDaemonAlive(info.Endpoint())`

- [ ] **Step 5: Update KillDaemon to use endpoint for HTTP shutdown**

```go
func KillDaemon(info *RuntimeInfo) bool {
	if info == nil {
		return true
	}

	removeRuntimeFile := func() {
		if info.SourcePath != "" {
			os.Remove(info.SourcePath)
		} else if info.PID > 0 {
			RemoveRuntimeForPID(info.PID)
		}
		// Clean up Unix socket file if applicable
		ep := info.Endpoint()
		if ep.IsUnix() {
			os.Remove(ep.Address)
		}
	}

	ep := info.Endpoint()

	// Try graceful HTTP shutdown
	if ep.Address != "" {
		client := ep.HTTPClient(2 * time.Second)
		resp, err := client.Post(ep.BaseURL()+"/api/shutdown", "application/json", nil)
		if err == nil {
			resp.Body.Close()
			for range 10 {
				time.Sleep(200 * time.Millisecond)
				if !IsDaemonAlive(ep) {
					removeRuntimeFile()
					return true
				}
			}
		}
	}

	// ... rest unchanged (OS kill, PID check)
}
```

- [ ] **Step 6: Update waitForServerReady signature**

```go
// Before:
func waitForServerReady(ctx context.Context, addr string, timeout time.Duration, serveErrCh <-chan error) (bool, bool, error)

// After:
func waitForServerReady(ctx context.Context, ep DaemonEndpoint, timeout time.Duration, serveErrCh <-chan error) (bool, bool, error)
```

Update the `ProbeDaemon` call inside:
```go
// Before:
if _, err := ProbeDaemon(addr, 200*time.Millisecond); err == nil {
// After:
if _, err := ProbeDaemon(ep, 200*time.Millisecond); err == nil {
```

Update caller in `server.go` `Start()`:
```go
// Before:
ready, serveExited, err := waitForServerReady(ctx, addr, 2*time.Second, serveErrCh)
// After:
ready, serveExited, err := waitForServerReady(ctx, DaemonEndpoint{Network: "tcp", Address: addr}, 2*time.Second, serveErrCh)
```

(This temporary construction will be replaced in Task 4 when `Start()` gets the full endpoint flow.)

- [ ] **Step 7: Build and run all daemon tests**

Run: `nix develop -c go build ./... && nix develop -c go test ./internal/daemon/ -v -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/runtime.go internal/daemon/runtime_test.go internal/daemon/server.go internal/daemon/server_test.go
git commit -m "Update probe, kill, and alive-check functions to use DaemonEndpoint"
```

---

### Task 4: Server Start() with full endpoint flow

**Files:**
- Modify: `internal/daemon/server.go`
- Modify: `internal/daemon/server_test.go`

- [ ] **Step 1: Rewrite Start() to use ParseEndpoint**

Replace the validation + port discovery + listener block in `Start()`:

```go
func (s *Server) Start(ctx context.Context) error {
	cfg := s.configWatcher.Config()

	ep, err := ParseEndpoint(cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("invalid server address: %w", err)
	}

	// Clean up any zombie daemons first
	if cleaned := CleanupZombieDaemons(); cleaned > 0 {
		log.Printf("Cleaned up %d zombie daemon(s)", cleaned)
		// ... activity log unchanged
	}

	// Check if a responsive daemon is still running after cleanup
	if info, err := GetAnyRunningDaemon(); err == nil && IsDaemonAlive(info.Endpoint()) {
		return fmt.Errorf("daemon already running (pid %d on %s)", info.PID, info.Addr)
	}

	// Reset stale jobs
	if err := s.db.ResetStaleJobs(); err != nil {
		log.Printf("Warning: failed to reset stale jobs: %v", err)
	}

	// Start config watcher
	if err := s.configWatcher.Start(ctx); err != nil {
		log.Printf("Warning: failed to start config watcher: %v", err)
	}

	// Prepare listener
	if ep.IsUnix() {
		// Create parent dir with owner-only permissions
		dir := filepath.Dir(ep.Address)
		if err := os.MkdirAll(dir, 0700); err != nil {
			s.configWatcher.Stop()
			return fmt.Errorf("create socket directory %s: %w", dir, err)
		}
		// Remove stale socket
		os.Remove(ep.Address)
	} else {
		// TCP: find available port
		// Use the parsed endpoint address (not cfg.ServerAddr which may have http:// prefix)
		addr, _, err := FindAvailablePort(ep.Address)
		if err != nil {
			s.configWatcher.Stop()
			return fmt.Errorf("find available port: %w", err)
		}
		ep = DaemonEndpoint{Network: "tcp", Address: addr}
	}

	listener, err := ep.Listener()
	if err != nil {
		s.configWatcher.Stop()
		return fmt.Errorf("listen on %s: %w", ep, err)
	}

	// Update endpoint with actual bound address (port may differ for TCP)
	if !ep.IsUnix() {
		actualAddr := listener.Addr().String()
		ep = DaemonEndpoint{Network: "tcp", Address: actualAddr}
	}

	// Set socket permissions
	if ep.IsUnix() {
		if err := os.Chmod(ep.Address, 0600); err != nil {
			listener.Close()
			s.configWatcher.Stop()
			return fmt.Errorf("chmod socket %s: %w", ep.Address, err)
		}
	}

	s.httpServer.Addr = ep.Address
	s.endpoint = ep // Store for cleanup

	serveErrCh := make(chan error, 1)
	log.Printf("Starting HTTP server on %s", ep)
	go func() {
		serveErrCh <- s.httpServer.Serve(listener)
	}()

	s.workerPool.Start()

	ready, serveExited, err := waitForServerReady(ctx, ep, 2*time.Second, serveErrCh)
	if err != nil {
		_ = listener.Close()
		s.configWatcher.Stop()
		s.workerPool.Stop()
		return err
	}
	if !ready {
		if err := awaitServeExitOnUnreadyStartup(serveExited, serveErrCh); err != nil {
			s.configWatcher.Stop()
			s.workerPool.Stop()
			return err
		}
		return nil
	}

	if err := WriteRuntime(ep, version.Version); err != nil {
		log.Printf("Warning: failed to write runtime info: %v", err)
	}

	// ... rest unchanged (activity log, hook check, serveErrCh wait)
}
```

- [ ] **Step 2: Add `endpoint` field to Server struct**

Find the Server struct definition and add:

```go
endpoint DaemonEndpoint // Stored for cleanup on stop
```

- [ ] **Step 3: Add socket cleanup to server shutdown**

Find the shutdown/stop method and add Unix socket removal:

```go
// In the shutdown path:
if s.endpoint.IsUnix() {
	os.Remove(s.endpoint.Address)
}
```

- [ ] **Step 4: Remove validateDaemonBindAddr calls**

Delete or leave as unused (will be removed once all callers are gone). The validation is now inside `ParseEndpoint`.

- [ ] **Step 5: Build and run tests**

Run: `nix develop -c go build ./... && nix develop -c go test ./internal/daemon/ -v -count=1`
Expected: PASS

- [ ] **Step 6: Run go vet and fmt**

Run: `nix develop -c go fmt ./... && nix develop -c go vet ./...`

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/server.go internal/daemon/runtime.go
git commit -m "Wire DaemonEndpoint through Server.Start(), add Unix socket setup and cleanup"
```

---

### Task 5: Update daemon client

**Files:**
- Modify: `internal/daemon/client.go`

- [ ] **Step 1: Change HTTPClient struct and NewHTTPClient**

```go
type HTTPClient struct {
	baseURL      string
	httpClient   *http.Client
	pollInterval time.Duration
}

func NewHTTPClient(ep DaemonEndpoint) *HTTPClient {
	return &HTTPClient{
		baseURL:      ep.BaseURL(),
		httpClient:   ep.HTTPClient(10 * time.Second),
		pollInterval: DefaultPollInterval,
	}
}
```

- [ ] **Step 2: Rename c.addr to c.baseURL throughout client.go**

Find-and-replace `c.addr` -> `c.baseURL` in all method bodies. This is a pure rename -- no logic changes.

- [ ] **Step 3: Update NewHTTPClientFromRuntime**

```go
func NewHTTPClientFromRuntime() (*HTTPClient, error) {
	var lastErr error
	for range 5 {
		info, err := GetAnyRunningDaemon()
		if err == nil {
			return NewHTTPClient(info.Endpoint()), nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon not running: %w", lastErr)
}
```

- [ ] **Step 4: Build and test**

Run: `nix develop -c go build ./... && nix develop -c go test ./internal/daemon/ -v -count=1`
Expected: PASS (or fix compile errors from callers in cmd/)

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/client.go
git commit -m "Update daemon HTTPClient to use DaemonEndpoint, rename addr to baseURL"
```

---

### Task 6: Migrate all CLI command files to DaemonEndpoint

All CLI changes are done in a single task to keep the build green. Removing `getDaemonAddr()` and adding `getDaemonEndpoint()` must happen atomically across all callers.

**Files:**
- Modify: `cmd/roborev/main.go`
- Modify: `cmd/roborev/daemon_lifecycle.go`
- Modify: `cmd/roborev/daemon_client.go`
- Modify: `cmd/roborev/analyze.go`, `compact.go`, `review.go`, `run.go`, `fix.go`, `list.go`, `summary.go`, `status.go`, `show.go`, `wait.go`, `comment.go`, `sync.go`, `remap.go`, `postcommit.go`, `refine.go`, `job_helpers.go`, `stream.go`

- [ ] **Step 1: Run grep to inventory all callsites**

Run: `nix develop -c grep -rn 'getDaemonAddr\|serverAddr.*api\|http\.Client{Timeout\|http\.Post(' cmd/roborev/*.go | grep -v _test.go`

This identifies every callsite that needs updating.

- [ ] **Step 2: Update main.go global variable and flag**

```go
var (
	serverAddr string // Raw flag value, parsed via ParseEndpoint
	verbose    bool
)

// In main():
rootCmd.PersistentFlags().StringVar(&serverAddr, "server", "", "daemon server address (e.g. 127.0.0.1:7373 or unix://)")
```

- [ ] **Step 3: Replace getDaemonAddr with getDaemonEndpoint and getDaemonHTTPClient**

In `daemon_lifecycle.go`:

```go
func getDaemonEndpoint() daemon.DaemonEndpoint {
	if info, err := daemon.GetAnyRunningDaemon(); err == nil {
		return info.Endpoint()
	}
	ep, err := daemon.ParseEndpoint(serverAddr)
	if err != nil {
		return daemon.DaemonEndpoint{Network: "tcp", Address: "127.0.0.1:7373"}
	}
	return ep
}

func getDaemonHTTPClient(timeout time.Duration) *http.Client {
	return getDaemonEndpoint().HTTPClient(timeout)
}
```

- [ ] **Step 4: Update ensureDaemon**

Replace `serverAddr = fmt.Sprintf("http://%s", info.Addr)` references. Replace `probeDaemonServerURL` call with direct `ProbeDaemon(ep, timeout)`.

```go
func ensureDaemon() error {
	skipVersionCheck := os.Getenv("ROBOREV_SKIP_VERSION_CHECK") == "1"

	if info, err := getAnyRunningDaemon(); err == nil {
		if !skipVersionCheck {
			probe, err := daemon.ProbeDaemon(info.Endpoint(), 2*time.Second)
			if err != nil {
				if verbose {
					fmt.Printf("Daemon probe failed, restarting...\n")
				}
				return restartDaemonForEnsure()
			}
			// ... version check unchanged
		}
		return nil
	}

	// Try the configured address
	ep := getDaemonEndpoint()
	if probe, err := daemon.ProbeDaemon(ep, 2*time.Second); err == nil {
		if !skipVersionCheck {
			// ... version check unchanged
		}
		return nil
	}

	return startDaemon()
}
```

- [ ] **Step 5: Update startDaemon**

Remove `serverAddr = fmt.Sprintf("http://%s", info.Addr)`:

```go
func startDaemon() error {
	// ... exec unchanged ...
	for range 30 {
		time.Sleep(100 * time.Millisecond)
		if _, err := daemon.GetAnyRunningDaemon(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("daemon failed to start")
}
```

- [ ] **Step 6: Remove probeDaemonServerURL and getDaemonAddr**

Delete both functions entirely.

- [ ] **Step 7: Update daemon_lifecycle.go registerRepo**

```go
func registerRepo(repoPath string) error {
	body, err := json.Marshal(map[string]string{"repo_path": repoPath})
	if err != nil {
		return err
	}
	client := getDaemonHTTPClient(5 * time.Second)
	resp, err := client.Post(getDaemonEndpoint().BaseURL()+"/api/repos/register", "application/json", bytes.NewReader(body))
	// ... rest unchanged
}
```

- [ ] **Step 8: Update daemon_client.go**

`waitForJob` and `showReview` take `serverAddr string` as a parameter. Change callers to pass `getDaemonEndpoint().BaseURL()`. Inside these functions, replace `&http.Client{Timeout: N}` with `getDaemonHTTPClient(N)`. All 5 locally-constructed clients and the bare `http.Post` call are replaced.

- [ ] **Step 9: Apply mechanical replacements across all remaining CLI files**

For each file (`analyze.go`, `compact.go`, `review.go`, `run.go`, `fix.go`, `list.go`, `summary.go`, `status.go`, `show.go`, `wait.go`, `comment.go`, `sync.go`, `remap.go`, `postcommit.go`, `refine.go`, `job_helpers.go`, `stream.go`):

1. Replace `getDaemonAddr()` with `getDaemonEndpoint().BaseURL()`
2. Replace `&http.Client{Timeout: N}` with `getDaemonHTTPClient(N)`
3. Replace bare `http.Post(serverAddr+...)` with `getDaemonHTTPClient(N).Post(getDaemonEndpoint().BaseURL()+...)` (affects `review.go`, `run.go`, `postcommit.go`)
4. For `postcommit.go`: replace `var hookHTTPClient = &http.Client{Timeout: 3 * time.Second}` with `func getHookHTTPClient() *http.Client { return getDaemonHTTPClient(3 * time.Second) }` and update callers. Also replace the bare `serverAddr+"/api/enqueue"` on line 81 with `getDaemonEndpoint().BaseURL()+"/api/enqueue"`.
5. For `stream.go`: use `getDaemonEndpoint()` for both the URL and the HTTP client transport (streaming client needs `Timeout: 0`).

- [ ] **Step 10: Build to verify all compile errors are resolved**

Run: `nix develop -c go build ./...`
Expected: PASS

- [ ] **Step 11: Run go fmt and go vet**

Run: `nix develop -c go fmt ./... && nix develop -c go vet ./...`

- [ ] **Step 12: Commit**

```bash
git add cmd/roborev/*.go
git commit -m "Migrate all CLI command files to use DaemonEndpoint"
```

---

### Task 7: TUI changes

**Files:**
- Modify: `cmd/roborev/tui_cmd.go`
- Modify: `cmd/roborev/tui/tui.go`
- Modify: `cmd/roborev/tui/api.go`
- Modify: `cmd/roborev/tui/fetch.go`
- Modify: `cmd/roborev/tui/handlers_msg.go`

- [ ] **Step 1: Update tui/tui.go model struct and newModel**

```go
// In model struct:
endpoint         daemon.DaemonEndpoint // replaces serverAddr string

// newModel signature:
func newModel(ep daemon.DaemonEndpoint, opts ...option) model {
	// ...
	m := model{
		endpoint: ep,
		client:   ep.HTTPClient(10 * time.Second),
		// ...
	}
}
```

- [ ] **Step 2: Update tui/api.go**

```go
func (m model) getJSON(path string, out any) error {
	url := m.endpoint.BaseURL() + path
	// ... rest unchanged
}

func (m model) postJSON(path string, in any, out any) error {
	// ...
	resp, err := m.client.Post(m.endpoint.BaseURL()+path, "application/json", bytes.NewReader(body))
	// ... rest unchanged
}
```

- [ ] **Step 3: Update tui/fetch.go tryReconnect**

Find `tryReconnect` and update the reconnect message to carry a `DaemonEndpoint`:

```go
// In reconnectMsg struct:
type reconnectMsg struct {
	endpoint daemon.DaemonEndpoint
}

// In tryReconnect:
func tryReconnect() tea.Msg {
	info, err := daemon.GetAnyRunningDaemon()
	if err != nil {
		return reconnectMsg{}
	}
	return reconnectMsg{endpoint: info.Endpoint()}
}
```

- [ ] **Step 4: Update tui/handlers_msg.go handleReconnectMsg**

```go
// Update m.endpoint and m.client on successful reconnect:
m.endpoint = msg.endpoint
m.client = msg.endpoint.HTTPClient(10 * time.Second)
```

- [ ] **Step 5: Update tui/fetch.go for all m.serverAddr references**

Replace all `m.serverAddr` with `m.endpoint.BaseURL()` in URL constructions. **Important:** Several functions (`fetchRepoNames`, `fetchRepos`, `backfillBranches`, `fetchBranchesForRepo`) capture `serverAddr` into a local variable before a closure (e.g., `serverAddr := m.serverAddr`). These closures must be updated to capture `m.endpoint.BaseURL()` into the local variable instead.

- [ ] **Step 6: Update tui_cmd.go and tui.Config**

The `tui.Config` struct has a `ServerAddr string` field passed to `tui.Run()` which calls `newModel`. Update the flow:

In `tui_cmd.go`: Parse `--addr` via `ParseEndpoint` before calling `tui.Run()`. Change `Config.ServerAddr string` to `Config.Endpoint daemon.DaemonEndpoint`. Remove the old `http://` prefix logic.

```go
// In tuiCmd RunE:
ep, err := daemon.ParseEndpoint(addr)
if err != nil {
	return fmt.Errorf("invalid address: %w", err)
}
cfg := tui.Config{
	Endpoint: ep,
	// ...
}
return tui.Run(cfg)
```

In `tui/tui.go` `Run()`: pass `cfg.Endpoint` to `newModel(cfg.Endpoint, ...)`.

- [ ] **Step 7: Build and test**

Run: `nix develop -c go build ./... && nix develop -c go test ./cmd/roborev/tui/ -v -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add cmd/roborev/tui_cmd.go cmd/roborev/tui/*.go
git commit -m "Update TUI to use DaemonEndpoint for transport-agnostic daemon communication"
```

---

### Task 8: Update tests

**Files:**
- Modify: `internal/daemon/runtime_test.go`
- Modify: `internal/daemon/server_test.go`
- Modify: `cmd/roborev/helpers_test.go`
- Modify: `cmd/roborev/main_test_helpers_test.go`
- Modify: Various `cmd/roborev/*_test.go`

- [ ] **Step 1: Update patchServerAddr in helpers_test.go**

The test helper that patches `serverAddr` needs to work with the new system. Since the CLI files now call `getDaemonEndpoint()` which falls back to `ParseEndpoint(serverAddr)`, patching `serverAddr` with a test server URL (`http://127.0.0.1:PORT`) should still work -- `ParseEndpoint` strips the `http://` prefix.

Verify `patchServerAddr` still works or update it:

```go
func patchServerAddr(t *testing.T, newURL string) {
	old := serverAddr
	serverAddr = newURL
	t.Cleanup(func() { serverAddr = old })
}
```

This should work as-is since `ParseEndpoint("http://127.0.0.1:PORT")` strips the prefix.

- [ ] **Step 2: Update runtime_test.go for Network field**

Add `Network` to any `RuntimeInfo` or `runtimeData` literals in tests. Verify existing test assertions about JSON format include the new field.

- [ ] **Step 3: Update server_test.go for waitForServerReady signature**

Find any direct calls to `waitForServerReady` in tests and pass a `DaemonEndpoint` instead of a string.

- [ ] **Step 4: Update main_test_helpers_test.go**

Add `Network` field to `daemon.RuntimeInfo{Addr: mockAddr, ...}` literals.

- [ ] **Step 5: Build and run full test suite**

Run: `nix develop -c go build ./... && nix develop -c go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Run go fmt and go vet**

Run: `nix develop -c go fmt ./... && nix develop -c go vet ./...`

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "Update all tests for DaemonEndpoint migration"
```

---

### Task 9: Clean up dead code

**Files:**
- Modify: `internal/daemon/runtime.go`
- Modify: `internal/daemon/server.go`

- [ ] **Step 1: Remove validateDaemonBindAddr and parseDaemonBindAddr if unused**

Check if anything still calls them:

Run: `nix develop -c grep -rn 'validateDaemonBindAddr\|parseDaemonBindAddr' internal/ cmd/`

If no callers remain, delete both functions.

- [ ] **Step 2: Check for unused getDaemonAddr**

Run: `nix develop -c grep -rn 'getDaemonAddr' cmd/`

If no callers remain, delete.

- [ ] **Step 3: Check for unused probeDaemonServerURL**

Should already be deleted in Task 6, verify.

- [ ] **Step 4: Build, vet, fmt**

Run: `nix develop -c go build ./... && nix develop -c go vet ./... && nix develop -c go fmt ./...`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `nix develop -c go test ./... -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "Remove dead code: validateDaemonBindAddr, parseDaemonBindAddr, getDaemonAddr"
```

Note: `isLoopbackAddr` is still used by `ProbeDaemon` for TCP defense-in-depth and by `ParseEndpoint` via `parseTCPEndpoint`. Keep it.

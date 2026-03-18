# Unix Socket Transport for Daemon Communication

## Motivation

The roborev daemon currently only listens on TCP loopback addresses (127.0.0.1, [::1], localhost). This is enforced throughout the codebase as an SSRF mitigation since there is no authentication or TLS on the HTTP API.

Unix domain sockets provide two advantages over TCP loopback:

1. **Security hardening** -- filesystem permissions (owner-only `0600` on the socket, `0700` on the parent directory) give per-user access control without needing TLS/mTLS. Other local users cannot connect.
2. **Container isolation** -- NixOS containers with network isolation can share the daemon via a bind-mounted socket path, without exposing a TCP port or adding HTTPS/mTLS.

## Design Summary

Introduce a `DaemonEndpoint` type that encapsulates the transport (TCP or Unix) and address. Thread it through RuntimeInfo, the server, the client, probe functions, and CLI commands -- replacing raw `host:port` strings. TCP loopback remains the default. Users opt in to Unix sockets via config.

## Config Surface

The existing `server_addr` config key determines the transport based on its value:

| Value | Transport | Address |
|-------|-----------|---------|
| `""` (empty/default) | TCP | `127.0.0.1:7373` |
| `"127.0.0.1:7373"` | TCP | As specified |
| `"[::1]:7373"` | TCP | As specified |
| `"unix://"` | Unix | Auto-generated: `os.TempDir()/roborev-<uid>/daemon.sock` |
| `"unix:///explicit/path.sock"` | Unix | Explicit absolute path |

Relative paths (`unix://relative.sock`) are rejected. Paths exceeding the platform socket limit (104 bytes on macOS, 108 on Linux) are rejected at parse time.

The `--server` CLI flag accepts the same values and takes precedence over the config file.

## DaemonEndpoint Type

New file: `internal/daemon/endpoint.go`

```go
type DaemonEndpoint struct {
    Network string // "tcp" or "unix"
    Address string // "127.0.0.1:7373" or "/tmp/roborev-1000/daemon.sock"
}
```

### Construction

```go
func ParseEndpoint(serverAddr string) (DaemonEndpoint, error)
```

Parses a `server_addr` config value:
- Empty string returns TCP default `127.0.0.1:7373`
- Bare `host:port` returns TCP with loopback validation
- `unix://` returns Unix with `DefaultSocketPath()`
- `unix:///absolute/path` returns Unix with the given path
- Anything else (relative path, non-loopback TCP) returns an error

```go
func DefaultSocketPath() string
```

Returns `filepath.Join(os.TempDir(), fmt.Sprintf("roborev-%d", os.Getuid()), "daemon.sock")`. Example paths:
- Linux: `/tmp/roborev-1000/daemon.sock` (~31 chars)
- macOS: `/var/folders/zz/.../T/roborev-501/daemon.sock` (~73 chars)

Both are well within the platform socket path limits.

### Methods

```go
func (e DaemonEndpoint) Listener() (net.Listener, error)
```

Calls `net.Listen(e.Network, e.Address)`. For Unix, the caller must ensure the parent directory exists and any stale socket is removed before calling this.

```go
func (e DaemonEndpoint) HTTPClient(timeout time.Duration) *http.Client
```

For TCP: standard `http.Client` with the given timeout.
For Unix: `http.Client` with a custom transport:

```go
&http.Transport{
    DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
        return net.DialContext(ctx, "unix", e.Address)
    },
}
```

```go
func (e DaemonEndpoint) BaseURL() string
```

TCP: `"http://<host>:<port>"`. Unix: `"http://localhost"` (host is ignored by the Unix dialer, but `net/http` requires a valid URL).

```go
func (e DaemonEndpoint) IsUnix() bool
func (e DaemonEndpoint) String() string // For logging: "tcp:127.0.0.1:7373" or "unix:/tmp/..."
```

### Validation

Performed inside `ParseEndpoint`:
- TCP: loopback check (same `isLoopbackAddr` logic as today)
- Unix: must be absolute path, must not exceed platform socket path limit, must not contain null bytes

## RuntimeInfo Changes

Current JSON format:

```json
{"pid": 1234, "addr": "127.0.0.1:7373", "port": 7373, "version": "0.47.0"}
```

New format adds a `network` field:

```json
{"pid": 1234, "addr": "/tmp/roborev-1000/daemon.sock", "port": 0, "network": "unix", "version": "0.47.0"}
```

Struct changes:

```go
type RuntimeInfo struct {
    PID        int    `json:"pid"`
    Addr       string `json:"addr"`
    Port       int    `json:"port"`       // 0 for unix
    Network    string `json:"network"`    // "tcp" or "unix"; empty treated as "tcp"
    Version    string `json:"version"`
    SourcePath string `json:"-"`
}
```

**Backwards compatibility:**
- Reading: missing `network` field defaults to `"tcp"`. Old daemon.json files work as before.
- Writing: always includes `network`.
- An old CLI reading a Unix runtime file will fail to probe (it will try `http:///tmp/roborev-1000/daemon.sock/api/ping`), which fails gracefully -- it reports "daemon not running" rather than crashing.

New helper:

```go
func (r *RuntimeInfo) Endpoint() DaemonEndpoint
```

Returns a `DaemonEndpoint` derived from the runtime info, defaulting `Network` to `"tcp"` if empty.

`WriteRuntime` signature changes:

```go
// Before:
func WriteRuntime(addr string, port int, version string) error

// After:
func WriteRuntime(ep DaemonEndpoint, version string) error
```

## Server Changes

`server.go` `Start()` flow:

1. `ParseEndpoint(cfg.ServerAddr)` -- parse and validate config
2. For TCP: `FindAvailablePort` scans for open port (unchanged)
3. For Unix: create parent directory with `0700`, remove stale socket file if present
4. `ep.Listener()` -- bind
5. For Unix: `os.Chmod(socketPath, 0600)` after bind
6. `WriteRuntime(ep, version)` -- persist with network field

`FindAvailablePort` remains TCP-only. Not called for Unix endpoints.

`validateDaemonBindAddr` is replaced by validation inside `ParseEndpoint`.

**Shutdown cleanup:** `Server.Stop()` removes the socket file for Unix endpoints. `CleanupZombieDaemons` also removes stale socket files when the owning process is dead.

## Client Changes

`HTTPClient` struct:

```go
// Before:
type HTTPClient struct {
    addr         string
    httpClient   *http.Client
    pollInterval time.Duration
}

// After:
type HTTPClient struct {
    baseURL      string
    httpClient   *http.Client
    pollInterval time.Duration
}
```

`NewHTTPClient` takes a `DaemonEndpoint`:

```go
func NewHTTPClient(ep DaemonEndpoint) *HTTPClient {
    return &HTTPClient{
        baseURL:      ep.BaseURL(),
        httpClient:   ep.HTTPClient(10 * time.Second),
        pollInterval: DefaultPollInterval,
    }
}
```

All endpoint methods change `c.addr` to `c.baseURL` -- pure rename, no logic changes.

`NewHTTPClientFromRuntime`:

```go
func NewHTTPClientFromRuntime() (*HTTPClient, error) {
    // ... retry loop ...
    info, err := GetAnyRunningDaemon()
    if err == nil {
        return NewHTTPClient(info.Endpoint()), nil
    }
    // ...
}
```

## Probe Function Changes

`ProbeDaemon`, `IsDaemonAlive`, `probeDaemonPing`, `probeLegacyDaemonStatus` change from `addr string` to `DaemonEndpoint`:

```go
func ProbeDaemon(ep DaemonEndpoint, timeout time.Duration) (*PingInfo, error)
func IsDaemonAlive(ep DaemonEndpoint) bool
```

TCP probes still validate loopback (via the endpoint's validation at parse time). Unix probes skip loopback checks (filesystem permissions handle access control).

URL construction uses `ep.BaseURL() + "/api/ping"` instead of `fmt.Sprintf("http://%s/api/ping", addr)`.

HTTP clients use `ep.HTTPClient(timeout)` to get the right transport.

`KillDaemon` gets the endpoint from `info.Endpoint()` and uses it for the shutdown POST. Drops the inline `isLoopbackAddr` guard.

## CLI Changes

**`cmd/roborev/main.go`:**

`--server` flag default changes from `"http://127.0.0.1:7373"` to `""`. Empty means "use config file, which defaults to TCP `127.0.0.1:7373`". This prevents the flag from always overriding config.

**`cmd/roborev/daemon_lifecycle.go`:**

`getDaemonAddr() string` becomes `getDaemonEndpoint() DaemonEndpoint`:

```go
func getDaemonEndpoint() DaemonEndpoint {
    if info, err := daemon.GetAnyRunningDaemon(); err == nil {
        return info.Endpoint()
    }
    ep, _ := daemon.ParseEndpoint(serverAddr)
    return ep
}
```

`ensureDaemon()` and `startDaemon()` set a `DaemonEndpoint` instead of a `serverAddr` string.

**`cmd/roborev/daemon_client.go`:**

`waitForJob` and `showReview` use `getDaemonEndpoint().BaseURL()`.

**`cmd/roborev/stream.go`:**

Uses `getDaemonEndpoint()` for both the stream URL and the HTTP client (to get the right transport).

## TUI Changes

`tui/tui.go` model struct: `serverAddr string` becomes `endpoint DaemonEndpoint`.

`tui/api.go`: `m.serverAddr + path` becomes `m.endpoint.BaseURL() + path`. The `http.Client` on the model is created via `endpoint.HTTPClient()`.

`tui_cmd.go`: passes `DaemonEndpoint` to `newModel` instead of a URL string.

## Testing

### Existing test impact

- **`httptest.Server` tests** (mock daemon, CLI): `patchServerAddr` sets a `DaemonEndpoint` parsed from the test server URL. Mechanical change.
- **`newWorkerTestContext`** (daemon): no networking, no changes.
- **`RuntimeInfo` tests**: add `Network` field. Add compat test: JSON without `network` field defaults to TCP.

### New tests

- **`ParseEndpoint`** -- table-driven: empty, TCP, `unix://`, `unix:///path`, invalid (relative path, too-long path, non-loopback TCP, null bytes).
- **`DaemonEndpoint.Listener()`** -- TCP and Unix listeners bind and accept connections.
- **`DaemonEndpoint.HTTPClient()`** -- round-trip through a Unix socket. Start a server on a Unix listener in a short temp path, verify the client connects and gets a response.
- **`DefaultSocketPath()`** -- verify format includes uid, verify length is under 104 bytes.
- **RuntimeInfo compat** -- read old-format JSON (no `network` field), verify `Endpoint()` returns TCP.

### Socket path length in tests

`t.TempDir()` on macOS produces long paths that can exceed the 104-byte Unix socket limit. Tests that create Unix sockets should use short explicit paths under `/tmp` with `t.Cleanup` for removal, not `t.TempDir()`.

## Files Changed

| File | Change |
|------|--------|
| `internal/daemon/endpoint.go` | **New.** `DaemonEndpoint` type, `ParseEndpoint`, `DefaultSocketPath` |
| `internal/daemon/runtime.go` | Add `Network` field to `RuntimeInfo`, add `Endpoint()` method, change `WriteRuntime` signature, update probe/kill functions to use `DaemonEndpoint` |
| `internal/daemon/server.go` | `Start()` uses `ParseEndpoint`, Unix listener setup, socket cleanup on stop |
| `internal/daemon/client.go` | `addr` -> `baseURL`, `NewHTTPClient` takes `DaemonEndpoint` |
| `internal/config/config.go` | No struct changes (ServerAddr stays string). Default stays `"127.0.0.1:7373"` |
| `cmd/roborev/main.go` | `--server` flag default to `""` |
| `cmd/roborev/daemon_lifecycle.go` | `getDaemonEndpoint()` returns `DaemonEndpoint` |
| `cmd/roborev/daemon_cmd.go` | Pass parsed endpoint to server |
| `cmd/roborev/daemon_client.go` | Use `getDaemonEndpoint().BaseURL()` |
| `cmd/roborev/stream.go` | Use `getDaemonEndpoint()` for URL and transport |
| `cmd/roborev/tui_cmd.go` | Pass `DaemonEndpoint` to TUI |
| `cmd/roborev/tui/tui.go` | `serverAddr` -> `endpoint DaemonEndpoint` |
| `cmd/roborev/tui/api.go` | Use `m.endpoint.BaseURL()` |
| `cmd/roborev/tui/fetch.go` | Use `m.endpoint.BaseURL()` |
| Various `*_test.go` | Update to use `DaemonEndpoint`, add new transport tests |

## Out of Scope

- Making Unix socket the default transport (TCP remains default)
- Remote/non-loopback TCP (still requires auth/TLS, separate effort)
- Relative socket paths (rejected; can be added later if needed)
- Windows named pipes (Unix sockets only; Windows support not a current goal)

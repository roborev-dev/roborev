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
- `http://host:port` strips the scheme and treats as bare `host:port` (for backwards compatibility with the old `--server` flag format)
- `unix://` returns Unix with `DefaultSocketPath()`
- `unix:///absolute/path` returns Unix with the given path
- Anything else (relative path, non-loopback TCP) returns an error

```go
func DefaultSocketPath() string
```

Returns `filepath.Join(os.TempDir(), fmt.Sprintf("roborev-%d", os.Getuid()), "daemon.sock")`. Example paths:
- Linux: `/tmp/roborev-1000/daemon.sock` (~31 chars)
- macOS: `/var/folders/zz/.../T/roborev-501/daemon.sock` (~73 chars)

Both are well within the platform socket path limits. The macOS path length varies by system (the `os.TempDir()` segment can be longer on some setups), but `ParseEndpoint` validates the length at parse time and returns a clear error if it exceeds the limit. Users can fall back to `unix:///short/path.sock` if needed.

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

`FindAvailablePort` remains TCP-only. Not called for Unix endpoints. For TCP, `FindAvailablePort` returns a resolved `addr, port` (potentially with an incremented port); a new `DaemonEndpoint` is constructed from this resolved address for the rest of the `Start()` flow.

`validateDaemonBindAddr` is replaced by validation inside `ParseEndpoint`.

**`waitForServerReady`** signature changes from `waitForServerReady(ctx, addr string, ...)` to `waitForServerReady(ctx, ep DaemonEndpoint, ...)`. It passes the endpoint to `ProbeDaemon(ep, ...)` instead of a raw address string. This is critical -- without this change, `ProbeDaemon`'s loopback check would reject Unix socket addresses and the daemon would fail to start.

**Shutdown cleanup:** `Server.Stop()` removes the socket file for Unix endpoints. `CleanupZombieDaemons` detects stale Unix sockets by checking whether the owning PID (from RuntimeInfo) is alive -- if the process is dead, it removes the socket file and runtime file directly, rather than attempting an HTTP probe on a potentially unresponsive socket.

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

`KillDaemon` gets the endpoint from `info.Endpoint()` and uses `ep.HTTPClient()` + `ep.BaseURL()` for the shutdown POST. The inline `isLoopbackAddr` guard is replaced by the endpoint's own safety: Unix endpoints are inherently local, TCP endpoints had loopback validated at parse time. Both `GetAnyRunningDaemon` and `CleanupZombieDaemons` call `IsDaemonAlive(info.Endpoint())` instead of `IsDaemonAlive(info.Addr)`.

## CLI Changes

### Central principle

**All `http.Client` instances in `cmd/roborev/` that communicate with the daemon must be created through `DaemonEndpoint.HTTPClient()`.** A plain `&http.Client{}` uses the default TCP transport, which cannot dial Unix sockets.

### Module-level variable

The global `serverAddr string` is replaced by a `daemonEndpoint daemon.DaemonEndpoint`. A convenience helper provides an HTTP client:

```go
func getDaemonHTTPClient(timeout time.Duration) *http.Client {
    return daemonEndpoint.HTTPClient(timeout)
}
```

### Specific changes

**`cmd/roborev/main.go`:**

`--server` flag default changes from `"http://127.0.0.1:7373"` to `""`. Empty means "use config file, which defaults to TCP `127.0.0.1:7373`". This prevents the flag from always overriding config. The flag is parsed into `daemonEndpoint` via `ParseEndpoint` in a `PersistentPreRun` hook.

**`cmd/roborev/daemon_lifecycle.go`:**

`getDaemonAddr() string` becomes `getDaemonEndpoint() DaemonEndpoint`:

```go
func getDaemonEndpoint() DaemonEndpoint {
    if info, err := daemon.GetAnyRunningDaemon(); err == nil {
        return info.Endpoint()
    }
    ep, err := daemon.ParseEndpoint(serverAddr)
    if err != nil {
        // Fall back to TCP default if parsing fails
        return daemon.DaemonEndpoint{Network: "tcp", Address: "127.0.0.1:7373"}
    }
    return ep
}
```

`ensureDaemon()` and `startDaemon()` set `daemonEndpoint` instead of `serverAddr`. The fallback path in `ensureDaemon()` (which currently calls `probeDaemonServerURL(serverAddr, ...)` when no runtime file exists) changes to `ProbeDaemon(daemonEndpoint, ...)` directly -- the endpoint already encapsulates the transport.

`probeDaemonServerURL` is removed -- its URL-parsing logic is no longer needed since `DaemonEndpoint` encapsulates both transport and address.

**`cmd/roborev/daemon_client.go`:**

`waitForJob` and `showReview` use `getDaemonEndpoint().BaseURL()` for URLs and `getDaemonHTTPClient(timeout)` for HTTP clients. All 5 locally-constructed `&http.Client{}` instances and the bare `http.Post()` call are replaced.

**`cmd/roborev/stream.go`:**

Uses `getDaemonEndpoint()` for both the stream URL and the HTTP client (to get the right transport).

**All other CLI command files** (`analyze.go`, `compact.go`, `review.go`, `run.go`, `fix.go`, `list.go`, `summary.go`, `status.go`, `show.go`, `wait.go`, `comment.go`, `sync.go`, `remap.go`, `postcommit.go`, `refine.go`, `job_helpers.go`): Replace `getDaemonAddr()` + raw `&http.Client{}` with `getDaemonEndpoint().BaseURL()` + `getDaemonHTTPClient(timeout)`. These are mechanical changes. Notable special cases:

- `review.go` and `run.go` use bare `http.Post(serverAddr+...)` with the global variable directly -- these change to `getDaemonHTTPClient(timeout).Post(getDaemonEndpoint().BaseURL()+...)`.
- `postcommit.go` has a package-level `var hookHTTPClient = &http.Client{...}` which cannot be initialized at package load time (the endpoint isn't parsed yet). Change to lazy initialization: `hookHTTPClient` becomes a function `getHookHTTPClient()` that calls `getDaemonHTTPClient(3 * time.Second)` on first use.

**`cmd/roborev/daemon_cmd.go`:**

The `daemon run --addr` flag feeds into `cfg.ServerAddr` which is then parsed by `ParseEndpoint` inside `Server.Start()`. The `--addr` flag accepts the same `unix://` values as `server_addr` in config. No separate parsing needed -- `Start()` handles it.

## TUI Changes

`tui/tui.go`: model struct `serverAddr string` becomes `endpoint DaemonEndpoint`. `newModel(serverAddr string, ...)` becomes `newModel(ep DaemonEndpoint, ...)`. The `http.Client` on the model is created via `ep.HTTPClient(10 * time.Second)` instead of `&http.Client{Timeout: 10 * time.Second}`.

`tui/api.go`: `m.serverAddr + path` becomes `m.endpoint.BaseURL() + path`.

`tui/fetch.go`: `tryReconnect()` currently returns a `reconnectMsg{newAddr: string}`. Changes to return `reconnectMsg{endpoint: DaemonEndpoint}` derived from `info.Endpoint()`. The `reconnectMsg` struct in `tui/types.go` (or equivalent) changes its field from `newAddr string` to `endpoint DaemonEndpoint`.

`tui/handlers_msg.go`: `handleReconnectMsg` must update both `m.endpoint` and `m.client` (via `ep.HTTPClient(10 * time.Second)`) since the reconnected daemon may be on a different transport than the original.

`tui_cmd.go`: passes `DaemonEndpoint` to `newModel` instead of a URL string. The TUI's own `--addr` flag is updated to accept `unix://` values and is parsed via `ParseEndpoint` before being passed to `newModel`. The existing `http://` prefix logic is removed (handled by `ParseEndpoint`'s backwards-compatible `http://` stripping).

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
| `cmd/roborev/daemon_cmd.go` | `--addr` flag accepts `unix://` values, feeds into `cfg.ServerAddr` |
| `cmd/roborev/daemon_client.go` | Replace raw `http.Client{}` with `getDaemonHTTPClient()`, use `getDaemonEndpoint().BaseURL()` |
| `cmd/roborev/stream.go` | Use `getDaemonEndpoint()` for URL and transport |
| `cmd/roborev/*.go` (15+ command files) | Replace `getDaemonAddr()` + raw `http.Client{}` with `getDaemonEndpoint().BaseURL()` + `getDaemonHTTPClient()` |
| `cmd/roborev/tui_cmd.go` | Pass `DaemonEndpoint` to TUI |
| `cmd/roborev/tui/tui.go` | `serverAddr` -> `endpoint DaemonEndpoint` |
| `cmd/roborev/tui/api.go` | Use `m.endpoint.BaseURL()` |
| `cmd/roborev/tui/fetch.go` | Use `m.endpoint.BaseURL()`, `tryReconnect()` returns `DaemonEndpoint` |
| `cmd/roborev/tui/handlers_msg.go` | `handleReconnectMsg` updates both endpoint and HTTP client |
| Various `*_test.go` | Update to use `DaemonEndpoint`, add new transport tests |

## Container Bind-Mount Note

The auto-generated socket path (`unix://`) uses `os.TempDir()` which resolves inside the current environment. For container bind-mount use cases, the explicit path form (`unix:///shared/roborev.sock`) is recommended, since the host's temp directory may not be accessible or may resolve differently inside a container.

## Out of Scope

- Making Unix socket the default transport (TCP remains default)
- Remote/non-loopback TCP (still requires auth/TLS, separate effort)
- Relative socket paths (rejected; can be added later if needed)
- Windows named pipes (Unix sockets only; Windows support not a current goal)

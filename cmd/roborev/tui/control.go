package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const controlResponseTimeout = 3 * time.Second

// removeStaleSocket checks whether socketPath is a leftover Unix
// socket from a previous crash and removes it. Returns an error if
// the path exists but is not a socket (protecting regular files from
// accidental deletion via a mistyped --control-socket).
func removeStaleSocket(socketPath string) error {
	fi, err := os.Lstat(socketPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat socket path: %w", err)
	}
	if fi.Mode().Type()&os.ModeSocket == 0 {
		return fmt.Errorf(
			"%s already exists and is not a socket", socketPath,
		)
	}
	// It's a socket — try to connect to see if it's still live.
	conn, dialErr := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return fmt.Errorf(
			"%s is already in use by another listener", socketPath,
		)
	}
	// Only treat ECONNREFUSED as proof that nothing is listening.
	// Other dial errors (wrong socket type, permission denied, etc.)
	// are ambiguous and could indicate a live non-stream socket.
	if !isConnRefused(dialErr) {
		return fmt.Errorf(
			"%s: cannot determine socket state: %w",
			socketPath, dialErr,
		)
	}
	// ECONNREFUSED — nothing is listening. Safe to remove.
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

// isConnRefused returns true when the error chain contains
// ECONNREFUSED, which means a socket exists but nothing is listening.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// startControlListener creates a Unix domain socket and starts
// accepting connections. Each connection receives one JSON command,
// dispatches it through the tea.Program, and returns a JSON response.
// Returns a cleanup function that closes the listener and removes
// the socket file.
func startControlListener(
	socketPath string, p *tea.Program,
) (func(), error) {
	// Ensure the parent directory exists (fresh installs,
	// custom --control-socket paths).
	if err := os.MkdirAll(filepath.Dir(socketPath), 0755); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}

	// Only remove an existing path if it is a stale Unix socket.
	// Refusing to remove regular files prevents data loss from
	// a mistyped --control-socket path.
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}

	// Restrict umask so the socket is created with 0600 from the
	// start, closing the TOCTOU window between Listen and Chmod.
	oldUmask := restrictedUmask()
	ln, err := net.Listen("unix", socketPath)
	restoreUmask(oldUmask)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	go acceptLoop(ctx, ln, p)

	cleanup := func() {
		cancel()
		ln.Close()
		os.Remove(socketPath)
	}
	return cleanup, nil
}

func acceptLoop(ctx context.Context, ln net.Listener, p *tea.Program) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Check if listener was closed (normal shutdown)
			select {
			case <-ctx.Done():
				return
			default:
			}
			log.Printf("control: accept error: %v", err)
			// Back off on transient errors (e.g. EMFILE) to
			// avoid a tight CPU-pegging loop.
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go handleControlConn(ctx, conn, p)
	}
}

func handleControlConn(
	ctx context.Context, conn net.Conn, p *tea.Program,
) {
	defer conn.Close()

	// Set read deadline to prevent hung connections
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	if !scanner.Scan() {
		writeError(conn, "empty request")
		return
	}

	var req controlRequest
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		writeError(conn, "invalid JSON: "+err.Error())
		return
	}

	resp := dispatchCommand(ctx, req, p)
	writeResponse(conn, resp)
}

func dispatchCommand(
	ctx context.Context, req controlRequest, p *tea.Program,
) controlResponse {
	isQuery, isMutation := isControlCommand(req.Command)

	switch {
	case isQuery:
		return queryViaProgram(ctx, p, req)
	case isMutation:
		return mutateViaProgram(ctx, p, req)
	default:
		return controlResponse{
			Error: fmt.Sprintf("unknown command: %s", req.Command),
		}
	}
}

// sendViaProgram dispatches msg through p.Send in a goroutine so
// the response timeout covers the Send call itself. Program.Send
// blocks until the event loop is running, so without the goroutine
// a control request arriving during startup would hang outside the
// timeout select.
func sendViaProgram(
	ctx context.Context, p *tea.Program, msg tea.Msg,
	respCh <-chan controlResponse,
) controlResponse {
	go p.Send(msg)

	select {
	case resp := <-respCh:
		return resp
	case <-ctx.Done():
		return controlResponse{Error: "TUI is shutting down"}
	case <-time.After(controlResponseTimeout):
		return controlResponse{Error: "response timeout"}
	}
}

// queryViaProgram sends a controlQueryMsg through the program and
// waits for the Update handler to write the response.
func queryViaProgram(
	ctx context.Context, p *tea.Program, req controlRequest,
) controlResponse {
	respCh := make(chan controlResponse, 1)
	return sendViaProgram(
		ctx, p,
		controlQueryMsg{req: req, respCh: respCh},
		respCh,
	)
}

// mutateViaProgram sends a controlMutationMsg through the program
// and waits for the Update handler to write the response.
func mutateViaProgram(
	ctx context.Context, p *tea.Program, req controlRequest,
) controlResponse {
	respCh := make(chan controlResponse, 1)
	return sendViaProgram(
		ctx, p,
		controlMutationMsg{req: req, respCh: respCh},
		respCh,
	)
}

func writeResponse(conn net.Conn, resp controlResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		writeError(conn, "marshal error: "+err.Error())
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

func writeError(conn net.Conn, msg string) {
	resp := controlResponse{Error: msg}
	data, _ := json.Marshal(resp)
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

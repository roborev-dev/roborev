package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/acp-go-sdk"
	"github.com/roborev-dev/roborev/internal/config"
)

// Security error for path traversal attempts
var ErrPathTraversal = errors.New("path traversal attempt detected")

const (
	defaultACPName            = "acp"
	defaultACPCommand         = "acp-agent"
	defaultACPReadOnlyMode    = "plan"
	defaultACPAutoApproveMode = "auto-approve"
	defaultACPTimeoutSeconds  = 600
	maxACPTextFileBytes       = 10_000_000
)

// ACPAgent runs code reviews using the Agent Client Protocol via acp-go-sdk
type ACPAgent struct {
	agentName       string   // Agent name (from configuration)
	Command         string   // ACP agent command (configured via TOML)
	Args            []string // Additional arguments for the agent
	Model           string   // Model to use
	Mode            string   // Mode to use
	ReadOnlyMode    string
	AutoApproveMode string
	Reasoning       ReasoningLevel // Reasoning level
	Agentic         bool           // Agentic mode
	Timeout         time.Duration  // Command timeout
	SessionId       string         // Current ACP session ID
	repoRoot        string         // Repository root for path validation
}

func NewACPAgent(command string) *ACPAgent {
	if command == "" {
		command = defaultACPCommand
	}

	return &ACPAgent{
		agentName:       defaultACPName,
		Command:         command,
		Args:            []string{},
		Model:           "",
		Mode:            defaultACPReadOnlyMode,
		ReadOnlyMode:    defaultACPReadOnlyMode,
		AutoApproveMode: defaultACPAutoApproveMode,
		Timeout:         time.Duration(defaultACPTimeoutSeconds) * time.Second,
		Reasoning:       ReasoningStandard,
		SessionId:       "", // Initialize with empty session ID
	}
}

func NewACPAgentFromConfig(config *config.ACPAgentConfig) *ACPAgent {
	if config == nil {
		return NewACPAgent("")
	}

	agent := NewACPAgent(config.Command)
	if agentName := strings.TrimSpace(config.Name); agentName != "" {
		agent.agentName = agentName
	}
	if len(config.Args) > 0 {
		agent.Args = append([]string(nil), config.Args...)
	}
	if model := strings.TrimSpace(config.Model); model != "" {
		agent.Model = model
	}
	if readOnlyMode := strings.TrimSpace(config.ReadOnlyMode); readOnlyMode != "" {
		agent.ReadOnlyMode = readOnlyMode
	}
	if autoApproveMode := strings.TrimSpace(config.AutoApproveMode); autoApproveMode != "" {
		agent.AutoApproveMode = autoApproveMode
	}
	if config.DisableModeNegotiation {
		agent.Mode = ""
	} else if mode := strings.TrimSpace(config.Mode); mode != "" {
		agent.Mode = mode
	} else {
		agent.Mode = agent.ReadOnlyMode
	}

	timeout := time.Duration(defaultACPTimeoutSeconds) * time.Second
	if config.Timeout > 0 {
		timeout = time.Duration(config.Timeout) * time.Second
	}
	agent.Timeout = timeout

	return agent
}

func (a *ACPAgent) Name() string {
	return a.agentName
}

func (a *ACPAgent) CommandName() string {
	return a.Command
}

func (a *ACPAgent) CommandLine() string {
	return a.Command + " " + strings.Join(a.Args, " ")
}

func (a *ACPAgent) WithReasoning(level ReasoningLevel) Agent {
	return &ACPAgent{
		agentName:       a.agentName,
		Command:         a.Command,
		Args:            a.Args,
		Model:           a.Model,
		ReadOnlyMode:    a.ReadOnlyMode,
		AutoApproveMode: a.AutoApproveMode,
		Mode:            a.Mode,
		Reasoning:       level,     // Use the provided level parameter
		Agentic:         a.Agentic, // Preserve Agentic field
		Timeout:         a.Timeout,
		SessionId:       a.SessionId, // Preserve SessionId
	}
}

func (a *ACPAgent) WithAgentic(agentic bool) Agent {

	// Set the appropriate mode based on agentic flag
	mode := a.ReadOnlyMode
	if agentic && a.AutoApproveMode != "" {
		mode = a.AutoApproveMode
	}
	if strings.TrimSpace(a.Mode) == "" {
		mode = ""
	}

	return &ACPAgent{
		agentName:       a.agentName,
		Command:         a.Command,
		Args:            a.Args,
		Model:           a.Model,
		ReadOnlyMode:    a.ReadOnlyMode,
		AutoApproveMode: a.AutoApproveMode,
		Mode:            mode,
		Reasoning:       a.Reasoning,
		Agentic:         agentic,
		Timeout:         a.Timeout,
		SessionId:       a.SessionId, // Preserve SessionId
	}
}

func (a *ACPAgent) WithModel(model string) Agent {
	if model == "" {
		return a
	}

	return &ACPAgent{
		agentName:       a.agentName,
		Command:         a.Command,
		Args:            a.Args,
		Model:           model,
		ReadOnlyMode:    a.ReadOnlyMode,
		AutoApproveMode: a.AutoApproveMode,
		Mode:            a.Mode,
		Reasoning:       a.Reasoning,
		Agentic:         a.Agentic, // Preserve Agentic field
		Timeout:         a.Timeout,
		SessionId:       a.SessionId, // Preserve SessionId
	}
}

// Review implements the main review functionality using ACP SDK
func (a *ACPAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	// Set timeout context
	var cancel context.CancelFunc
	var err error
	ctx, cancel = context.WithTimeout(ctx, a.Timeout)
	defer cancel()

	// Build the command with arguments
	cmd := exec.CommandContext(ctx, a.Command, a.Args...)

	// Set up stdio pipes for communication with the agent
	var stdinPipe io.WriteCloser
	var stdoutPipe io.ReadCloser
	var pipesCleanup func() error

	// Initialize pipes with proper cleanup
	pipeInit := func() error {
		var err error
		stdinPipe, err = cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("failed to create stdin pipe: %w", err)
		}
		stdoutPipe, err = cmd.StdoutPipe()
		if err != nil {
			_ = stdinPipe.Close()
			return fmt.Errorf("failed to create stdout pipe: %w", err)
		}

		// Set up cleanup function that will be called in reverse order
		pipesCleanup = func() error {
			var pipeErrors []error
			if closeErr := stdoutPipe.Close(); closeErr != nil {
				pipeErrors = append(pipeErrors, closeErr)
			}
			if closeErr := stdinPipe.Close(); closeErr != nil {
				pipeErrors = append(pipeErrors, closeErr)
			}
			if len(pipeErrors) > 0 {
				return fmt.Errorf("pipe cleanup errors: %v", pipeErrors)
			}
			return nil
		}
		return nil
	}

	if err := pipeInit(); err != nil {
		return "", err
	}

	// Start the agent process
	if err := cmd.Start(); err != nil {
		_ = pipesCleanup()
		return "", fmt.Errorf("failed to start ACP agent: %w", err)
	}

	// Defer cleanup in proper order: terminals -> pipes -> process
	// Create a client that handles agent responses
	client := &acpClient{
		agent:          a,
		output:         output,
		result:         &bytes.Buffer{},
		sessionID:      "",
		repoRoot:       repoPath,
		terminals:      make(map[string]*acpTerminal),
		nextTerminalID: 1,
	}

	// Deferred cleanup to ensure no orphaned terminal processes
	defer func() {
		// Cancel all active terminals first
		client.terminalsMutex.Lock()
		for _, terminal := range client.terminals {
			terminal.cancel()
		}
		client.terminalsMutex.Unlock()

		// Then clean up pipes
		if pipesCleanup != nil {
			_ = pipesCleanup()
		}

		// Finally clean up process resources
		if cmd.Process != nil {
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		}
	}()

	// Create the ACP connection
	conn := acp.NewClientSideConnection(client, stdinPipe, stdoutPipe)

	_, err = conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientCapabilities: acp.ClientCapabilities{
			Fs: acp.FileSystemCapability{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: true,
		},
	})
	if err != nil {
		// Check process state to provide better error context
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return "", fmt.Errorf("failed to initialize ACP connection (agent exited with code %d): %w",
				cmd.ProcessState.ExitCode(), err)
		}
		return "", fmt.Errorf("failed to initialize ACP connection: %w", err)
	}

	// Create a new session
	sessionResp, err := conn.NewSession(ctx, acp.NewSessionRequest{
		Cwd:        repoPath,
		McpServers: []acp.McpServer{},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	// Store the session ID for request-scoped validation.
	client.sessionID = string(sessionResp.SessionId)

	if a.Mode != "" {
		if err := validateConfiguredMode(a.Mode, sessionResp.Modes); err != nil {
			return "", err
		}

		_, err = conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sessionResp.SessionId, ModeId: acp.SessionModeId(a.Mode)})
		if err != nil {
			return "", fmt.Errorf("failed to set session mode: %w", err)
		}
	}

	if a.Model != "" {
		if err := validateConfiguredModel(a.Model, sessionResp.Models); err != nil {
			return "", err
		}

		_, err = conn.SetSessionModel(ctx, acp.SetSessionModelRequest{SessionId: sessionResp.SessionId, ModelId: acp.ModelId(a.Model)})
		if err != nil {
			return "", fmt.Errorf("failed to set session model: %w", err)
		}
	}

	// Send the prompt request
	promptRequest := acp.PromptRequest{
		SessionId: sessionResp.SessionId,
		Prompt: []acp.ContentBlock{
			acp.TextBlock(fmt.Sprintf("Review the code changes in commit %s.\n\nRepository: %s\n\nPrompt: %s",
				commitSHA, repoPath, prompt)),
		},
	}

	promptResponse, err := conn.Prompt(ctx, promptRequest)
	if err != nil {
		return "", fmt.Errorf("failed to send prompt: %w", err)
	}

	// Wait for the agent to finish processing
	if promptResponse.StopReason != acp.StopReasonEndTurn {
		return "", fmt.Errorf("agent did not complete processing: %s", promptResponse.StopReason)
	}

	return client.resultString(), nil
}

// acpTerminal represents an active terminal session
type acpTerminal struct {
	id              string
	cmd             *exec.Cmd
	output          *bytes.Buffer
	outputWriter    *boundedWriter
	context         context.Context
	cancel          context.CancelFunc
	outputByteLimit int
	truncated       bool
	done            chan struct{} // Channel to signal command completion
	stateMu         sync.RWMutex
	exitStatus      *acp.TerminalExitStatus
}

// threadSafeWriter is a thread-safe io.Writer that protects a bytes.Buffer
type threadSafeWriter struct {
	buf   *bytes.Buffer
	mutex *sync.Mutex
}

func (w *threadSafeWriter) Write(p []byte) (n int, err error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.buf.Write(p)
}

// boundedWriter wraps a threadSafeWriter and enforces output size limits
// while maintaining UTF-8 character boundaries
type boundedWriter struct {
	writer    *threadSafeWriter
	maxSize   int
	truncated bool
}

func (bw *boundedWriter) Write(p []byte) (n int, err error) {
	bw.writer.mutex.Lock()
	defer bw.writer.mutex.Unlock()

	if bw.maxSize <= 0 {
		if len(p) > 0 {
			bw.truncated = true
		}
		return len(p), nil
	}

	if _, err := bw.writer.buf.Write(p); err != nil {
		return 0, err
	}

	bw.trimToMaxSizeLocked()
	return len(p), nil
}

// acpClient implements the acp.Client interface to handle agent responses
type acpClient struct {
	agent               *ACPAgent
	output              io.Writer
	result              *bytes.Buffer
	resultMutex         sync.Mutex
	sessionID           string
	repoRoot            string
	terminals           map[string]*acpTerminal // Active terminals by ID
	terminalsMutex      sync.Mutex              // Mutex for concurrent access to terminals map
	nextTerminalID      int                     // Counter for generating terminal IDs
	nextTerminalIDMutex sync.Mutex              // Mutex for concurrent access to terminal ID counter
}

// validateSessionID validates that the session ID in the request matches the agent's session ID
func (c *acpClient) validateSessionID(sessionID acp.SessionId) error {
	expectedSessionID := c.sessionID
	if expectedSessionID == "" && c.agent != nil {
		expectedSessionID = c.agent.SessionId
	}
	if expectedSessionID == "" {
		if sessionID != "" {
			return fmt.Errorf("session ID mismatch: no active session, got %s", sessionID)
		}
		return nil
	}
	if expectedSessionID != "" && string(sessionID) != expectedSessionID {
		return fmt.Errorf("session ID mismatch: expected %s, got %s", expectedSessionID, sessionID)
	}
	return nil
}

// getTerminal retrieves a terminal by ID with proper locking.
// The returned terminal pointer remains valid even if it is later removed from the map.
func (c *acpClient) getTerminal(terminalID string) (*acpTerminal, bool) {
	c.terminalsMutex.Lock()
	defer c.terminalsMutex.Unlock()
	terminal, exists := c.terminals[terminalID]
	return terminal, exists
}

func (c *acpClient) effectiveRepoRoot() string {
	if c.repoRoot != "" {
		return c.repoRoot
	}
	if c.agent != nil {
		return c.agent.repoRoot
	}
	return ""
}

// addTerminal adds a terminal to the map with proper locking
func (c *acpClient) addTerminal(terminal *acpTerminal) {
	c.terminalsMutex.Lock()
	defer c.terminalsMutex.Unlock()
	c.terminals[terminal.id] = terminal
}

// removeTerminal removes a terminal from the map with proper locking
func (c *acpClient) removeTerminal(terminalID string) {
	c.terminalsMutex.Lock()
	defer c.terminalsMutex.Unlock()
	delete(c.terminals, terminalID)
}

// generateTerminalID generates a unique terminal ID with proper locking
func (c *acpClient) generateTerminalID() string {
	c.nextTerminalIDMutex.Lock()
	defer c.nextTerminalIDMutex.Unlock()
	id := fmt.Sprintf("term-%d", c.nextTerminalID)
	c.nextTerminalID++
	return id
}

// truncateOutput ensures output stays within byte limits, truncating from the beginning
// while maintaining character boundaries as required by ACP spec
func truncateOutput(output *bytes.Buffer, limit int, outputMutex *sync.Mutex) (string, bool) {
	// Validate limit to prevent panics
	if limit < 0 {
		limit = 0
	}

	outputMutex.Lock()
	defer outputMutex.Unlock()

	currentOutput := output.Bytes()

	if len(currentOutput) <= limit {
		return string(currentOutput), false
	}

	start := len(currentOutput) - limit
	for start < len(currentOutput) && !utf8.RuneStart(currentOutput[start]) {
		start++
	}

	truncatedOutput := append([]byte(nil), currentOutput[start:]...)

	// Clear the buffer and write the truncated content
	output.Reset()
	_, _ = output.Write(truncatedOutput)

	return string(truncatedOutput), true
}

// getTerminalExitStatus extracts the exit status from a command's ProcessState
// Returns nil if the command hasn't exited yet
// Follows ACP spec: exitCode is nil when process terminated by signal
func getTerminalExitStatus(processState *os.ProcessState) *acp.TerminalExitStatus {
	if processState == nil {
		return nil
	}

	exitCodeValue := processState.ExitCode()
	exitCode := &exitCodeValue
	var signal *string

	// Check if process was terminated by a signal
	if exited := processState.Sys(); exited != nil {
		// For Unix systems, check for signal termination
		if ws, ok := exited.(syscall.WaitStatus); ok {
			if ws.Signaled() {
				signalName := ws.Signal().String()
				signal = &signalName
				// Per ACP spec, exitCode should be nil when terminated by signal
				exitCode = nil
			}
		}
	}

	return &acp.TerminalExitStatus{
		ExitCode: exitCode,
		Signal:   signal,
	}
}

func cloneTerminalExitStatus(status *acp.TerminalExitStatus) *acp.TerminalExitStatus {
	if status == nil {
		return nil
	}

	var exitCode *int
	if status.ExitCode != nil {
		value := *status.ExitCode
		exitCode = &value
	}

	var signal *string
	if status.Signal != nil {
		value := *status.Signal
		signal = &value
	}

	return &acp.TerminalExitStatus{
		ExitCode: exitCode,
		Signal:   signal,
	}
}

func (t *acpTerminal) setExitStatus(status *acp.TerminalExitStatus) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	t.exitStatus = cloneTerminalExitStatus(status)
}

func (t *acpTerminal) getExitStatus() *acp.TerminalExitStatus {
	t.stateMu.RLock()
	defer t.stateMu.RUnlock()
	return cloneTerminalExitStatus(t.exitStatus)
}

func (c *acpClient) resultString() string {
	c.resultMutex.Lock()
	defer c.resultMutex.Unlock()
	return c.result.String()
}

func readTextFileWindow(path string, startLine int, limit *int, maxBytes int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var out strings.Builder
	var lineBuf bytes.Buffer
	currentLine := 0
	selectedLines := 0
	wroteLine := false
	endedWithNewline := false

	appendLine := func(line []byte) error {
		additionalBytes := len(line)
		if wroteLine {
			additionalBytes++
		}
		if out.Len()+additionalBytes > maxBytes {
			return fmt.Errorf("file content too large: exceeds max %d bytes", maxBytes)
		}
		if wroteLine {
			out.WriteByte('\n')
		}
		if len(line) > 0 {
			out.Write(line)
		}
		wroteLine = true
		selectedLines++
		return nil
	}

	lineBufferBudget := func() int {
		budget := maxBytes - out.Len()
		if wroteLine {
			budget--
		}
		return budget
	}

	appendLineChunk := func(chunk []byte) error {
		if len(chunk) == 0 {
			return nil
		}
		if lineBufferBudget() < lineBuf.Len()+len(chunk) {
			return fmt.Errorf("file content too large: exceeds max %d bytes", maxBytes)
		}
		_, err := lineBuf.Write(chunk)
		return err
	}

	for {
		chunk, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			if currentLine >= startLine && (limit == nil || selectedLines < *limit) {
				if err := appendLineChunk(chunk); err != nil {
					return "", err
				}
			}
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return "", readErr
		}
		if len(chunk) == 0 && readErr == io.EOF {
			break
		}

		hasTrailingNewline := len(chunk) > 0 && chunk[len(chunk)-1] == '\n'
		endedWithNewline = hasTrailingNewline

		if currentLine >= startLine && (limit == nil || selectedLines < *limit) {
			if hasTrailingNewline {
				chunk = chunk[:len(chunk)-1]
			}
			if err := appendLineChunk(chunk); err != nil {
				return "", err
			}
			if err := appendLine(lineBuf.Bytes()); err != nil {
				return "", err
			}
			lineBuf.Reset()
			if limit != nil && selectedLines >= *limit {
				return out.String(), nil
			}
		}
		currentLine++

		if readErr == io.EOF {
			return out.String(), nil
		}
	}

	// strings.Split preserves a trailing empty line when the file ends with '\n'.
	if endedWithNewline && currentLine >= startLine && (limit == nil || selectedLines < *limit) {
		if wroteLine {
			if out.Len()+1 > maxBytes {
				return "", fmt.Errorf("file content too large: exceeds max %d bytes", maxBytes)
			}
			out.WriteByte('\n')
		}
	}

	return out.String(), nil
}

func writeTextFileAtomically(path string, content []byte) error {
	parentDir := filepath.Dir(path)
	baseName := filepath.Base(path)

	existingInfo, statErr := os.Stat(path)
	pathExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if pathExists {
		// Preserve write permission semantics for existing files.
		writableProbe, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_ = writableProbe.Close()
	}

	tempPerm := os.FileMode(0o644)
	if pathExists {
		tempPerm = existingInfo.Mode().Perm()
	}

	tempFile, err := createTempFileWithPerm(parentDir, baseName, tempPerm)
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	removeTemp := true
	defer func() {
		_ = tempFile.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := tempFile.Write(content); err != nil {
		return err
	}
	if pathExists {
		if err := tempFile.Chmod(existingInfo.Mode().Perm()); err != nil {
			return err
		}
	}
	if err := tempFile.Sync(); err != nil {
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func createTempFileWithPerm(parentDir, baseName string, perm os.FileMode) (*os.File, error) {
	for range 256 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		tempName := fmt.Sprintf(".%s.tmp-%x", baseName, suffix)
		tempPath := filepath.Join(parentDir, tempName)

		file, err := os.OpenFile(tempPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return file, nil
		}
		if os.IsExist(err) {
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("failed to create temporary file in %s", parentDir)
}

func (c *acpClient) ReadTextFile(ctx context.Context, params acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.ReadTextFileResponse{}, err
	}

	// Validate input parameters
	if params.Path == "" {
		return acp.ReadTextFileResponse{}, fmt.Errorf("path cannot be empty")
	}

	// Validate path to prevent directory traversal
	validatedPath, err := c.validateAndResolvePath(params.Path, false) // false = read operation
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("failed to validate read path %s: %w", params.Path, err)
	}

	// Validate numeric parameters
	if params.Line != nil && *params.Line < 1 {
		return acp.ReadTextFileResponse{}, fmt.Errorf("invalid line number: %d (must be >= 1)", *params.Line)
	}
	if params.Limit != nil && *params.Limit < 0 {
		return acp.ReadTextFileResponse{}, fmt.Errorf("invalid limit: %d (must be >= 0)", *params.Limit)
	}

	// Validate that line and limit are reasonable to prevent resource exhaustion
	if params.Line != nil && *params.Line > 1000000 {
		return acp.ReadTextFileResponse{}, fmt.Errorf("line number too large: %d (max 1,000,000)", *params.Line)
	}
	if params.Limit != nil && *params.Limit > 1000000 {
		return acp.ReadTextFileResponse{}, fmt.Errorf("limit too large: %d (max 1,000,000)", *params.Limit)
	}

	var fileContent string

	startLine := 0
	if params.Line != nil {
		startLine = max(*params.Line-1, 0) // Convert to 0-based index
	}

	fileContent, err = readTextFileWindow(validatedPath, startLine, params.Limit, maxACPTextFileBytes)
	if err != nil {
		return acp.ReadTextFileResponse{}, fmt.Errorf("failed to read file %s: %w", validatedPath, err)
	}

	return acp.ReadTextFileResponse{
		Content: fileContent,
	}, nil
}

func (c *acpClient) WriteTextFile(ctx context.Context, params acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.WriteTextFileResponse{}, err
	}

	// Enforce authorization at point of operation.
	if !c.agent.mutatingOperationsAllowed() {
		if c.agent.effectivePermissionMode() == c.agent.ReadOnlyMode {
			return acp.WriteTextFileResponse{}, fmt.Errorf("write operation not permitted in read-only mode")
		}
		return acp.WriteTextFileResponse{}, fmt.Errorf("write operation not permitted unless auto-approve mode is explicitly enabled")
	}

	// Validate input parameters
	if params.Path == "" {
		return acp.WriteTextFileResponse{}, fmt.Errorf("path cannot be empty")
	}

	// Validate path to prevent directory traversal
	validatedPath, err := c.validateAndResolvePath(params.Path, true) // true = write operation
	if err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("failed to validate write path %s: %w", params.Path, err)
	}

	// Validate content size to prevent resource exhaustion
	if len(params.Content) > maxACPTextFileBytes {
		return acp.WriteTextFileResponse{}, fmt.Errorf("content too large: %d bytes (max %d)", len(params.Content), maxACPTextFileBytes)
	}

	// Write via temp file + rename to avoid validate-then-write races.
	err = writeTextFileAtomically(validatedPath, []byte(params.Content))
	if err != nil {
		return acp.WriteTextFileResponse{}, fmt.Errorf("failed to write file %s: %w", validatedPath, err)
	}

	return acp.WriteTextFileResponse{}, nil
}

func (c *acpClient) RequestPermission(ctx context.Context, params acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.RequestPermissionResponse{}, err
	}

	// Default to deny for safety - unknown operations should be rejected
	var isDestructive bool
	var isKnownKind bool

	if params.ToolCall.Kind != nil {
		toolKind := string(*params.ToolCall.Kind)

		// Define destructive operations that modify state
		// Based on ACP protocol ToolKind constants
		destructiveKinds := map[string]bool{
			"edit":    true, // Modifying files or content
			"delete":  true, // Removing files or data
			"move":    true, // Moving or renaming files
			"execute": true, // Executing commands (potentially destructive)
		}

		// Non-destructive operations
		nonDestructiveKinds := map[string]bool{
			"read":   true, // Reading files or data
			"search": true, // Searching for files or data
			"think":  true, // Internal reasoning
			"fetch":  true, // Fetching data
		}

		// Explicitly validate tool kind
		if destructiveKinds[toolKind] {
			isDestructive = true
			isKnownKind = true
		} else if nonDestructiveKinds[toolKind] {
			isDestructive = false
			isKnownKind = true
		} else {
			// Unknown tool kind - explicitly deny
			return acp.RequestPermissionResponse{
				Outcome: selectPermissionOutcome(params.Options, false),
			}, nil
		}
	} else {
		// ToolCall.Kind is nil - invalid request
		return acp.RequestPermissionResponse{
			Outcome: selectPermissionOutcome(params.Options, false),
		}, nil
	}

	// Apply permission logic based on effective permission mode.
	// When session mode negotiation is disabled (Mode == ""), keep
	// permission behavior in read-only mode by default.
	effectiveMode := c.agent.effectivePermissionMode()

	// In read-only mode, deny all destructive operations.
	if effectiveMode == c.agent.ReadOnlyMode {
		if isDestructive {
			return acp.RequestPermissionResponse{
				Outcome: selectPermissionOutcome(params.Options, false),
			}, nil
		}
		// Allow non-destructive operations in read-only mode
		return acp.RequestPermissionResponse{
			Outcome: selectPermissionOutcome(params.Options, true),
		}, nil
	}

	// Only explicit auto-approve mode allows known operations.
	if c.agent.mutatingOperationsAllowed() && isKnownKind {
		return acp.RequestPermissionResponse{
			Outcome: selectPermissionOutcome(params.Options, true),
		}, nil
	}

	// This should not be reached due to earlier checks, but default to deny
	return acp.RequestPermissionResponse{
		Outcome: selectPermissionOutcome(params.Options, false),
	}, nil
}

func (c *acpClient) SessionUpdate(ctx context.Context, params acp.SessionNotification) error {
	if err := c.validateSessionID(params.SessionId); err != nil {
		return err
	}

	// Handle streaming updates from the agent
	if params.Update.AgentMessageChunk != nil {
		if params.Update.AgentMessageChunk.Content.Text != nil {
			text := params.Update.AgentMessageChunk.Content.Text.Text
			c.resultMutex.Lock()
			if c.output != nil {
				if _, err := c.output.Write([]byte(text)); err != nil {
					c.resultMutex.Unlock()
					return err
				}
			}
			c.result.WriteString(text)
			c.resultMutex.Unlock()
		}
	}
	return nil
}

// validateAndResolvePath validates that a file path is within the repository root
// and resolves it to an absolute path. This prevents directory traversal attacks
// including symlink traversal.
// For write operations (forWrite=true), only validates parent directory since the file may not exist yet.
func (c *acpClient) validateAndResolvePath(requestedPath string, forWrite bool) (string, error) {
	repoRoot := c.effectiveRepoRoot()
	if repoRoot == "" {
		return "", fmt.Errorf("repository root not set")
	}

	// Clean the path to remove any . or .. components
	cleanPath := filepath.Clean(requestedPath)

	// Get absolute path of repository root
	repoRootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to resolve repository root path: %w", err)
	}

	// Get absolute path of the requested file
	absPath := cleanPath
	if !filepath.IsAbs(cleanPath) {
		absPath = filepath.Join(repoRootAbs, cleanPath)
	}

	// Clean the absolute path again to resolve any remaining . or .. components
	absPath = filepath.Clean(absPath)

	if forWrite {
		// For writes, validate parent directory only since file may not exist yet
		parentDir := filepath.Dir(absPath)
		resolvedParent, err := filepath.EvalSymlinks(parentDir)
		if err != nil {
			return "", fmt.Errorf("%w: failed to resolve parent directory symlinks for path %s: %v", ErrPathTraversal, requestedPath, err)
		}

		// Check if parent directory is within repository root
		resolvedRepoRoot, err := filepath.EvalSymlinks(repoRootAbs)
		if err != nil {
			return "", fmt.Errorf("failed to resolve repository root symlinks: %w", err)
		}

		// Normalize both paths for comparison
		resolvedParent = filepath.Clean(resolvedParent)
		resolvedRepoRoot = filepath.Clean(resolvedRepoRoot)

		// Check if resolved parent directory is within resolved repository root
		if !pathWithinRoot(resolvedParent, resolvedRepoRoot) {
			return "", fmt.Errorf("%w: path %s (parent resolved to %s) is outside repository root %s", ErrPathTraversal, requestedPath, resolvedParent, repoRoot)
		}

		// Append base filename without requiring it to exist.
		validatedPath := filepath.Join(resolvedParent, filepath.Base(absPath))

		// If the target exists and is a symlink, ensure its resolved destination
		// still stays within the repository root, then write through the resolved
		// target path to avoid symlink swap races.
		info, err := os.Lstat(validatedPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", fmt.Errorf("failed to inspect write target %s: %w", validatedPath, err)
			}
			return validatedPath, nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolvedTarget, err := filepath.EvalSymlinks(validatedPath)
			if err != nil {
				return "", fmt.Errorf("%w: failed to resolve write target symlink for path %s: %v", ErrPathTraversal, requestedPath, err)
			}
			resolvedTarget = filepath.Clean(resolvedTarget)
			if !pathWithinRoot(resolvedTarget, resolvedRepoRoot) {
				return "", fmt.Errorf("%w: path %s (symlink target resolved to %s) is outside repository root %s", ErrPathTraversal, requestedPath, resolvedTarget, repoRoot)
			}
			return resolvedTarget, nil
		}

		return validatedPath, nil
	}

	// For reads, keep strict validation requiring full path to exist
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// If we can't resolve symlinks (e.g., broken symlink or permission issue),
		// we treat this as an invalid path for security
		return "", fmt.Errorf("%w: failed to resolve symlinks for path %s: %v", ErrPathTraversal, requestedPath, err)
	}

	// Check if the resolved path is within the repository root
	// This prevents directory traversal attacks like ../../../etc/passwd
	// We need to ensure the path is within the repo root, accounting for symlinks
	resolvedRepoRoot, err := filepath.EvalSymlinks(repoRootAbs)
	if err != nil {
		return "", fmt.Errorf("failed to resolve repository root symlinks: %w", err)
	}

	// Normalize both paths for comparison
	resolvedPath = filepath.Clean(resolvedPath)
	resolvedRepoRoot = filepath.Clean(resolvedRepoRoot)

	// Check if resolved path is within resolved repository root
	if !pathWithinRoot(resolvedPath, resolvedRepoRoot) {
		return "", fmt.Errorf("%w: path %s (resolved to %s) is outside repository root %s", ErrPathTraversal, requestedPath, resolvedPath, repoRoot)
	}

	return resolvedPath, nil
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (bw *boundedWriter) isTruncated() bool {
	bw.writer.mutex.Lock()
	defer bw.writer.mutex.Unlock()
	return bw.truncated
}

func (bw *boundedWriter) trimToMaxSizeLocked() {
	if bw.writer.buf.Len() <= bw.maxSize {
		return
	}

	bw.truncated = true

	bytesView := bw.writer.buf.Bytes()
	start := len(bytesView) - bw.maxSize
	for start < len(bytesView) && !utf8.RuneStart(bytesView[start]) {
		start++
	}

	tail := append([]byte(nil), bytesView[start:]...)
	bw.writer.buf.Reset()
	_, _ = bw.writer.buf.Write(tail)
}

func (c *acpClient) CreateTerminal(ctx context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.CreateTerminalResponse{}, err
	}

	// Enforce authorization at point of operation.
	if !c.agent.mutatingOperationsAllowed() {
		if c.agent.effectivePermissionMode() == c.agent.ReadOnlyMode {
			return acp.CreateTerminalResponse{}, fmt.Errorf("terminal creation not permitted in read-only mode")
		}
		return acp.CreateTerminalResponse{}, fmt.Errorf("terminal creation not permitted unless auto-approve mode is explicitly enabled")
	}
	repoRoot := c.effectiveRepoRoot()

	// Set working directory (default to repository root if available).
	cwd := repoRoot
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	if params.Cwd != nil {
		cwd = strings.TrimSpace(*params.Cwd)
		if cwd == "" {
			cwd = repoRoot
			if cwd == "" {
				cwd = "."
			}
		} else if !filepath.IsAbs(cwd) {
			base := repoRoot
			if base == "" {
				base = "."
			}
			cwd = filepath.Join(base, cwd)
		}
	}
	cwd = filepath.Clean(cwd)

	if repoRoot != "" {
		repoRootAbs, err := filepath.Abs(repoRoot)
		if err != nil {
			return acp.CreateTerminalResponse{}, fmt.Errorf("failed to resolve repository root path: %w", err)
		}

		resolvedRepoRoot, err := filepath.EvalSymlinks(repoRootAbs)
		if err != nil {
			return acp.CreateTerminalResponse{}, fmt.Errorf("failed to resolve repository root symlinks: %w", err)
		}
		resolvedRepoRoot = filepath.Clean(resolvedRepoRoot)

		cwdAbs := cwd
		if !filepath.IsAbs(cwdAbs) {
			cwdAbs, err = filepath.Abs(cwdAbs)
			if err != nil {
				return acp.CreateTerminalResponse{}, fmt.Errorf("failed to resolve terminal cwd path %q: %w", cwd, err)
			}
		}

		resolvedCwd, err := filepath.EvalSymlinks(cwdAbs)
		if err != nil {
			return acp.CreateTerminalResponse{}, fmt.Errorf("failed to resolve terminal cwd symlinks for path %q: %w", cwd, err)
		}
		resolvedCwd = filepath.Clean(resolvedCwd)

		if !pathWithinRoot(resolvedCwd, resolvedRepoRoot) {
			return acp.CreateTerminalResponse{}, fmt.Errorf("%w: terminal cwd %s (resolved to %s) is outside repository root %s", ErrPathTraversal, cwd, resolvedCwd, repoRoot)
		}

		cwd = resolvedCwd
	}

	// Terminal lifetime should not be tied to a single CreateTerminal RPC context.
	terminalCtx, cancelFunc := context.WithCancel(context.Background())

	// Build the command with the terminal-specific context
	cmd := exec.CommandContext(terminalCtx, params.Command, params.Args...)
	cmd.Dir = cwd

	// Set environment variables if specified
	if len(params.Env) > 0 {
		env := os.Environ()
		for _, envVar := range params.Env {
			env = append(env, fmt.Sprintf("%s=%s", envVar.Name, envVar.Value))
		}
		cmd.Env = env
	}

	// Create output buffer with mutex for thread safety
	output := &bytes.Buffer{}
	outputMutex := &sync.Mutex{}

	// Create thread-safe writers for stdout and stderr
	threadSafeOutput := &threadSafeWriter{
		buf:   output,
		mutex: outputMutex,
	}

	// Create bounded writer to enforce output limits
	outputLimit := 1024 * 1024 // Default 1MB limit
	if params.OutputByteLimit != nil {
		outputLimit = max(0, *params.OutputByteLimit) // Clamp negative values to 0
	}

	boundedOutput := &boundedWriter{
		writer:    threadSafeOutput,
		maxSize:   outputLimit,
		truncated: false,
	}

	// Set up output capture with bounded writers
	cmd.Stdout = boundedOutput
	cmd.Stderr = boundedOutput

	// Generate terminal ID first (needed for goroutine)
	terminalID := c.generateTerminalID()

	// Create done channel for command completion signaling
	doneChan := make(chan struct{})

	// Start the command
	if err := cmd.Start(); err != nil {
		// Clean up the context if command fails to start
		cancelFunc()
		close(doneChan)
		return acp.CreateTerminalResponse{}, fmt.Errorf("failed to start terminal command: %w", err)
	}

	// Register terminal before starting cleanup goroutine to prevent race conditions
	terminal := &acpTerminal{
		id:              terminalID,
		cmd:             cmd,
		output:          output,
		outputWriter:    boundedOutput,
		context:         terminalCtx,
		cancel:          cancelFunc,
		outputByteLimit: outputLimit,
		truncated:       false,
		done:            doneChan,
	}
	c.addTerminal(terminal)

	// Start a goroutine to wait for the command to finish.
	// Terminal entries stay available until explicit ReleaseTerminal.
	go func() {
		_ = cmd.Wait()
		terminal.setExitStatus(getTerminalExitStatus(cmd.ProcessState))
		// Signal that the command has completed.
		close(doneChan)
	}()

	return acp.CreateTerminalResponse{
		TerminalId: terminalID,
	}, nil
}

func (c *acpClient) KillTerminalCommand(ctx context.Context, params acp.KillTerminalCommandRequest) (acp.KillTerminalCommandResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.KillTerminalCommandResponse{}, err
	}

	// Find and cancel the terminal
	terminal, exists := c.getTerminal(params.TerminalId)

	if !exists {
		return acp.KillTerminalCommandResponse{}, fmt.Errorf("terminal %s not found", params.TerminalId)
	}

	// Use context cancellation for graceful termination
	terminal.cancel()

	return acp.KillTerminalCommandResponse{}, nil
}

func (c *acpClient) TerminalOutput(ctx context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.TerminalOutputResponse{}, err
	}

	// Find the terminal and get output
	terminal, exists := c.getTerminal(params.TerminalId)

	if !exists {
		return acp.TerminalOutputResponse{}, fmt.Errorf("terminal %s not found", params.TerminalId)
	}

	// Apply output truncation if needed
	output, truncated := truncateOutput(terminal.output, terminal.outputByteLimit, terminal.outputWriter.writer.mutex)
	truncated = truncated || terminal.outputWriter.isTruncated()

	// Return exit status only once command completion is fully observed.
	var exitStatus *acp.TerminalExitStatus
	select {
	case <-terminal.done:
		exitStatus = terminal.getExitStatus()
	default:
		exitStatus = nil
	}

	return acp.TerminalOutputResponse{
		Output:     output,
		Truncated:  truncated,
		ExitStatus: exitStatus,
	}, nil
}

func (c *acpClient) ReleaseTerminal(ctx context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.ReleaseTerminalResponse{}, err
	}

	// Get the terminal to access its cancel function
	terminal, exists := c.getTerminal(params.TerminalId)

	if !exists {
		return acp.ReleaseTerminalResponse{}, fmt.Errorf("terminal %s not found", params.TerminalId)
	}

	// Cancel the context to terminate the command gracefully
	terminal.cancel()
	c.removeTerminal(params.TerminalId)

	return acp.ReleaseTerminalResponse{}, nil
}

func (c *acpClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {

	// Validate session ID
	if err := c.validateSessionID(params.SessionId); err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}

	// Find the terminal and get reference
	terminal, exists := c.getTerminal(params.TerminalId)

	if !exists {
		return acp.WaitForTerminalExitResponse{},
			fmt.Errorf("terminal %s not found", params.TerminalId)
	}

	// Wait for the command to finish
	select {
	case <-terminal.done:
		// Command has finished
		exitStatus := terminal.getExitStatus()
		if exitStatus == nil {
			// Command hasn't exited yet (shouldn't happen since we waited for done channel)
			return acp.WaitForTerminalExitResponse{
				ExitCode: nil,
				Signal:   nil,
			}, nil
		}

		return acp.WaitForTerminalExitResponse{
			ExitCode: exitStatus.ExitCode,
			Signal:   exitStatus.Signal,
		}, nil
	case <-ctx.Done():
		// Context was canceled
		return acp.WaitForTerminalExitResponse{}, fmt.Errorf("wait interrupted: %w", ctx.Err())
	}
}

func selectPermissionOptionID(options []acp.PermissionOption, preferredKinds ...acp.PermissionOptionKind) (acp.PermissionOptionId, bool) {
	for _, preferredKind := range preferredKinds {
		for _, option := range options {
			if option.Kind == preferredKind {
				return option.OptionId, true
			}
		}
	}
	return "", false
}

func selectPermissionOutcome(options []acp.PermissionOption, allow bool) acp.RequestPermissionOutcome {
	if allow {
		if optionID, ok := selectPermissionOptionID(options, acp.PermissionOptionKindAllowAlways, acp.PermissionOptionKindAllowOnce); ok {
			return acp.NewRequestPermissionOutcomeSelected(optionID)
		}
	} else {
		if optionID, ok := selectPermissionOptionID(options, acp.PermissionOptionKindRejectAlways, acp.PermissionOptionKindRejectOnce); ok {
			return acp.NewRequestPermissionOutcomeSelected(optionID)
		}
	}

	// Safe fallback when the request does not offer an expected option kind.
	return acp.NewRequestPermissionOutcomeCancelled()
}

// configuredModeIsAvailable checks if the configured mode is available in the list of available modes
// from the ACP agent session response.
func configuredModeIsAvailable(configuredMode string, availablesModes []acp.SessionMode) bool {
	for _, mode := range availablesModes {
		if string(mode.Id) == configuredMode {
			return true
		}
	}
	return false
}

func validateConfiguredMode(configuredMode string, modes *acp.SessionModeState) error {
	if configuredMode == "" {
		return nil
	}
	if modes == nil {
		return fmt.Errorf("agent does not support session modes (configured mode: %s)", configuredMode)
	}
	if !configuredModeIsAvailable(configuredMode, modes.AvailableModes) {
		return fmt.Errorf("mode %s is not available", configuredMode)
	}
	return nil
}

// configuredModelIsAvailable checks if the configured model ID is available in the list of available
// models from the ACP agent session response.
func configuredModelIsAvailable(modelId string, modelInfo []acp.ModelInfo) bool {
	acpModelId := acp.ModelId(modelId)
	for _, m := range modelInfo {
		if m.ModelId == acpModelId {
			return true
		}
	}
	return false
}

func validateConfiguredModel(configuredModel string, models *acp.SessionModelState) error {
	if configuredModel == "" {
		return nil
	}
	if models == nil {
		return fmt.Errorf("agent does not support session models (configured model: %s)", configuredModel)
	}
	if !configuredModelIsAvailable(configuredModel, models.AvailableModels) {
		return fmt.Errorf("model %s is not available", configuredModel)
	}
	return nil
}

func (a *ACPAgent) mutatingOperationsAllowed() bool {
	return a.AutoApproveMode != "" && a.effectivePermissionMode() == a.AutoApproveMode
}

func (a *ACPAgent) effectivePermissionMode() string {
	if strings.TrimSpace(a.Mode) != "" {
		return a.Mode
	}
	if a.Agentic && strings.TrimSpace(a.AutoApproveMode) != "" {
		return a.AutoApproveMode
	}
	return a.ReadOnlyMode
}

func defaultACPAgentConfig() *config.ACPAgentConfig {
	return &config.ACPAgentConfig{
		Name:            defaultACPName,
		Command:         defaultACPCommand,
		Args:            []string{},
		ReadOnlyMode:    defaultACPReadOnlyMode,
		AutoApproveMode: defaultACPAutoApproveMode,
		Mode:            defaultACPReadOnlyMode,
		Model:           "",
		Timeout:         defaultACPTimeoutSeconds,
	}
}

func isConfiguredACPAgentName(name string, cfg *config.Config) bool {
	rawName := strings.TrimSpace(name)
	if rawName == defaultACPName {
		return true
	}
	if cfg == nil || cfg.ACP == nil {
		return false
	}

	configuredName := strings.TrimSpace(cfg.ACP.Name)
	if rawName == "" || configuredName == "" {
		return false
	}

	// Compare both raw and alias-normalized forms so ACP names configured as
	// aliases (for example "claude") still match canonical requests
	// (for example "claude-code").
	if rawName == configuredName {
		return true
	}
	return resolveAlias(rawName) == resolveAlias(configuredName)
}

func configuredACPAgent(cfg *config.Config) *ACPAgent {
	var acpCfg *config.ACPAgentConfig
	if cfg != nil {
		acpCfg = cfg.ACP
	}
	resolved := NewACPAgentFromConfig(acpCfg)
	// Keep a stable canonical name in runtime state.
	resolved.agentName = defaultACPName
	return resolved
}

// GetAvailableWithConfig resolves an available agent while honoring runtime ACP config.
// It treats cfg.ACP.Name as an alias for "acp" and applies cfg.ACP command/mode/model
// at resolution time instead of package-init time.
func GetAvailableWithConfig(preferred string, cfg *config.Config) (Agent, error) {
	preferred = resolveAlias(strings.TrimSpace(preferred))

	if isConfiguredACPAgentName(preferred, cfg) {
		acpAgent := configuredACPAgent(cfg)
		if _, err := exec.LookPath(acpAgent.CommandName()); err == nil {
			return acpAgent, nil
		}
		// ACP requested with an invalid configured command. Try canonical ACP next.
		if canonicalACP, err := Get(defaultACPName); err == nil {
			if commandAgent, ok := canonicalACP.(CommandAgent); !ok {
				return canonicalACP, nil
			} else if _, err := exec.LookPath(commandAgent.CommandName()); err == nil {
				return canonicalACP, nil
			}
		}

		// Finally fall back to normal auto-selection.
		return GetAvailable("")
	}

	resolved, err := GetAvailable(preferred)
	if err != nil {
		return nil, err
	}
	if resolved.Name() == defaultACPName {
		configured := configuredACPAgent(cfg)
		if _, err := exec.LookPath(configured.CommandName()); err == nil {
			return configured, nil
		}
		return resolved, nil
	}
	return resolved, nil
}

func applyACPAgentConfigOverride(cfg *config.ACPAgentConfig, override *config.ACPAgentConfig) {
	if cfg == nil || override == nil {
		return
	}

	if name := strings.TrimSpace(override.Name); name != "" {
		cfg.Name = name
	}
	if command := strings.TrimSpace(override.Command); command != "" {
		cfg.Command = command
	}
	if len(override.Args) > 0 {
		cfg.Args = append([]string(nil), override.Args...)
	}
	if readOnlyMode := strings.TrimSpace(override.ReadOnlyMode); readOnlyMode != "" {
		cfg.ReadOnlyMode = readOnlyMode
	}
	if autoApproveMode := strings.TrimSpace(override.AutoApproveMode); autoApproveMode != "" {
		cfg.AutoApproveMode = autoApproveMode
	}
	if override.DisableModeNegotiation {
		cfg.DisableModeNegotiation = true
	}
	if cfg.DisableModeNegotiation {
		cfg.Mode = ""
	} else if mode := strings.TrimSpace(override.Mode); mode != "" {
		cfg.Mode = mode
	} else {
		// If mode is omitted, default to the effective read-only mode.
		cfg.Mode = cfg.ReadOnlyMode
	}
	if model := strings.TrimSpace(override.Model); model != "" {
		cfg.Model = model
	}
	if override.Timeout > 0 {
		cfg.Timeout = override.Timeout
	}
}

func init() {
	Register(NewACPAgent(""))
}

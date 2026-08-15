package deepseekharness

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const stderrTailLines = 400

// HarnessClientOptions controls how HarnessClient launches the runtime.
// Explicit launch channels are considered in this order:
// LaunchArgsOverride, Command+Args, RuntimeBin, BridgeBin, DSH_RUNTIME_BIN,
// and finally the bundled runtime downloader.
type HarnessClientOptions struct {
	Command                string
	Args                   []string
	RuntimeBin             string
	BridgeBin              string
	LaunchArgsOverride     []string
	CWD                    string
	Env                    map[string]string
	RequestTimeoutSeconds  float64
	ShutdownTimeoutSeconds float64
	RuntimeIndexURL        string
	RuntimeVersion         string
	RuntimeCacheDir        string
}

// HarnessConfig is the Python SDK-compatible name for HarnessClientOptions.
type HarnessConfig = HarnessClientOptions

// DefaultHarnessClientOptions returns the documented client defaults.
func DefaultHarnessClientOptions() HarnessClientOptions {
	return HarnessClientOptions{ShutdownTimeoutSeconds: 1}
}

type resolvedLaunch struct {
	command       string
	args          []string
	defaultConfig string
}

type responseResult struct {
	value any
	err   error
}

type clientState struct {
	mu sync.Mutex

	pending map[string]chan responseResult
	subs    map[uint64]*NotificationSubscription
	parents map[string]string

	unmatched *itemQueue[Notification]
	requests  *itemQueue[IncomingRequest]

	closedErr error
	closed    bool

	cmd         *exec.Cmd
	stdin       io.WriteCloser
	processDone chan struct{}
	stdoutDone  chan struct{}
	stderrDone  chan struct{}
	exitCode    *int
	writeMu     sync.Mutex
	stderrMu    sync.Mutex
	stderrLines []string
}

func newClientState(cmd *exec.Cmd, stdin io.WriteCloser) *clientState {
	return &clientState{
		pending:     make(map[string]chan responseResult),
		subs:        make(map[uint64]*NotificationSubscription),
		parents:     make(map[string]string),
		unmatched:   newItemQueue[Notification](),
		requests:    newItemQueue[IncomingRequest](),
		cmd:         cmd,
		stdin:       stdin,
		processDone: make(chan struct{}),
		stdoutDone:  make(chan struct{}),
		stderrDone:  make(chan struct{}),
	}
}

// HarnessClient is the low-level synchronous JSON-RPC client. It owns one
// runtime subprocess at a time and fans notifications out to subscriptions.
type HarnessClient struct {
	Options HarnessClientOptions

	mu      sync.Mutex
	closeMu sync.Mutex
	state   *clientState
	nextSub uint64
}

// NewHarnessClient builds a lazily-started protocol client.

func NewHarnessClient(config ...HarnessClientOptions) *HarnessClient {
	var options HarnessClientOptions
	if len(config) != 0 {
		options = config[0]
	}
	if options.ShutdownTimeoutSeconds == 0 {
		options.ShutdownTimeoutSeconds = 1
	}
	options.Args = append([]string(nil), options.Args...)
	options.LaunchArgsOverride = cloneStrings(options.LaunchArgsOverride)
	options.Env = cloneStringMap(options.Env)
	return &HarnessClient{Options: options}
}

// Start spawns the runtime and attaches the JSON-RPC stdio transport.
func (c *HarnessClient) Start() error {
	c.mu.Lock()
	if c.state != nil && !c.state.isClosed() {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	launch, err := c.resolveLaunch()
	if err != nil {
		return err
	}
	env, err := c.buildEnvironment(launch)
	if err != nil {
		return err
	}

	command := exec.Command(launch.command, launch.args...)
	command.Env = env
	if c.Options.CWD != "" {
		cwd, err := filepath.Abs(c.Options.CWD)
		if err != nil {
			return &IOError{Err: err}
		}
		command.Dir = cwd
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return &IOError{Err: err}
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return &IOError{Err: err}
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return &IOError{Err: err}
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return &IOError{Err: err}
	}

	state := newClientState(command, stdin)
	c.mu.Lock()
	if c.state != nil && !c.state.isClosed() {
		c.mu.Unlock()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil
	}
	c.state = state
	c.mu.Unlock()

	go c.readerLoop(state, stdout)
	go c.stderrLoop(state, stderr)
	go c.processLoop(state)
	return nil
}

// Close performs protocol shutdown, stdin EOF, termination, and a final kill
// fallback. It is idempotent and always attempts to reap the child process.
func (c *HarnessClient) Close() error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()

	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state == nil {
		return nil
	}

	if !state.isClosed() {
		timeout := c.shutdownTimeout()
		if _, err := c.requestOnState(state, "shutdown", nil, timeout, true); err != nil {
			state.appendStderr("shutdown request failed: " + err.Error())
		}
	}
	_ = state.stdin.Close()

	if !waitFor(state.processDone, c.shutdownTimeout()) {
		terminateProcess(state.cmd)
		if !waitFor(state.processDone, c.shutdownTimeout()) {
			_ = state.cmd.Process.Kill()
			<-state.processDone
		}
	}

	state.failClosed(state.transportError("DeepSeek Harness runtime closed"))
	c.mu.Lock()
	if c.state == state {
		c.state = nil
	}
	c.mu.Unlock()
	return nil
}

// Initialize performs the process-wide handshake.
func (c *HarnessClient) Initialize(params InitializeParams) (InitializeResult, error) {
	cwd := params.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return InitializeResult{}, &IOError{Err: err}
	}
	payload := JSONObject{
		"cwd":      cwd,
		"provider": params.Provider,
		"model":    params.Model,
	}
	if params.MaxTokens > 0 {
		payload["maxTokens"] = params.MaxTokens
	}
	result, err := c.Request("initialize", payload)
	if err != nil {
		_ = c.Close()
		return InitializeResult{}, err
	}
	object, ok := normalizeJSONObject(result)
	if !ok {
		return InitializeResult{}, protocolError("initialize response must be a JSON object")
	}
	parsed := InitializeResult{}
	if rawInfo, exists := object["serverInfo"]; exists && rawInfo != nil {
		info, ok := normalizeJSONObject(rawInfo)
		if !ok {
			return InitializeResult{}, protocolError("initialize serverInfo must be a JSON object")
		}
		parsed.ServerInfo = &ServerInfo{}
		if name, ok := info["name"].(string); ok {
			parsed.ServerInfo.Name = name
		}
		if version, ok := info["version"].(string); ok {
			parsed.ServerInfo.Version = version
		}
	}
	return parsed, nil
}

// SessionPrompt enqueues one message and returns its durable message ID as
// soon as the runtime accepts it.
func (c *HarnessClient) SessionPrompt(sessionID string, contentBlocks []JSONObject) (string, error) {
	result, err := c.Request("session/prompt", JSONObject{
		"sessionId":     sessionID,
		"contentBlocks": contentBlocks,
	})
	if err != nil {
		return "", err
	}
	object, ok := normalizeJSONObject(result)
	if !ok {
		return "", protocolError("session/prompt response must be a JSON object")
	}
	messageID, ok := object["messageId"].(string)
	if !ok || messageID == "" {
		return "", protocolError("session/prompt response requires a string messageId")
	}
	return messageID, nil
}

// Request sends a JSON-RPC request and returns its decoded result.
func (c *HarnessClient) Request(method string, params ...any) (any, error) {
	if len(params) > 1 {
		return nil, fmt.Errorf("Request accepts at most one params value")
	}
	var payload any
	if len(params) == 1 {
		payload = params[0]
	}
	timeout, hasTimeout := c.requestTimeout()
	return c.requestWithTimeout(method, payload, timeout, hasTimeout)
}

// RequestAs sends a request and decodes its result into T. It is the generic
// counterpart to the Python SDK's response_model argument.
func RequestAs[T any](client *HarnessClient, method string, params ...any) (T, error) {
	var result T
	raw, err := client.Request(method, params...)
	if err != nil {
		return result, err
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return result, &IOError{Err: err}
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, protocolError(fmt.Sprintf("%s response could not be decoded: %v", method, err))
	}
	return result, nil
}

// Notify sends a JSON-RPC notification frame.
func (c *HarnessClient) Notify(method string, params ...any) error {
	if len(params) > 1 {
		return fmt.Errorf("Notify accepts at most one params value")
	}
	state, err := c.currentState()
	if err != nil {
		return err
	}
	message := JSONObject{"jsonrpc": "2.0", "method": method}
	if len(params) == 1 && params[0] != nil {
		message["params"] = params[0]
	}
	return c.writeMessage(state, message)
}

// NextNotification waits for a notification that no active subscription
// matched.
func (c *HarnessClient) NextNotification() (Notification, error) {
	state, err := c.currentState()
	if err != nil {
		return Notification{}, err
	}
	return state.unmatched.get()
}

// Subscribe registers a notification subscription. A nil filter matches all
// notifications. Each matching notification is delivered once to the
// subscription, even if multiple subscriptions match it.
func (c *HarnessClient) Subscribe(filters ...NotificationFilter) (*NotificationSubscription, error) {
	if len(filters) > 1 {
		return nil, fmt.Errorf("Subscribe accepts at most one filter")
	}
	var filter NotificationFilter
	if len(filters) == 1 {
		filter = filters[0]
	}
	state, err := c.currentState()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.nextSub++
	id := c.nextSub
	c.mu.Unlock()
	subscription := &NotificationSubscription{
		client: c,
		state:  state,
		id:     id,
		filter: filter,
		queue:  newItemQueue[Notification](),
	}
	state.mu.Lock()
	if state.closed {
		closedErr := state.closedErr
		state.mu.Unlock()
		return nil, closedErr
	}
	state.subs[id] = subscription
	state.mu.Unlock()
	return subscription, nil
}

// SubscribeSessionTree subscribes to a session and all descendants discovered
// through subagent.started lineage notifications.
func (c *HarnessClient) SubscribeSessionTree(sessionID string) (*NotificationSubscription, error) {
	state, err := c.currentState()
	if err != nil {
		return nil, err
	}
	root := sessionID
	return c.Subscribe(func(notification Notification) bool {
		payload := notification.Payload
		if notification.Method == "subagent.started" || notification.Method == "subagent.finished" {
			if parent, ok := payload["parentSessionId"].(string); ok && state.isDescendant(parent, root) {
				return true
			}
			return payload["childSessionId"] == root
		}
		related, ok := payload["sessionId"].(string)
		return ok && state.isDescendant(related, root)
	})
}

// NextRequest waits for a server-to-client request.
func (c *HarnessClient) NextRequest() (IncomingRequest, error) {
	state, err := c.currentState()
	if err != nil {
		return IncomingRequest{}, err
	}
	return state.requests.get()
}

// Respond answers a server-to-client request with a result value.
func (c *HarnessClient) Respond(requestID any, result any) error {
	state, err := c.currentState()
	if err != nil {
		return err
	}
	return c.writeMessage(state, JSONObject{
		"jsonrpc": "2.0",
		"id":      requestID,
		"result":  result,
	})
}

// RespondError answers a server-to-client request with a JSON-RPC error.
func (c *HarnessClient) RespondError(requestID any, code int, message string, data ...any) error {
	if len(data) > 1 {
		return fmt.Errorf("RespondError accepts at most one data value")
	}
	state, err := c.currentState()
	if err != nil {
		return err
	}
	errorObject := JSONObject{"code": code, "message": message}
	if len(data) == 1 && data[0] != nil {
		errorObject["data"] = data[0]
	}
	return c.writeMessage(state, JSONObject{
		"jsonrpc": "2.0",
		"id":      requestID,
		"error":   errorObject,
	})
}

func (c *HarnessClient) requestWithTimeout(method string, params any, timeout time.Duration, hasTimeout bool) (any, error) {
	state, err := c.currentState()
	if err != nil {
		return nil, err
	}
	id := newRequestID()
	waiter := make(chan responseResult, 1)
	state.mu.Lock()
	if state.closed {
		err := state.closedErr
		state.mu.Unlock()
		return nil, err
	}
	state.pending[id] = waiter
	state.mu.Unlock()

	message := JSONObject{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := c.writeMessage(state, message); err != nil {
		state.removePending(id)
		return nil, err
	}

	var timer *time.Timer
	var timeoutChannel <-chan time.Time
	if hasTimeout {
		timer = time.NewTimer(timeout)
		timeoutChannel = timer.C
		defer timer.Stop()
	}
	select {
	case result := <-waiter:
		return result.value, result.err
	case <-timeoutChannel:
		state.removePending(id)
		return nil, requestTimeoutError(method, state.diagnostics())
	}
}

func (c *HarnessClient) requestOnState(state *clientState, method string, params any, timeout time.Duration, hasTimeout bool) (any, error) {
	// Close uses the same correlation path but must keep using its original
	// state even while another goroutine is clearing the client pointer.
	id := newRequestID()
	waiter := make(chan responseResult, 1)
	state.mu.Lock()
	if state.closed {
		err := state.closedErr
		state.mu.Unlock()
		return nil, err
	}
	state.pending[id] = waiter
	state.mu.Unlock()
	message := JSONObject{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := c.writeMessage(state, message); err != nil {
		state.removePending(id)
		return nil, err
	}
	var timer *time.Timer
	var timeoutChannel <-chan time.Time
	if hasTimeout {
		timer = time.NewTimer(timeout)
		timeoutChannel = timer.C
		defer timer.Stop()
	}
	select {
	case result := <-waiter:
		return result.value, result.err
	case <-timeoutChannel:
		state.removePending(id)
		return nil, requestTimeoutError(method, state.diagnostics())
	}
}

func (c *HarnessClient) currentState() (*clientState, error) {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state == nil {
		return nil, transportClosedError(ErrClientNotRunning.Error(), nil, nil)
	}
	state.mu.Lock()
	closed := state.closed
	err := state.closedErr
	state.mu.Unlock()
	if closed {
		if err != nil {
			return nil, err
		}
		return nil, transportClosedError("DeepSeek Harness runtime closed", nil, nil)
	}
	return state, nil
}

func (c *HarnessClient) writeMessage(state *clientState, message JSONObject) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return &IOError{Err: err}
	}
	encoded = append(encoded, '\n')
	state.mu.Lock()
	if state.closed {
		err := state.closedErr
		state.mu.Unlock()
		return err
	}
	stdin := state.stdin
	state.mu.Unlock()
	// The process state lock only protects selecting the current stdin. The
	// write itself is serialized so concurrent requests cannot interleave bytes.
	return c.writeMu(state, stdin, encoded)
}

// writeMu keeps the lock outside clientState so writes from multiple callers
// remain frame-atomic without blocking state inspection.
func (c *HarnessClient) writeMu(state *clientState, stdin io.WriteCloser, frame []byte) error {
	state.writeMu.Lock()
	defer state.writeMu.Unlock()
	if _, err := stdin.Write(frame); err != nil {
		wrapped := state.transportError("Failed to write to DeepSeek Harness runtime")
		state.failClosed(wrapped)
		return wrapped
	}
	return nil
}

func (c *HarnessClient) readerLoop(state *clientState, stdout io.ReadCloser) {
	defer close(state.stdoutDone)
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) != 0 {
			c.handleFrame(state, line)
		}
		if err != nil {
			break
		}
	}
	select {
	case <-state.processDone:
	case <-time.After(100 * time.Millisecond):
	}
	state.failClosed(state.transportError("DeepSeek Harness runtime stdout closed"))
}

func (c *HarnessClient) stderrLoop(state *clientState, stderr io.ReadCloser) {
	defer close(state.stderrDone)
	reader := bufio.NewScanner(stderr)
	reader.Buffer(make([]byte, 4096), 1024*1024)
	for reader.Scan() {
		state.appendStderr(reader.Text())
	}
}

func (c *HarnessClient) processLoop(state *clientState) {
	err := state.cmd.Wait()
	var exitCode *int
	if err == nil {
		code := 0
		exitCode = &code
	} else if exitError, ok := err.(*exec.ExitError); ok {
		code := exitError.ExitCode()
		exitCode = &code
	}
	state.mu.Lock()
	state.exitCode = cloneIntPointer(exitCode)
	state.mu.Unlock()
	close(state.processDone)
	select {
	case <-state.stderrDone:
	case <-time.After(100 * time.Millisecond):
	}
	state.failClosed(state.transportError("DeepSeek Harness runtime stdout closed"))
}

func (c *HarnessClient) handleFrame(state *clientState, frame []byte) {
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.UseNumber()
	var raw any
	if err := decoder.Decode(&raw); err != nil {
		// The protocol deliberately ignores garbage lines, matching both
		// reference SDKs and allowing child-process diagnostics on stdout.
		return
	}
	message, ok := raw.(map[string]any)
	if !ok {
		return
	}
	id, hasID := message["id"]
	method, hasMethod := message["method"].(string)
	switch {
	case hasID && hasMethod:
		state.requests.put(IncomingRequest{
			ID:      normalizeID(id),
			Method:  method,
			Payload: objectParams(message["params"]),
		})
	case hasID:
		pending := state.takePending(idKey(id))
		if pending == nil {
			return
		}
		if rawError, ok := normalizeJSONObject(message["error"]); ok {
			var code *int
			if value, ok := integerValue(rawError["code"]); ok {
				code = &value
			}
			messageText, _ := rawError["message"].(string)
			if messageText == "" {
				messageText = "JSON-RPC error"
			}
			pending <- responseResult{err: &JsonRpcError{
				Code:    code,
				Message: messageText,
				Data:    rawError["data"],
			}}
			return
		}
		pending <- responseResult{value: message["result"]}
	case hasMethod:
		notification := Notification{Method: method, Payload: objectParams(message["params"])}
		state.recordLineage(notification)
		state.mu.Lock()
		subscribers := make([]*NotificationSubscription, 0, len(state.subs))
		for _, subscription := range state.subs {
			subscribers = append(subscribers, subscription)
		}
		state.mu.Unlock()
		delivered := false
		for _, subscription := range subscribers {
			matches, err := subscription.matches(notification)
			if err != nil {
				subscription.queue.closeWithError(err)
				state.removeSubscription(subscription.id)
				continue
			}
			if matches {
				subscription.queue.put(notification)
				delivered = true
			}
		}
		if !delivered {
			state.unmatched.put(notification)
		}
	}
}

func objectParams(value any) JSONObject {
	if object, ok := normalizeJSONObject(value); ok {
		return object
	}
	return JSONObject{}
}

func (s *clientState) recordLineage(notification Notification) {
	if notification.Method != "subagent.started" {
		return
	}
	parent, parentOK := notification.Payload["parentSessionId"].(string)
	child, childOK := notification.Payload["childSessionId"].(string)
	if !parentOK || !childOK || parent == "" || child == "" || parent == child {
		return
	}
	s.mu.Lock()
	s.parents[child] = parent
	s.mu.Unlock()
}

func (s *clientState) isDescendant(sessionID, root string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	visited := make(map[string]struct{})
	current := sessionID
	for {
		if current == root {
			return true
		}
		if _, ok := visited[current]; ok {
			return false
		}
		visited[current] = struct{}{}
		parent, ok := s.parents[current]
		if !ok {
			return false
		}
		current = parent
	}
}

func (s *clientState) takePending(id string) chan responseResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiter := s.pending[id]
	delete(s.pending, id)
	return waiter
}

func (s *clientState) removePending(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *clientState) removeSubscription(id uint64) {
	s.mu.Lock()
	delete(s.subs, id)
	s.mu.Unlock()
}

func (s *clientState) isClosed() bool {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	return closed
}

func (s *clientState) failClosed(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.closedErr = err
	pending := make([]chan responseResult, 0, len(s.pending))
	for id, waiter := range s.pending {
		pending = append(pending, waiter)
		delete(s.pending, id)
	}
	subscribers := make([]*NotificationSubscription, 0, len(s.subs))
	for id, subscription := range s.subs {
		subscribers = append(subscribers, subscription)
		delete(s.subs, id)
	}
	s.mu.Unlock()
	for _, waiter := range pending {
		waiter <- responseResult{err: err}
	}
	for _, subscription := range subscribers {
		subscription.queue.closeWithError(err)
	}
	s.unmatched.closeWithError(err)
	s.requests.closeWithError(err)
}

func (s *clientState) appendStderr(line string) {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	if len(s.stderrLines) >= stderrTailLines {
		copy(s.stderrLines, s.stderrLines[1:])
		s.stderrLines = s.stderrLines[:stderrTailLines-1]
	}
	s.stderrLines = append(s.stderrLines, line)
}

func (s *clientState) diagnostics() string {
	s.mu.Lock()
	exitCode := cloneIntPointer(s.exitCode)
	s.mu.Unlock()
	s.stderrMu.Lock()
	tail := append([]string(nil), s.stderrLines...)
	s.stderrMu.Unlock()
	parts := make([]string, 0, 2)
	if exitCode != nil {
		parts = append(parts, fmt.Sprintf("exit code: %d", *exitCode))
	}
	if len(tail) != 0 {
		parts = append(parts, "stderr tail:\n"+strings.Join(tail, "\n"))
	}
	return strings.Join(parts, "\n")
}

func (s *clientState) transportError(reason string) error {
	s.mu.Lock()
	exitCode := cloneIntPointer(s.exitCode)
	s.mu.Unlock()
	s.stderrMu.Lock()
	tail := append([]string(nil), s.stderrLines...)
	s.stderrMu.Unlock()
	return transportClosedError(reason, exitCode, tail)
}

// NotificationSubscription is a session-independent notification stream.
type NotificationSubscription struct {
	client *HarnessClient
	state  *clientState
	id     uint64
	filter NotificationFilter
	queue  *itemQueue[Notification]
	mu     sync.Mutex
	closed bool
}

func (s *NotificationSubscription) matches(notification Notification) (matches bool, err error) {
	// The filter is user code. Turn a panic into a terminal subscription error
	// instead of taking down the runtime reader goroutine.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("notification filter panicked: %v", recovered)
		}
	}()
	return s.filter == nil || s.filter(notification), nil
}

// Next waits for the next matching notification or the terminal transport
// error. Notifications already queued are delivered before that error.
func (s *NotificationSubscription) Next() (Notification, error) {
	return s.queue.get()
}

// TryNext polls without blocking. The bool is false when no notification is
// currently queued; a terminal error is returned separately.
func (s *NotificationSubscription) TryNext() (Notification, bool, error) {
	return s.queue.tryGet()
}

// Drain delivers all currently queued notifications to callback.
func (s *NotificationSubscription) Drain(callback func(Notification)) error {
	for {
		notification, ok, err := s.TryNext()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		callback(notification)
	}
}

// Close unregisters the subscription. It is idempotent.
func (s *NotificationSubscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.state.removeSubscription(s.id)
	s.queue.closeWithError(ErrSubscriptionClosed)
}

// IsClosed reports whether Close was called.
func (s *NotificationSubscription) IsClosed() bool {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	return closed
}

func (c *HarnessClient) requestTimeout() (time.Duration, bool) {
	if c.Options.RequestTimeoutSeconds <= 0 {
		return 0, false
	}
	return time.Duration(c.Options.RequestTimeoutSeconds * float64(time.Second)), true
}

func (c *HarnessClient) shutdownTimeout() time.Duration {
	seconds := c.Options.ShutdownTimeoutSeconds
	if seconds <= 0 {
		seconds = 1
	}
	return time.Duration(seconds * float64(time.Second))
}

func terminateProcess(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	if runtime.GOOS != "windows" {
		if err := command.Process.Signal(syscall.SIGTERM); err == nil {
			return
		}
	}
	_ = command.Process.Kill()
}

func waitFor(done <-chan struct{}, timeout time.Duration) bool {
	if timeout <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%d", atomic.AddInt64(&fallbackRequestID, 1))
}

var fallbackRequestID int64

func normalizeID(value any) any {
	if number, ok := value.(json.Number); ok {
		if integer, err := strconv.ParseInt(string(number), 10, 64); err == nil {
			return integer
		}
		return string(number)
	}
	return value
}

func idKey(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return string(typed)
	case int:
		return strconv.Itoa(typed)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		return fmt.Sprint(typed)
	}
}

func integerValue(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(string(typed))
		return parsed, err == nil
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	default:
		return 0, false
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

type itemQueue[T any] struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []T
	closed   bool
	closeErr error
}

func newItemQueue[T any]() *itemQueue[T] {
	queue := &itemQueue[T]{}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *itemQueue[T]) put(item T) {
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, item)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

func (q *itemQueue[T]) closeWithError(err error) {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.closeErr = err
	}
	q.cond.Broadcast()
	q.mu.Unlock()
}

func (q *itemQueue[T]) get() (T, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) != 0 {
		item := q.items[0]
		q.items = q.items[1:]
		return item, nil
	}
	var zero T
	return zero, q.closeErr
}

func (q *itemQueue[T]) tryGet() (T, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) != 0 {
		item := q.items[0]
		q.items = q.items[1:]
		return item, true, nil
	}
	var zero T
	if q.closed {
		return zero, false, q.closeErr
	}
	return zero, false, nil
}

func (c *HarnessClient) resolveLaunch() (resolvedLaunch, error) {
	options := c.Options
	if options.LaunchArgsOverride != nil {
		if len(options.LaunchArgsOverride) == 0 {
			return resolvedLaunch{}, runtimeResolveError("launch_args_override must not be empty")
		}
		return resolvedLaunch{
			command: options.LaunchArgsOverride[0],
			args:    cloneStrings(options.LaunchArgsOverride[1:]),
		}, nil
	}
	if options.Command != "" {
		return resolvedLaunch{command: options.Command, args: cloneStrings(options.Args)}, nil
	}
	if options.RuntimeBin != "" {
		return resolvedLaunch{command: options.RuntimeBin}, nil
	}
	if options.BridgeBin != "" {
		return resolvedLaunch{command: options.BridgeBin}, nil
	}
	if value := os.Getenv("DSH_RUNTIME_BIN"); value != "" {
		return resolvedLaunch{command: value}, nil
	}
	resolved, err := ResolveRuntime(ResolveOptions{
		IndexURL: options.RuntimeIndexURL,
		Version:  options.RuntimeVersion,
		CacheDir: options.RuntimeCacheDir,
	})
	if err != nil {
		return resolvedLaunch{}, err
	}
	if len(resolved.LaunchArgs) == 0 {
		return resolvedLaunch{}, runtimeResolveError("runtime resolver returned empty launch args")
	}
	return resolvedLaunch{
		command:       resolved.LaunchArgs[0],
		args:          cloneStrings(resolved.LaunchArgs[1:]),
		defaultConfig: resolved.DefaultConfig,
	}, nil
}

func (c *HarnessClient) buildEnvironment(launch resolvedLaunch) ([]string, error) {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range c.Options.Env {
		env[key] = value
	}
	if launch.defaultConfig != "" && env["DSH_CORDIS_CONFIG"] == "" {
		env["DSH_CORDIS_CONFIG"] = launch.defaultConfig
	}
	result := make([]string, 0, len(env))
	for key, value := range env {
		result = append(result, key+"="+value)
	}
	return result, nil
}

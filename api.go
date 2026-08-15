package deepseekharness

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DeepSeekHarnessConfig configures the high-level owned-runtime API.
// Runtime and protocol options intentionally mirror HarnessClientOptions so a
// caller can switch between the high- and low-level surfaces without changing
// launch behavior.
type DeepSeekHarnessConfig struct {
	Provider               string
	Model                  string
	MaxTokens              int
	CWD                    string
	RuntimeCWD             string
	SessionRoot            string
	Cordis                 string
	Env                    map[string]string
	RuntimeBin             string
	BridgeBin              string
	Command                string
	Args                   []string
	LaunchArgsOverride     []string
	RequestTimeoutSeconds  float64
	ShutdownTimeoutSeconds float64
	BaseURL                string
	APIKey                 string
	RuntimeIndexURL        string
	RuntimeVersion         string
	RuntimeCacheDir        string
}

// DefaultDeepSeekHarnessConfig returns the documented SDK defaults.
// MaxTokens==0 means no explicit output-token cap.
func DefaultDeepSeekHarnessConfig() DeepSeekHarnessConfig {
	return DeepSeekHarnessConfig{
		Provider:               "deepseek-official",
		Model:                  "deepseek-v4-flash",
		ShutdownTimeoutSeconds: 1,
	}
}

type harnessInner struct {
	config      DeepSeekHarnessConfig
	client      *HarnessClient
	cwd         string
	startMu     sync.Mutex
	initialized bool
}

// DeepSeekHarness is a reusable synchronous high-level SDK. The runtime is
// started lazily and kept alive across Run calls until Close.
type DeepSeekHarness struct{ inner *harnessInner }

// NewDeepSeekHarness creates a harness. Calling it without an argument uses
// DefaultDeepSeekHarnessConfig.
func NewDeepSeekHarness(config ...DeepSeekHarnessConfig) *DeepSeekHarness {
	resolved := DefaultDeepSeekHarnessConfig()
	if len(config) != 0 {
		resolved = mergeHarnessConfig(resolved, config[0])
	}
	cwd := resolved.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, _ = filepath.Abs(cwd)
	runtimeCWD := resolved.RuntimeCWD
	if runtimeCWD == "" {
		runtimeCWD = cwd
	} else {
		runtimeCWD, _ = filepath.Abs(runtimeCWD)
	}

	env := cloneStringMap(resolved.Env)
	if env == nil {
		env = make(map[string]string)
	}
	if resolved.SessionRoot != "" {
		env["DSH_SESSION_ROOT"] = resolved.SessionRoot
	}
	if resolved.Cordis != "" {
		env["DSH_CORDIS_CONFIG"] = resolved.Cordis
	}
	env["DSH_CWD"] = cwd
	if resolved.BaseURL != "" {
		env["DEEPSEEK_BASE_URL"] = resolved.BaseURL
	}
	if resolved.APIKey != "" {
		env["DEEPSEEK_API_KEY"] = resolved.APIKey
	}

	client := NewHarnessClient(HarnessClientOptions{
		Command:                resolved.Command,
		Args:                   resolved.Args,
		RuntimeBin:             resolved.RuntimeBin,
		BridgeBin:              resolved.BridgeBin,
		LaunchArgsOverride:     resolved.LaunchArgsOverride,
		CWD:                    runtimeCWD,
		Env:                    env,
		RequestTimeoutSeconds:  resolved.RequestTimeoutSeconds,
		ShutdownTimeoutSeconds: resolved.ShutdownTimeoutSeconds,
		RuntimeIndexURL:        resolved.RuntimeIndexURL,
		RuntimeVersion:         resolved.RuntimeVersion,
		RuntimeCacheDir:        resolved.RuntimeCacheDir,
	})
	return &DeepSeekHarness{inner: &harnessInner{
		config: resolved,
		client: client,
		cwd:    cwd,
	}}
}

// Start launches the runtime and performs the memoized initialize handshake.
func (h *DeepSeekHarness) Start() error {
	h.inner.startMu.Lock()
	defer h.inner.startMu.Unlock()
	if h.inner.initialized {
		return nil
	}
	if err := h.inner.client.Start(); err != nil {
		return err
	}
	_, err := h.inner.client.Initialize(InitializeParams{
		CWD:       h.inner.cwd,
		Provider:  h.inner.config.Provider,
		Model:     h.inner.config.Model,
		MaxTokens: h.inner.config.MaxTokens,
	})
	if err != nil {
		_ = h.inner.client.Close()
		return err
	}
	h.inner.initialized = true
	return nil
}

// Close shuts down the owned runtime. It is safe to call more than once.
func (h *DeepSeekHarness) Close() error {
	h.inner.startMu.Lock()
	defer h.inner.startMu.Unlock()
	err := h.inner.client.Close()
	h.inner.initialized = false
	return err
}

// StartSession returns a reusable named session. An empty ID creates a fresh
// session ID with the same session-<uuid> shape as the Python SDK.
func (h *DeepSeekHarness) StartSession(sessionIDs ...string) (*Session, error) {
	if err := h.Start(); err != nil {
		return nil, err
	}
	var sessionID string
	if len(sessionIDs) != 0 {
		sessionID = sessionIDs[0]
	}
	if len(sessionIDs) > 1 {
		return nil, fmt.Errorf("StartSession accepts at most one session ID")
	}
	if sessionID == "" {
		sessionID = "session-" + newRequestID()
	}
	return &Session{Harness: h, ID: sessionID}, nil
}

// Run owns one prompt-to-idle activity interval. Options are optional; at
// most one RunOptions value is accepted.
func (h *DeepSeekHarness) Run(input any, options ...*RunOptions) (*RunResult, error) {
	var resolved RunOptions
	if len(options) > 1 {
		return nil, fmt.Errorf("Run accepts at most one RunOptions value")
	}
	if len(options) == 1 && options[0] != nil {
		resolved = *options[0]
	}
	session, err := h.StartSession(resolved.SessionID)
	if err != nil {
		return nil, err
	}
	return session.run(input, resolved.OnNotification)
}

// RunWithOptions is an explicit alternative to the optional Run argument.
func (h *DeepSeekHarness) RunWithOptions(input any, options RunOptions) (*RunResult, error) {
	return h.Run(input, &options)
}

// Client exposes the low-level protocol client for advanced workflows.
func (h *DeepSeekHarness) Client() *HarnessClient { return h.inner.client }

// Config returns a copy of the applied high-level configuration.
func (h *DeepSeekHarness) Config() DeepSeekHarnessConfig { return h.inner.config }

// Session is a named handle over one DeepSeek Harness session.
type Session struct {
	Harness *DeepSeekHarness
	ID      string
}

// Run executes one prompt on this session. The optional callback receives the
// notifications collected after the durable inbox receipt.
func (s *Session) Run(input any, callbacks ...func(Notification)) (*RunResult, error) {
	var callback func(Notification)
	if len(callbacks) > 1 {
		return nil, fmt.Errorf("Session.Run accepts at most one notification callback")
	}
	if len(callbacks) == 1 {
		callback = callbacks[0]
	}
	return s.run(input, callback)
}

// RunWithOptions runs this session with an explicit RunOptions value. The
// SessionID field is ignored because the handle already owns its ID.
func (s *Session) RunWithOptions(input any, options RunOptions) (*RunResult, error) {
	return s.run(input, options.OnNotification)
}

func (s *Session) run(input any, callback func(Notification)) (*RunResult, error) {
	if s == nil || s.Harness == nil {
		return nil, fmt.Errorf("session has no harness")
	}
	blocks, err := contentBlocks(input)
	if err != nil {
		return nil, err
	}
	subscription, err := s.Harness.inner.client.SubscribeSessionTree(s.ID)
	if err != nil {
		return nil, err
	}
	defer subscription.Close()
	messageID, err := s.Harness.inner.client.SessionPrompt(s.ID, blocks)
	if err != nil {
		return nil, err
	}

	notifications := make([]Notification, 0)
	events := make([]JSONObject, 0)
	received := false
	for {
		notification, err := subscription.Next()
		if err != nil {
			return nil, err
		}
		if !received {
			if !isInboxReceipt(notification, s.ID, messageID) {
				continue
			}
			received = true
		}
		if callback != nil {
			callback(notification)
		}
		if notification.Method == "session.event" && notification.Payload["sessionId"] == s.ID {
			if event, ok := normalizeJSONObject(notification.Payload["event"]); ok {
				events = append(events, event)
			}
		}
		notifications = append(notifications, notification)
		if notification.Method == "session.status" &&
			notification.Payload["sessionId"] == s.ID &&
			notification.Payload["status"] == "idle" {
			break
		}
	}

	finishReason, err := FinishReason(events)
	if err != nil {
		return nil, err
	}
	return &RunResult{
		SessionID:     s.ID,
		FinalResponse: FinalResponse(events),
		FinishReason:  finishReason,
		Events:        events,
		Notifications: notifications,
		SessionRoot:   s.Harness.inner.config.SessionRoot,
	}, nil
}

func mergeHarnessConfig(defaults, supplied DeepSeekHarnessConfig) DeepSeekHarnessConfig {
	// The zero value is useful as a concise config literal, so only fields with
	// meaningful zero values are copied over the documented defaults.
	if supplied.Provider != "" {
		defaults.Provider = supplied.Provider
	}
	if supplied.Model != "" {
		defaults.Model = supplied.Model
	}
	if supplied.MaxTokens != 0 {
		defaults.MaxTokens = supplied.MaxTokens
	}
	if supplied.CWD != "" {
		defaults.CWD = supplied.CWD
	}
	if supplied.RuntimeCWD != "" {
		defaults.RuntimeCWD = supplied.RuntimeCWD
	}
	if supplied.SessionRoot != "" {
		defaults.SessionRoot = supplied.SessionRoot
	}
	if supplied.Cordis != "" {
		defaults.Cordis = supplied.Cordis
	}
	if supplied.Env != nil {
		defaults.Env = supplied.Env
	}
	if supplied.RuntimeBin != "" {
		defaults.RuntimeBin = supplied.RuntimeBin
	}
	if supplied.BridgeBin != "" {
		defaults.BridgeBin = supplied.BridgeBin
	}
	if supplied.Command != "" {
		defaults.Command = supplied.Command
	}
	if supplied.Args != nil {
		defaults.Args = supplied.Args
	}
	if supplied.LaunchArgsOverride != nil {
		defaults.LaunchArgsOverride = supplied.LaunchArgsOverride
	}
	if supplied.RequestTimeoutSeconds != 0 {
		defaults.RequestTimeoutSeconds = supplied.RequestTimeoutSeconds
	}
	if supplied.ShutdownTimeoutSeconds != 0 {
		defaults.ShutdownTimeoutSeconds = supplied.ShutdownTimeoutSeconds
	}
	if supplied.BaseURL != "" {
		defaults.BaseURL = supplied.BaseURL
	}
	if supplied.APIKey != "" {
		defaults.APIKey = supplied.APIKey
	}
	if supplied.RuntimeIndexURL != "" {
		defaults.RuntimeIndexURL = supplied.RuntimeIndexURL
	}
	if supplied.RuntimeVersion != "" {
		defaults.RuntimeVersion = supplied.RuntimeVersion
	}
	if supplied.RuntimeCacheDir != "" {
		defaults.RuntimeCacheDir = supplied.RuntimeCacheDir
	}
	return defaults
}

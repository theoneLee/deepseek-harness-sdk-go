package deepseekharness

import (
	"bytes"
	"encoding/json"
)

// JSONValue is a decoded JSON value. Object values use string keys and are
// represented by JSONObject.
type JSONValue = any

// JSONObject is the shape used by JSON-RPC params and notification payloads.
type JSONObject map[string]any

// JsonObject is kept as an alternate spelling for callers mirroring the
// Python SDK's type alias.
type JsonObject = JSONObject

// Notification is one server-to-client JSON-RPC notification. Non-object
// params are normalized to an empty object, matching the Python SDK contract.
type Notification struct {
	Method  string
	Payload JSONObject
}

// IncomingRequest is a server-to-client JSON-RPC request. The ID is retained
// as a JSON-compatible Go value so it can be sent back verbatim with Respond.
type IncomingRequest struct {
	ID      any
	Method  string
	Payload JSONObject
}

// ServerInfo is the runtime identity returned by initialize.
type ServerInfo struct {
	Name    string
	Version string
}

// InitializeResult is the parsed initialize response. ServerInfo is nil when
// the runtime omitted the optional serverInfo object.
type InitializeResult struct {
	ServerInfo *ServerInfo
}

// InitializeResponse is an alias used by the Python SDK naming.
type InitializeResponse = InitializeResult

// InitializeParams configures the process-wide initialize handshake. A zero
// MaxTokens omits maxTokens from the wire payload.
type InitializeParams struct {
	CWD       string
	Provider  string
	Model     string
	MaxTokens int
}

// NotificationFilter decides whether a notification is delivered to a
// subscription. A nil filter matches every notification.
type NotificationFilter func(Notification) bool

// RunOptions controls one high-level run interval.
type RunOptions struct {
	SessionID      string
	OnNotification func(Notification)
}

// RunResult is the activity interval collected by DeepSeekHarness.Run.
type RunResult struct {
	SessionID     string
	FinalResponse string
	FinishReason  *string
	Events        []JSONObject
	Notifications []Notification
	SessionRoot   string
}

// Input makes structured prompt input explicit when callers do not want to
// pass a string or []JSONObject directly to Run.
type Input struct {
	Text   *string
	Blocks []JSONObject
}

// TextInput creates one text content block.
func TextInput(text string) Input { return Input{Text: &text} }

// BlocksInput sends content blocks verbatim.
func BlocksInput(blocks []JSONObject) Input { return Input{Blocks: blocks} }

// RawJSONObject converts a decoded JSON object into a JSONObject without
// changing its values. It is useful at API boundaries receiving map values.
func RawJSONObject(value any) (JSONObject, bool) {
	if object, ok := value.(JSONObject); ok {
		return object, true
	}
	object, ok := value.(map[string]any)
	return JSONObject(object), ok
}

// normalizeJSONObject is used internally for response validation.
func normalizeJSONObject(value any) (JSONObject, bool) {
	if object, ok := value.(JSONObject); ok {
		return object, true
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return JSONObject(object), true
}

// cloneJSONValue round-trips values that may have been supplied using typed
// maps. Most public paths already use map[string]any and take the fast path.
func cloneJSONValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if object, ok := value.(JSONObject); ok {
		return object, nil
	}
	if object, ok := value.(map[string]any); ok {
		return JSONObject(object), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

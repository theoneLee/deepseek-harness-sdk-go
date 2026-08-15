package deepseekharness

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrClientNotRunning is returned when a low-level operation needs a
	// runtime that has not been started or has already been closed.
	ErrClientNotRunning = errors.New("DeepSeek Harness runtime is not running")
	// ErrSubscriptionClosed is returned after a notification subscription is
	// explicitly closed by its owner.
	ErrSubscriptionClosed = errors.New("DeepSeek Harness notification subscription is closed")
)

// JsonRpcError reports a JSON-RPC error response from the runtime.
type JsonRpcError struct {
	Code    *int
	Message string
	Data    any
}

func (e *JsonRpcError) Error() string { return e.Message }

// JsonRPCError is the acronym-styled spelling of JsonRpcError.
type JsonRPCError = JsonRpcError

// ProtocolError reports a documented-protocol violation.
type ProtocolError struct{ Message string }

func (e *ProtocolError) Error() string { return e.Message }

// SdkProtocolError is the name used by the Python SDK for ProtocolError.
type SdkProtocolError = ProtocolError

// RequestTimeoutError reports a request deadline that elapsed before the
// runtime answered.
type RequestTimeoutError struct{ Message string }

func (e *RequestTimeoutError) Error() string { return e.Message }

// TransportClosedError reports a runtime process or stdio transport failure.
// ExitCode is nil when the process status was not available.
type TransportClosedError struct {
	Message    string
	ExitCode   *int
	StderrTail []string
}

func (e *TransportClosedError) Error() string { return e.Message }

// RuntimeResolveError reports a failure to locate or download the bundled
// runtime executable.
type RuntimeResolveError struct{ Message string }

func (e *RuntimeResolveError) Error() string { return e.Message }

// IOError wraps an I/O failure while preserving the original error for
// errors.Is/errors.As.
type IOError struct{ Err error }

func (e *IOError) Error() string { return "io error: " + e.Err.Error() }
func (e *IOError) Unwrap() error { return e.Err }

func protocolError(message string) error { return &ProtocolError{Message: message} }

func requestTimeoutError(method string, diagnostics string) error {
	message := fmt.Sprintf("%s timed out waiting for DeepSeek Harness runtime", method)
	if diagnostics != "" {
		message += "\n" + diagnostics
	}
	return &RequestTimeoutError{Message: message}
}

func transportClosedError(reason string, exitCode *int, stderrTail []string) error {
	parts := []string{reason}
	if exitCode != nil {
		parts = append(parts, fmt.Sprintf("exit code: %d", *exitCode))
	}
	if len(stderrTail) != 0 {
		parts = append(parts, "stderr tail:\n"+strings.Join(stderrTail, "\n"))
	}
	return &TransportClosedError{
		Message:    strings.Join(parts, "\n"),
		ExitCode:   cloneIntPointer(exitCode),
		StderrTail: append([]string(nil), stderrTail...),
	}
}

func runtimeResolveError(format string, args ...any) error {
	return &RuntimeResolveError{Message: fmt.Sprintf(format, args...)}
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

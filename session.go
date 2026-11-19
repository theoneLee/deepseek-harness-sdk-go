package deepseekharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ResumeSession restores persisted history and optionally overrides the saved
// permission preset. Restoration errors are returned by the runtime.
func (c *HarnessClient) ResumeSession(sessionID string, options ...ResumeSessionOptions) error {
	if len(options) > 1 {
		return fmt.Errorf("ResumeSession accepts at most one ResumeSessionOptions value")
	}
	params := JSONObject{"sessionId": sessionID}
	if len(options) == 1 && options[0].PermissionMode != nil {
		params["permissionMode"] = *options[0].PermissionMode
	}
	result, err := c.Request("session/resume", params)
	if err != nil {
		return err
	}
	if _, ok := normalizeJSONObject(result); !ok {
		return protocolError("session/resume response must be a JSON object")
	}
	return nil
}

// GetWorkingDirectory returns the effective working directory of a session.
func (c *HarnessClient) GetWorkingDirectory(sessionID string) (string, error) {
	return c.workingDirectory("session/working-directory/get", JSONObject{"sessionId": sessionID})
}

// SetWorkingDirectory changes the effective directory and its write boundary.
func (c *HarnessClient) SetWorkingDirectory(sessionID, path string) (string, error) {
	return c.workingDirectory("session/working-directory/set", JSONObject{"sessionId": sessionID, "path": path})
}

func (c *HarnessClient) workingDirectory(method string, params JSONObject) (string, error) {
	result, err := c.Request(method, params)
	if err != nil {
		return "", err
	}
	object, _ := normalizeJSONObject(result)
	cwd, ok := object["cwd"].(string)
	if !ok {
		return "", protocolError(method + " response requires a string cwd")
	}
	return cwd, nil
}

// GetWorkingDirectory returns this session's effective working directory.
func (s *Session) GetWorkingDirectory() (string, error) {
	if err := s.Harness.Start(); err != nil {
		return "", err
	}
	return s.Harness.Client().GetWorkingDirectory(s.ID)
}

// SetWorkingDirectory changes this session's effective directory.
func (s *Session) SetWorkingDirectory(path string) (string, error) {
	if err := s.Harness.Start(); err != nil {
		return "", err
	}
	return s.Harness.Client().SetWorkingDirectory(s.ID, path)
}

func (c *HarnessClient) query(sessionID string, blocks []JSONObject) (messageID, commandID string, err error) {
	result, err := c.Request("session/query", JSONObject{"sessionId": sessionID, "contentBlocks": blocks})
	if err != nil {
		var rpcErr *JsonRpcError
		if errors.As(err, &rpcErr) && rpcErr.Code != nil && *rpcErr.Code == -32601 {
			messageID, err = c.SessionPrompt(sessionID, blocks)
		}
		return messageID, "", err
	}
	object, _ := normalizeJSONObject(result)
	switch object["kind"] {
	case "prompt":
		if id, ok := object["messageId"].(string); ok && id != "" {
			return id, "", nil
		}
	case "command":
		execution, _ := normalizeJSONObject(object["execution"])
		id, _ := execution["commandId"].(string)
		outcome, _ := normalizeJSONObject(execution["result"])
		if id != "" && validCommandResult(outcome) {
			return "", id, nil
		}
	}
	return "", "", protocolError("session/query returned an invalid prompt or command result")
}

func validCommandResult(outcome JSONObject) bool {
	text, hasText := outcome["text"]
	textValue, textOK := text.(string)
	switch outcome["kind"] {
	case "success":
		if hasText && !textOK {
			return false
		}
		if seq, present := outcome["sourceEventSeq"]; present {
			number, ok := seq.(json.Number)
			if !ok {
				return false
			}
			value, err := number.Int64()
			return err == nil && value >= 0 && value <= 1<<53-1
		}
		return true
	case "error":
		return textOK && strings.TrimSpace(textValue) != ""
	default:
		return false
	}
}

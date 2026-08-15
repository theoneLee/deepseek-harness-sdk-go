package deepseekharness

import "fmt"

func isInboxReceipt(notification Notification, sessionID, messageID string) bool {
	if notification.Method != "session.event" || notification.Payload["sessionId"] != sessionID {
		return false
	}
	event, ok := normalizeJSONObject(notification.Payload["event"])
	if !ok || event["type"] != "agent/inbox/spliced" {
		return false
	}
	data, _ := normalizeJSONObject(event["data"])
	inserted, _ := data["inserted"].([]any)
	for _, item := range inserted {
		message, ok := normalizeJSONObject(item)
		if ok && message["id"] == messageID {
			return true
		}
	}
	return false
}

// IsInboxReceipt exposes the durable prompt-acceptance predicate for callers
// implementing a custom activity loop.
func IsInboxReceipt(notification Notification, sessionID, messageID string) bool {
	return isInboxReceipt(notification, sessionID, messageID)
}

// FinalResponse returns the last committed assistant text in an event list.
func FinalResponse(events []JSONObject) string {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event["type"] != "assistant/message" {
			continue
		}
		data, _ := normalizeJSONObject(event["data"])
		contentOwner := data
		if message, ok := normalizeJSONObject(data["message"]); ok {
			contentOwner = message
		}
		content, _ := contentOwner["content"].([]any)
		result := ""
		for _, item := range content {
			block, ok := normalizeJSONObject(item)
			if !ok || block["type"] != "text" {
				continue
			}
			if text, ok := block["text"].(string); ok {
				result += text
			}
		}
		return result
	}
	return ""
}

// FinishReason returns the kind of the last turn/end event. A turn/end event
// without a string data.reason.kind is a protocol violation.
func FinishReason(events []JSONObject) (*string, error) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event["type"] != "turn/end" {
			continue
		}
		data, _ := normalizeJSONObject(event["data"])
		reason, _ := normalizeJSONObject(data["reason"])
		kind, ok := reason["kind"].(string)
		if !ok {
			return nil, protocolError("turn/end event requires a string data.reason.kind")
		}
		return &kind, nil
	}
	return nil, nil
}

func contentBlocks(input any) ([]JSONObject, error) {
	switch value := input.(type) {
	case Input:
		if value.Text != nil {
			return []JSONObject{{"type": "text", "text": *value.Text}}, nil
		}
		return cloneBlocks(value.Blocks), nil
	case *Input:
		if value == nil {
			return nil, fmt.Errorf("input must be a string, Input, or content block list")
		}
		return contentBlocks(*value)
	case string:
		return []JSONObject{{"type": "text", "text": value}}, nil
	case []JSONObject:
		return cloneBlocks(value), nil
	case []map[string]any:
		blocks := make([]JSONObject, len(value))
		for index, block := range value {
			blocks[index] = JSONObject(block)
		}
		return blocks, nil
	case []any:
		blocks := make([]JSONObject, len(value))
		for index, item := range value {
			block, ok := normalizeJSONObject(item)
			if !ok {
				return nil, fmt.Errorf("content block %d must be a JSON object", index)
			}
			blocks[index] = block
		}
		return blocks, nil
	default:
		return nil, fmt.Errorf("input must be a string, Input, or content block list")
	}
}

func cloneBlocks(blocks []JSONObject) []JSONObject {
	if blocks == nil {
		return nil
	}
	copy := make([]JSONObject, len(blocks))
	for index, block := range blocks {
		copy[index] = block
	}
	return copy
}

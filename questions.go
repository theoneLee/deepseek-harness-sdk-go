package deepseekharness

import "context"

func (c *HarnessClient) handleUserQuestion(state *clientState, incoming IncomingRequest) bool {
	state.mu.Lock()
	handler := state.questionHandler
	state.mu.Unlock()
	if handler == nil {
		return false
	}

	request, valid := parseUserQuestion(incoming.Payload)
	response := JSONObject{"jsonrpc": "2.0", "id": incoming.ID}
	if !valid {
		response["error"] = JSONObject{"code": -32602, "message": "invalid user-question/ask parameters"}
		_ = c.writeMessage(state, response)
		return true
	}

	state.mu.Lock()
	if state.closed || state.closing {
		state.mu.Unlock()
		return true
	}
	if state.questions[request.RequestID] != nil {
		state.mu.Unlock()
		response["error"] = JSONObject{"code": -32602, "message": "duplicate user-question requestId"}
		_ = c.writeMessage(state, response)
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	if state.questions == nil {
		state.questions = make(map[string]context.CancelFunc)
	}
	state.questions[request.RequestID] = cancel
	state.mu.Unlock()

	go func() {
		defer func() {
			state.mu.Lock()
			delete(state.questions, request.RequestID)
			state.mu.Unlock()
			cancel()
		}()
		if ctx.Err() != nil {
			return
		}
		answer, err := handler(ctx, request)
		if err != nil {
			response["error"] = JSONObject{"code": -32603, "message": err.Error()}
		} else {
			response["result"] = answer
		}
		if ctx.Err() == nil {
			_ = c.writeMessage(state, response)
		}
	}()
	return true
}

func parseUserQuestion(payload JSONObject) (UserQuestionRequest, bool) {
	requestID, requestOK := payload["requestId"].(string)
	sessionID, sessionOK := payload["sessionId"].(string)
	items, itemsOK := payload["questions"].([]any)
	if !requestOK || requestID == "" || !sessionOK || !itemsOK || len(items) == 0 {
		return UserQuestionRequest{}, false
	}
	questions := make([]JSONObject, 0, len(items))
	for _, item := range items {
		question, _ := normalizeJSONObject(item)
		_, idOK := question["id"].(string)
		_, textOK := question["question"].(string)
		if !idOK || !textOK {
			return UserQuestionRequest{}, false
		}
		questions = append(questions, question)
	}
	return UserQuestionRequest{RequestID: requestID, SessionID: sessionID, Questions: questions}, true
}

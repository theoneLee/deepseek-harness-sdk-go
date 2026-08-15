package deepseekharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestFakeRuntimeProcess is launched as a child process by the mechanism
// tests below. Keeping it in the test binary exercises the same pipe behavior
// as a separately installed runtime without requiring model credentials.
func TestFakeRuntimeProcess(t *testing.T) {
	if os.Getenv("DSH_TEST_RUNTIME") != "1" {
		return
	}
	scenario := os.Getenv("DSH_TEST_SCENARIO")
	decoder := json.NewDecoder(os.Stdin)
	decoder.UseNumber()
	for {
		var message map[string]any
		if err := decoder.Decode(&message); err != nil {
			return
		}
		method, _ := message["method"].(string)
		switch scenario {
		case "crash":
			if method == "initialize" {
				writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": map[string]any{}})
				fmt.Fprintln(os.Stderr, "fake runtime stderr line one")
				os.Exit(7)
			}
		case "request-then-exit":
			if method == "initialize" {
				writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": map[string]any{}})
				writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "id": "req-1", "method": "approval/ask"})
				os.Exit(2)
			}
		default:
			handleFakeRequest(t, scenario, message)
		}
	}
}

func handleFakeRequest(t *testing.T, scenario string, message map[string]any) {
	method, _ := message["method"].(string)
	switch method {
	case "initialize":
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      message["id"],
			"result": map[string]any{
				"serverInfo": map[string]any{
					"name": "deepseek-harness-sdk-runtime", "version": "0.0.1",
				},
			},
		})
		if scenario == "request-to-client" {
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "id": "req-1", "method": "approval/ask",
			})
		}
		if scenario == "invalid-lines" {
			fmt.Fprintln(os.Stdout, "this is not json")
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "method": "session.status",
				"params": map[string]any{"sessionId": "s1", "status": "ready"},
			})
		}
	case "anything":
		writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": message["params"]})
	case "session/prompt":
		if scenario == "slow" {
			time.Sleep(5 * time.Second)
			return
		}
		if scenario == "error" {
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "id": message["id"],
				"error": map[string]any{
					"code": -32603, "message": "fake runtime refused",
					"data": map[string]any{"detail": "fake runtime refused"},
				},
			})
			return
		}
		params, _ := message["params"].(map[string]any)
		sessionID, _ := params["sessionId"].(string)
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0", "id": message["id"],
			"result": map[string]any{"messageId": "message-1"},
		})
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{
				"sessionId": sessionID,
				"event": map[string]any{
					"type": "agent/inbox/spliced",
					"data": map[string]any{"inserted": []any{map[string]any{"id": "message-1"}}},
				},
			},
		})
		if scenario == "subagent" {
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "method": "subagent.started",
				"params": map[string]any{
					"parentSessionId": sessionID, "childSessionId": "child-1",
				},
			})
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "method": "session.event",
				"params": map[string]any{
					"sessionId": "child-1", "event": map[string]any{"type": "assistant/message"},
				},
			})
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "method": "subagent.finished",
				"params": map[string]any{
					"parentSessionId": sessionID, "childSessionId": "child-1",
				},
			})
		}
		if scenario != "no-assistant" {
			writeFakeFrame(t, map[string]any{
				"jsonrpc": "2.0", "method": "session.event",
				"params": map[string]any{
					"sessionId": sessionID,
					"event": map[string]any{
						"type": "assistant/message",
						"data": map[string]any{"message": map[string]any{
							"content": []any{map[string]any{"type": "text", "text": "hello from fake"}},
						}},
					},
				},
			})
		}
		reason := "max-tokens"
		if scenario == "no-assistant" {
			reason = "ok"
		}
		turnData := map[string]any{"reason": map[string]any{"kind": reason}}
		if scenario == "bad-turn-end" {
			turnData = map[string]any{}
		}
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{"sessionId": sessionID, "event": map[string]any{"type": "turn/end", "data": turnData}},
		})
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0", "method": "session.status",
			"params": map[string]any{"sessionId": sessionID, "status": "idle"},
		})
	case "shutdown":
		if scenario == "no-shutdown" {
			select {}
		}
		writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": map[string]any{}})
		os.Exit(0)
	case "ping":
		// Notifications sent by the client do not have an id and need no reply.
	}
}

func writeFakeFrame(t *testing.T, frame map[string]any) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

func fakeOptions(scenario string) HarnessClientOptions {
	return HarnessClientOptions{
		LaunchArgsOverride: []string{os.Args[0], "-test.run=TestFakeRuntimeProcess", "--"},
		Env: map[string]string{
			"DSH_TEST_RUNTIME":  "1",
			"DSH_TEST_SCENARIO": scenario,
		},
		ShutdownTimeoutSeconds: 0.2,
	}
}

func TestClientHandshakeAndPrompt(t *testing.T) {
	client := NewHarnessClient(fakeOptions("basic"))
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Initialize(InitializeParams{Provider: "provider", Model: "model"})
	if err != nil || result.ServerInfo == nil || result.ServerInfo.Name != "deepseek-harness-sdk-runtime" {
		t.Fatalf("Initialize() = %#v, %v", result, err)
	}
	subscription, err := client.SubscribeSessionTree("s1")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	messageID, err := client.SessionPrompt("s1", []JSONObject{{"type": "text", "text": "hi"}})
	if err != nil || messageID != "message-1" {
		t.Fatalf("SessionPrompt() = %q, %v", messageID, err)
	}
	var methods []string
	for {
		notification, err := subscription.Next()
		if err != nil {
			t.Fatal(err)
		}
		methods = append(methods, notification.Method)
		if notification.Method == "session.status" && notification.Payload["status"] == "idle" {
			break
		}
	}
	if len(methods) < 3 {
		t.Fatalf("unexpected methods: %#v", methods)
	}
}

func TestHighLevelRunAndSessionTree(t *testing.T) {
	for _, scenario := range []string{"basic", "subagent"} {
		harness := NewDeepSeekHarness(DeepSeekHarnessConfig{
			LaunchArgsOverride:     fakeOptions(scenario).LaunchArgsOverride,
			Env:                    fakeOptions(scenario).Env,
			ShutdownTimeoutSeconds: 0.2,
		})
		result, err := harness.Run("hi")
		if err != nil {
			t.Fatalf("scenario %s: %v", scenario, err)
		}
		if result.FinalResponse != "hello from fake" || result.FinishReason == nil || *result.FinishReason != "max-tokens" {
			t.Fatalf("scenario %s result: %#v", scenario, result)
		}
		if scenario == "subagent" && !hasMethod(result.Notifications, "subagent.started") {
			t.Fatalf("subagent notification missing: %#v", result.Notifications)
		}
		if err := harness.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClientErrorTimeoutAndServerRequest(t *testing.T) {
	client := NewHarnessClient(fakeOptions("error"))
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	_, err := client.Initialize(InitializeParams{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SessionPrompt("s1", nil)
	var jsonError *JsonRpcError
	if !asError(err, &jsonError) || jsonError.Code == nil || *jsonError.Code != -32603 {
		t.Fatalf("expected JsonRpcError, got %T: %v", err, err)
	}
	client.Close()

	timeoutOptions := fakeOptions("slow")
	timeoutOptions.RequestTimeoutSeconds = 0.05
	slow := NewHarnessClient(timeoutOptions)
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = slow.Initialize(InitializeParams{})
	_, err = slow.SessionPrompt("s1", nil)
	var timeoutError *RequestTimeoutError
	if !asError(err, &timeoutError) {
		t.Fatalf("expected RequestTimeoutError, got %T: %v", err, err)
	}
	slow.Close()

	requestClient := NewHarnessClient(fakeOptions("request-to-client"))
	if err := requestClient.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := requestClient.Initialize(InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	request, err := requestClient.NextRequest()
	if err != nil || request.Method != "approval/ask" || request.ID != "req-1" {
		t.Fatalf("NextRequest() = %#v, %v", request, err)
	}
	if err := requestClient.Respond(request.ID, JSONObject{"ok": true}); err != nil {
		t.Fatal(err)
	}
	requestClient.Close()
}

func TestTransportDiagnosticsAndCloseLadder(t *testing.T) {
	crashed := NewHarnessClient(fakeOptions("crash"))
	if err := crashed.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := crashed.Initialize(InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	_, err := crashed.Request("anything")
	var closedError *TransportClosedError
	if !asError(err, &closedError) || closedError.ExitCode == nil || *closedError.ExitCode != 7 {
		t.Fatalf("expected diagnostic TransportClosedError, got %T: %v", err, err)
	}
	foundStderr := false
	for _, line := range closedError.StderrTail {
		if line == "fake runtime stderr line one" {
			foundStderr = true
		}
	}
	if !foundStderr {
		t.Fatalf("stderr tail missing: %#v", closedError.StderrTail)
	}
	crashed.Close()

	uncooperative := NewHarnessClient(fakeOptions("no-shutdown"))
	if err := uncooperative.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := uncooperative.Initialize(InitializeParams{}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	uncooperative.Close()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close took too long: %s", elapsed)
	}
}

func hasMethod(notifications []Notification, method string) bool {
	for _, notification := range notifications {
		if notification.Method == method {
			return true
		}
	}
	return false
}

func asError(err error, target any) bool {
	if err == nil {
		return false
	}
	return errors.As(err, target)
}

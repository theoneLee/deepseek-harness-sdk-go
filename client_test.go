package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		if method == "initialize" {
			if path := os.Getenv("DSH_TEST_INIT_FILE"); path != "" {
				encoded, err := json.Marshal(message["params"])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, encoded, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		switch scenario {
		case "crash":
			if method == "anything" {
				fmt.Fprintln(os.Stderr, "fake runtime stderr line one")
				os.Exit(7)
			}
			handleFakeRequest(t, scenario, message)
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
		if scenario == "native-command" || scenario == "native-failure" {
			params := message["params"].(map[string]any)
			capabilities, ok := params["capabilities"].(map[string]any)
			if params["approvalRequests"] != true || !ok || capabilities["userQuestions"] != true {
				t.Fatalf("missing initialize capabilities: %#v", params)
			}
		}
		if scenario == "initialize-slow" {
			time.Sleep(5 * time.Second)
			return
		}
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
	case "session/resume", "session/working-directory/get", "session/working-directory/set":
		params := message["params"].(map[string]any)
		result := map[string]any{}
		switch method {
		case "session/resume":
			if params["sessionId"] != "resumed" || params["permissionMode"] != "test-mode" {
				t.Fatalf("unexpected resume params: %#v", params)
			}
		case "session/working-directory/get":
			result["cwd"] = "/workspace/start"
		case "session/working-directory/set":
			result["cwd"] = params["path"]
		}
		writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "id": message["id"], "result": result})
	case "session/query":
		if scenario == "native-command" || scenario == "native-failure" {
			handleFakeCommand(t, scenario, message)
			return
		}
		if scenario == "native-prompt" {
			handleFakeRequest(t, scenario, map[string]any{
				"id": message["id"], "params": message["params"], "method": "session/prompt",
			})
			return
		}
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0", "id": message["id"],
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
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
		receipt := map[string]any{"messageId": "message-1"}
		if scenario == "native-prompt" {
			receipt["kind"] = "prompt"
		}
		writeFakeFrame(t, map[string]any{
			"jsonrpc": "2.0", "id": message["id"],
			"result": receipt,
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
	options := fakeOptions("basic")
	initFile := filepath.Join(t.TempDir(), "initialize.json")
	options.Env["DSH_TEST_INIT_FILE"] = initFile
	client := NewHarnessClient(options)
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Initialize(InitializeParams{
		Provider:        "provider",
		Model:           "model",
		ReasoningEffort: "max",
		MaxTokens:       123,
	})
	if err != nil || result.ServerInfo == nil || result.ServerInfo.Name != "deepseek-harness-sdk-runtime" {
		t.Fatalf("Initialize() = %#v, %v", result, err)
	}
	initPayload, err := os.ReadFile(initFile)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(initPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reasoningEffort"] != "max" || payload["maxTokens"] != float64(123) {
		t.Fatalf("initialize payload = %#v", payload)
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
	for _, scenario := range []string{"basic", "subagent", "native-prompt"} {
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

	initializeTimeoutOptions := fakeOptions("initialize-slow")
	initializeTimeoutOptions.Profile = "custom"
	initializeTimeoutOptions.InitializeTimeoutSeconds = 0.05
	initializeSlow := NewHarnessClient(initializeTimeoutOptions)
	if err := initializeSlow.Start(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = initializeSlow.Initialize(InitializeParams{})
	if time.Since(started) > 2*time.Second {
		t.Fatalf("initialize timeout took too long: %s", time.Since(started))
	}
	var initializeTimeoutError *RequestTimeoutError
	if !asError(err, &initializeTimeoutError) || !strings.Contains(initializeTimeoutError.Error(), `selected dsh profile "custom"`) {
		t.Fatalf("expected profile diagnostic, got %T: %v", err, err)
	}
	initializeSlow.Close()

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

func TestDshLaunchConfigurationRequiresHomeAndAddsProfilePatches(t *testing.T) {
	patch := filepath.Join(t.TempDir(), "example.patch.yml")
	if err := os.WriteFile(patch, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	dshBin := filepath.Join(t.TempDir(), "dsh")
	home := filepath.Join(t.TempDir(), "home")
	client := NewHarnessClient(HarnessClientOptions{
		DSHBin:  dshBin,
		Profile: "custom",
		Patches: []string{patch},
		DSHHome: home,
		Env:     map[string]string{"DSH_HOME": "overridden"},
	})
	launch, err := client.resolveLaunch()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"--profile", "custom", "--patch", patch}
	if launch.command != dshBin || !equalStrings(launch.args, wantArgs) || !launch.requiresDSHHome {
		t.Fatalf("resolved launch = %#v, want command=%q args=%#v", launch, dshBin, wantArgs)
	}
	env, err := client.buildEnvironment(launch)
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := lookupEnv(env, "DSH_HOME"); !ok || value != home {
		t.Fatalf("DSH_HOME = %q, present=%v; want %q", value, ok, home)
	}

	missingHome := NewHarnessClient(HarnessClientOptions{
		DSHBin: dshBin,
		Env:    map[string]string{"DSH_HOME": ""},
	})
	missingLaunch, err := missingHome.resolveLaunch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missingHome.buildEnvironment(missingLaunch); err == nil || !strings.Contains(err.Error(), "explicit DSHHome") {
		t.Fatalf("missing DSH_HOME error = %v", err)
	}
}

func TestHighLevelConfigCarriesNewPythonSDKOptions(t *testing.T) {
	patches := []string{"first.patch.yml", "second.patch.yml"}
	harness := NewDeepSeekHarness(DeepSeekHarnessConfig{
		ReasoningEffort:          "high",
		DSHBin:                   "/path/to/dsh",
		Profile:                  "sdk-minimal",
		Patches:                  patches,
		DSHHome:                  "/tmp/dsh-home",
		InitializeTimeoutSeconds: 12,
	})
	config := harness.Config()
	if config.ReasoningEffort != "high" || config.DSHBin != "/path/to/dsh" ||
		config.Profile != "sdk-minimal" || config.DSHHome != "/tmp/dsh-home" ||
		config.InitializeTimeoutSeconds != 12 || !equalStrings(config.Patches, patches) {
		t.Fatalf("high-level config = %#v", config)
	}
	if harness.Client().Options.Profile != "sdk-minimal" ||
		harness.Client().Options.InitializeTimeoutSeconds != 12 ||
		!equalStrings(harness.Client().Options.Patches, patches) {
		t.Fatalf("client options = %#v", harness.Client().Options)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func lookupEnv(values []string, key string) (string, bool) {
	prefix := key + "="
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix), true
		}
	}
	return "", false
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

func handleFakeCommand(t *testing.T, scenario string, message map[string]any) {
	params := message["params"].(map[string]any)
	sessionID := params["sessionId"]
	notify := func(method string, params map[string]any) {
		params["sessionId"] = sessionID
		writeFakeFrame(t, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	}
	event := func(kind string) {
		notify("session.event", map[string]any{"event": map[string]any{
			"type": kind, "data": map[string]any{"commandId": "command-1"},
		}})
	}
	complete := func(id, text, reason string) {
		notify("session.query.completed", map[string]any{
			"commandId": id, "finalResponse": text, "finishReason": reason,
		})
	}
	notify("session.status", map[string]any{"status": "idle"})
	event("command/run")
	notify("session.status", map[string]any{"status": "idle"})
	complete("unrelated", "ignore me", "ok")
	kind, reason := "success", "ok"
	if scenario == "native-failure" {
		kind, reason = "error", "error"
		complete("command-1", "native outcome", reason)
	}
	event("command/done")
	writeFakeFrame(t, map[string]any{
		"jsonrpc": "2.0", "id": message["id"],
		"result": map[string]any{"kind": "command", "execution": map[string]any{
			"commandId": "command-1", "result": map[string]any{"kind": kind, "text": "handler settlement"},
		}},
	})
	if scenario != "native-failure" {
		complete("command-1", "native outcome", reason)
	}
}

func TestNativeCommandActivityAndSessionControls(t *testing.T) {
	for _, scenario := range []string{"native-command", "native-failure"} {
		t.Run(scenario, func(t *testing.T) {
			opts := fakeOptions(scenario)
			h := NewDeepSeekHarness(DeepSeekHarnessConfig{
				LaunchArgsOverride: opts.LaunchArgsOverride, Env: opts.Env,
				RequestTimeoutSeconds: 2, ShutdownTimeoutSeconds: opts.ShutdownTimeoutSeconds,
				ApprovalRequests: true,
				OnUserQuestion: func(context.Context, UserQuestionRequest) (UserQuestionAnswer, error) {
					return UserQuestionAnswer{}, errors.New("unexpected question")
				},
			})
			t.Cleanup(func() { _ = h.Close() })
			preset := "test-mode"
			s, err := h.ResumeSession("resumed", ResumeSessionOptions{PermissionMode: &preset})
			if err != nil {
				t.Fatal(err)
			}
			if cwd, err := s.GetWorkingDirectory(); err != nil || cwd != "/workspace/start" {
				t.Fatalf("get directory = %q, %v", cwd, err)
			}
			if cwd, err := s.SetWorkingDirectory("/workspace/next"); err != nil || cwd != "/workspace/next" {
				t.Fatalf("set directory = %q, %v", cwd, err)
			}
			var observed int
			result, err := s.Run("/compact", func(Notification) { observed++ })
			if err != nil {
				t.Fatal(err)
			}
			reason := "ok"
			if scenario == "native-failure" {
				reason = "error"
			}
			if result.SessionID != s.ID || result.FinalResponse != "native outcome" ||
				result.FinishReason == nil || *result.FinishReason != reason ||
				len(result.Events) != 2 || len(result.Notifications) != 3 || observed != 3 {
				t.Fatalf("unexpected command result: %#v, observed %d", result, observed)
			}
		})
	}
}

func TestUserQuestionCancellation(t *testing.T) {
	state := newClientState(nil, nil)
	client := NewHarnessClient()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	state.questionHandler = func(ctx context.Context, question UserQuestionRequest) (UserQuestionAnswer, error) {
		if question.RequestID != "question-1" || question.SessionID != "s1" || len(question.Questions) != 1 {
			t.Errorf("unexpected question: %#v", question)
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		return UserQuestionAnswer{}, ctx.Err()
	}
	client.handleFrame(state, []byte(`{"id":1,"method":"user-question/ask","params":{"requestId":"question-1","sessionId":"s1","questions":[{"id":"q","question":"Which?"}]}}`))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("question handler did not start")
	}
	client.handleFrame(state, []byte(`{"method":"user-question.cancelled","params":{"requestId":"question-1"}}`))
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("question handler did not receive cancellation")
	}
}

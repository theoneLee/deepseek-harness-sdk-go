package deepseekharness

import "testing"

func TestEventHelpers(t *testing.T) {
	events := []JSONObject{
		{
			"type": "assistant/message",
			"data": JSONObject{
				"message": JSONObject{
					"content": []any{
						JSONObject{"type": "text", "text": "hello "},
						JSONObject{"type": "text", "text": "world"},
					},
				},
			},
		},
		{"type": "turn/end", "data": JSONObject{"reason": JSONObject{"kind": "completed"}}},
	}
	if got := FinalResponse(events); got != "hello world" {
		t.Fatalf("FinalResponse() = %q", got)
	}
	reason, err := FinishReason(events)
	if err != nil || reason == nil || *reason != "completed" {
		t.Fatalf("FinishReason() = %v, %v", reason, err)
	}

	_, err = FinishReason([]JSONObject{{"type": "turn/end", "data": JSONObject{}}})
	if _, ok := err.(*ProtocolError); !ok {
		t.Fatalf("expected ProtocolError, got %T", err)
	}
}

func TestToPyPIVersion(t *testing.T) {
	cases := map[string]string{
		"0.1.0-rc.6":    "0.1.0rc6",
		"0.2.0-beta.1":  "0.2.0b1",
		"0.3.0-alpha.3": "0.3.0a3",
		"0.4.0":         "0.4.0",
	}
	for input, expected := range cases {
		if got := ToPyPIVersion(input); got != expected {
			t.Errorf("ToPyPIVersion(%q) = %q, want %q", input, got, expected)
		}
	}
}

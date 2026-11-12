package tello

import "testing"

func TestParseUserTurn(t *testing.T) {
	event := ParseEvent(map[string]any{
		"type":      "user.turn",
		"version":   "1.0",
		"callId":    "c1",
		"turnIndex": float64(2),
		"text":      "hey",
		"timestamp": "t",
	})

	if event.Type != EventTypeUserTurn || event.TurnIndex != 2 || event.Text != "hey" || event.CallID != "c1" {
		t.Fatalf("unexpected event: %+v", event)
	}
}

func TestParseErrorFrame(t *testing.T) {
	event := ParseEvent(map[string]any{
		"type":      "error",
		"version":   "1.0",
		"code":      "callRejected",
		"message":   "Call rejected",
		"requestId": "r1",
		"question":  "why?",
	})

	if event.Code != "callRejected" || event.RequestID != "r1" || event.Question != "why?" {
		t.Fatalf("unexpected error event: %+v", event)
	}
}

func TestParseErrorFrameExtraFields(t *testing.T) {
	scope := ParseEvent(map[string]any{
		"type": "error", "version": "1.0", "code": "insufficientScope",
		"message": "m", "requestId": "r1", "requiredScope": "calls:write",
	})
	if scope.RequiredScope != "calls:write" {
		t.Fatalf("RequiredScope = %q", scope.RequiredScope)
	}

	ip := ParseEvent(map[string]any{"type": "error", "version": "1.0", "code": "ipNotAllowed", "message": "m", "ip": "203.0.113.7"})
	if ip.IP != "203.0.113.7" {
		t.Fatalf("IP = %q", ip.IP)
	}
	unproven := ParseEvent(map[string]any{"type": "error", "version": "1.0", "code": "ipNotAllowed", "message": "m", "ip": nil})
	if unproven.IP != "" {
		t.Fatalf("IP = %q, want empty for null", unproven.IP)
	}
}

func TestParseCallSummary(t *testing.T) {
	summary := ParseEvent(map[string]any{
		"type":            "call.summary",
		"version":         "1.0",
		"requestId":       "summary-1",
		"callId":          "call-1",
		"status":          "completed",
		"durationSeconds": float64(42),
		"transcript":      "고객: 예약 확인",
		"summary":         "예약 확인 완료",
		"creditCharged":   float64(15),
	})
	if summary.Type != EventTypeCallSummary || summary.RequestID != "summary-1" || summary.CallID != "call-1" || summary.DurationSeconds != 42 || summary.CreditCharged != 15 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestParseAcceptedAndCreatedEvents(t *testing.T) {
	created := ParseEvent(map[string]any{
		"type":      "call.created",
		"version":   "1.0",
		"sessionId": "s1",
		"callId":    "c1",
		"timestamp": "t",
	})
	if created.Type != EventTypeCallCreated || created.SessionID != "s1" || created.CallID != "c1" {
		t.Fatalf("unexpected call.created: %+v", created)
	}

	answer := ParseEvent(map[string]any{
		"type":      "answer.accepted",
		"version":   "1.0",
		"requestId": "r1",
		"sessionId": "s1",
		"callId":    "c1",
		"messageId": "m1",
		"timestamp": "t",
	})
	if answer.Type != EventTypeAnswerAccepted || answer.MessageID != "m1" || answer.RequestID != "r1" {
		t.Fatalf("unexpected answer.accepted: %+v", answer)
	}

	dtmf := ParseEvent(map[string]any{
		"type":      "dtmf.accepted",
		"version":   "1.0",
		"sessionId": "s1",
		"callId":    "c1",
		"messageId": "m2",
		"digits":    "12#",
		"timestamp": "t",
	})
	if dtmf.Type != EventTypeDtmfAccepted || dtmf.MessageID != "m2" || dtmf.Digits != "12#" {
		t.Fatalf("unexpected dtmf.accepted: %+v", dtmf)
	}
}

func TestTerminalDetection(t *testing.T) {
	if !IsTerminal(ParseEvent(map[string]any{
		"type":      "call.completed",
		"version":   "1.0",
		"callId":    "c1",
		"status":    "completed",
		"timestamp": "t",
	})) {
		t.Fatal("completed should be terminal")
	}
	if !IsTerminal(ParseEvent(map[string]any{
		"type":           "call.statusChanged",
		"version":        "1.0",
		"callId":         "c1",
		"status":         "cancelled",
		"previousStatus": "inProgress",
		"timestamp":      "t",
	})) {
		t.Fatal("cancelled should be terminal")
	}
}

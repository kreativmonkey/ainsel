package ainselapishared

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	evt := Event{
		ID:        "evt_test123",
		Version:   "1",
		Connector: "my-forgejo",
		Timestamp: now,
		Headers: map[string]string{
			"X-GitHub-Event": "issues",
		},
		Data: json.RawMessage(`{"comment":{"id":1,"body":"hello"}}`),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ID != evt.ID {
		t.Errorf("ID: got %q, want %q", got.ID, evt.ID)
	}
	if got.Connector != evt.Connector {
		t.Errorf("Connector: got %q, want %q", got.Connector, evt.Connector)
	}
	if got.Headers["X-GitHub-Event"] != "issues" {
		t.Errorf("Headers[X-GitHub-Event]: got %q, want %q", got.Headers["X-GitHub-Event"], "issues")
	}
	if !got.Timestamp.Equal(now) {
		t.Errorf("Timestamp: got %v, want %v", got.Timestamp, now)
	}
}

func TestEventWithNilData(t *testing.T) {
	evt := Event{
		ID:      "evt_nil",
		Version: "1",
	}
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal with nil data: %v", err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal with nil data: %v", err)
	}
	if got.Data != nil {
		t.Errorf("expected nil Data, got %s", string(got.Data))
	}
}

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentSubscriptionRuntimeRoundTrip(t *testing.T) {
	s := testServer(t, testAgentImage("img-1"))
	s.mux.HandleFunc("/api/v1/agents", s.handleAgents)
	s.mux.HandleFunc("/api/v1/agents/", s.handleAgent)
	call := func(method, path, body string, status int) SimpleAgentResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		var out SimpleAgentResponse
		if status < 400 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	created := call(http.MethodPost, "/api/v1/agents", `{"name":"subscription","imageRef":{"name":"img-1"},"runtime":{"engine":"codex","subscription":{"claimName":"private-home","login":true}}}`, 201)
	if created.Runtime.Engine != "codex" || !created.Runtime.Subscription.Login {
		t.Fatalf("unexpected runtime: %+v", created.Runtime)
	}
	path := "/api/v1/agents/" + created.ID
	// Unrelated edits preserve the selection; explicit runtime updates replace it.
	edited := call(http.MethodPut, path, `{"description":"preserve runtime"}`, 200)
	if edited.Runtime.Subscription.ClaimName != "private-home" {
		t.Fatal("lost credential claim")
	}
	edited = call(http.MethodPut, path, `{"runtime":{"engine":"claude","subscription":{"claimName":"private-home","login":false}}}`, 200)
	if edited.Runtime.Engine != "claude" || edited.Runtime.Subscription.Login {
		t.Fatal("failed to disable login")
	}
	edited = call(http.MethodPut, path, `{"runtime":{"engine":"pi"}}`, 200)
	if edited.Runtime.Engine != "pi" || edited.Runtime.Subscription != nil {
		t.Fatal("failed to clear subscription")
	}
	for _, body := range []string{
		`{"runtime":{"engine":"unknown"}}`,
		`{"runtime":{"engine":"codex"}}`,
		`{"runtime":{"engine":"codex","subscription":{"claimName":"../secret"}}}`,
		`{"runtime":{"engine":"pi","subscription":{"claimName":"private-home"}}}`,
	} {
		call(http.MethodPut, path, body, 400)
	}
}

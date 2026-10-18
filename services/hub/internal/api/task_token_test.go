package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ainsel-project/ainsel/shared/api/tasktoken"
)

func TestScopedTaskRoutes(t *testing.T) {
	s := &Server{internalValidateSecret: "secret"}
	token := tasktoken.Sign("secret", "reviewer", 42, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/internal/agents/reviewer/tasks/42", true},
		{"POST", "/api/internal/agents/reviewer/tasks/42/ack", true},
		{"POST", "/api/internal/agents/reviewer/tasks/42/nack", true},
		{"GET", "/api/internal/agents/other/tasks/42", false},
		{"GET", "/api/internal/agents/reviewer/tasks/43", false},
		{"GET", "/api/internal/agents/reviewer/tasks/42/extra", false},
		{"GET", "/api/internal/agents/reviewer/next-task", false},
		{"POST", "/api/internal/agents/reviewer/tasks/42", false},
		{"GET", "/api/internal/agents/reviewer/tasks/42/ack", false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("X-Internal-Token", token)
			if got := s.requireTaskRouteToken(httptest.NewRecorder(), r); got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
	for _, path := range []string{"/api/internal/events", "/api/internal/user-tokens/validate", "/api/internal/chat/rooms"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("X-Internal-Token", token)
		if s.requireInternalToken(httptest.NewRecorder(), r) {
			t.Fatalf("scoped token authorized global endpoint %s", path)
		}
	}
}

func TestScopedTaskMessages(t *testing.T) {
	s := &Server{internalValidateSecret: "secret"}
	token := tasktoken.Sign("secret", "reviewer", 42, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"agentName":"reviewer","correlationId":"task-42"}`, http.StatusServiceUnavailable},
		{`{"agentName":"other","correlationId":"task-42"}`, http.StatusUnauthorized},
		{`{"agentName":"reviewer","correlationId":"task-43"}`, http.StatusUnauthorized},
		{`{"agentName":"reviewer"}`, http.StatusUnauthorized},
	} {
		r := httptest.NewRequest("POST", "/api/internal/task-messages", strings.NewReader(tc.body))
		r.Header.Set("X-Internal-Token", token)
		w := httptest.NewRecorder()
		s.handleIngestTaskMessage(w, r)
		if w.Code != tc.want {
			t.Errorf("body %s: status %d, want %d", tc.body, w.Code, tc.want)
		}
	}
}

func TestScopedTokenCannotIngestEvents(t *testing.T) {
	s := &Server{internalValidateSecret: "secret"}
	for i, token := range []string{
		tasktoken.Sign("secret", "reviewer", 42, time.Now().Add(time.Hour)),
		tasktoken.Sign("secret", "reviewer", 42, time.Now().Add(-time.Hour)),
		"task-v1.malformed.signature",
	} {
		r := httptest.NewRequest("POST", "/api/internal/events", strings.NewReader(`{}`))
		r.Header.Set("X-Internal-Token", token)
		w := httptest.NewRecorder()
		s.handleIngestEvent(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("global endpoint status %d", w.Code)
		}
		r = httptest.NewRequest("GET", "/api/internal/agents/reviewer/tasks/42", nil)
		r.Header.Set("X-Internal-Token", token)
		if i > 0 && s.requireTaskRouteToken(httptest.NewRecorder(), r) {
			t.Fatal("invalid token authorized task endpoint")
		}
	}
}

package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ainsel-project/ainsel/shared/api/tasktoken"
)

func (s *Server) scopedTaskClaims(r *http.Request) (tasktoken.Claims, bool) {
	c, err := tasktoken.Verify(s.internalValidateSecret, r.Header.Get("X-Internal-Token"), time.Now())
	return c, err == nil
}

func (s *Server) requireTaskRouteToken(w http.ResponseWriter, r *http.Request) bool {
	if c, ok := s.scopedTaskClaims(r); ok {
		path := fmt.Sprintf("/api/internal/agents/%s/tasks/%d", c.Agent, c.Task)
		if (r.Method == http.MethodGet && r.URL.Path == path) ||
			(r.Method == http.MethodPost && (r.URL.Path == path+"/ack" || r.URL.Path == path+"/nack")) {
			return true
		}
		writeError(w, http.StatusUnauthorized, "task token scope mismatch")
		return false
	}
	return s.requireInternalToken(w, r)
}

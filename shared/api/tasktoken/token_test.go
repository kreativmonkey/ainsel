package tasktoken

import (
	"strings"
	"testing"
	"time"
)

func TestCapability(t *testing.T) {
	now := time.Unix(100, 0)
	token := Sign("secret", "reviewer", 42, now.Add(time.Hour))
	c, err := Verify("secret", token, now)
	if err != nil || c.Agent != "reviewer" || c.Task != 42 {
		t.Fatalf("claims: %+v, %v", c, err)
	}
	for _, tc := range []struct {
		secret, token string
		now           time.Time
	}{
		{"wrong", token, now}, {"secret", token, now.Add(time.Hour)},
		{"secret", token + "x", now}, {"secret", "malformed", now},
		{"secret", strings.Replace(token, "task-v1", "task-v2", 1), now},
		{"", token, now},
	} {
		if _, err := Verify(tc.secret, tc.token, tc.now); err == nil {
			t.Errorf("accepted invalid capability: %+v", tc)
		}
	}
}

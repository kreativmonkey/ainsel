// Package tasktoken signs short-lived capabilities for a single native task.
package tasktoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Claims struct {
	Agent   string `json:"agent"`
	Task    int64  `json:"task"`
	Expires int64  `json:"exp"`
}

// Sign derives a task capability without exposing the signing secret.
func Sign(secret, agent string, task int64, expires time.Time) string {
	if secret == "" || agent == "" || task <= 0 {
		return ""
	}
	payload, _ := json.Marshal(Claims{agent, task, expires.Unix()})
	body := "task-v1." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify authenticates the entire capability and rejects expired claims.
func Verify(secret, token string, now time.Time) (Claims, error) {
	var c Claims
	parts := strings.Split(token, ".")
	invalid := errors.New("invalid task token")
	if secret == "" || len(parts) != 3 || parts[0] != "task-v1" {
		return c, invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return c, invalid
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return c, invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, invalid
	}
	if err := json.Unmarshal(payload, &c); err != nil || c.Agent == "" || c.Task <= 0 || c.Expires <= now.Unix() {
		return Claims{}, invalid
	}
	return c, nil
}

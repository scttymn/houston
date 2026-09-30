package models

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

// WebhookVerified is whether a push is really from the repo's git host:
// any one of an HMAC-SHA256 of the body with the project's secret (GitHub's
// X-Hub-Signature-256, which Gitea and Forgejo send too, or their own hex
// headers), or the secret itself as a token (GitLab, Houston). Every
// comparison takes the same time.
func WebhookVerified(h http.Header, body []byte, secret string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range []string{strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256="), h.Get("X-Gitea-Signature"), h.Get("X-Forgejo-Signature")} {
		if sig != "" && subtle.ConstantTimeCompare([]byte(strings.ToLower(sig)), []byte(expected)) == 1 {
			return true
		}
	}
	for _, token := range []string{h.Get("X-Gitlab-Token"), h.Get("X-Houston-Token")} {
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
			return true
		}
	}
	return false
}

// CheckArgs is the change check's: the project whose repo to look at.
type CheckArgs struct {
	ProjectID int64 `json:"project_id"`
}

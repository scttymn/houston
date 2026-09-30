package models

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"
)

// StaleAfter is how long a deploy may go without a word before the next
// one may take over (houston deploy reports at least every 30 s).
const StaleAfter = 2 * time.Minute

// Digest is how a deploy's token is kept: its SHA-256, in hex.
func Digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// OwnedBy is whether token is the one the deploy was started or claimed
// with, compared in constant time.
func (d Deploy) OwnedBy(token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(Digest(token)), []byte(d.TokenDigest)) == 1
}

// Restore is whether it's a restore.
func (d Deploy) Restore() bool { return d.Kind == "restore" }

// InFlight is whether it's running now.
func (d Deploy) InFlight() bool { return d.Status == "in_flight" }

// StatusWords is its status for a sentence: "in flight", "no go".
func (d Deploy) StatusWords() string { return strings.ReplaceAll(d.Status, "_", " ") }

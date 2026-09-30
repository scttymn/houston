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

// A deploy's steps: houston deploy's, after the runner's Test; a restore's
// (the runner reports Switched once Kamal has switched traffic); a copy's
// first deploy's, with the old project's data and the handover.
var (
	DeploySteps  = []string{"Test", "Secrets", "Build", "Snapshot", "Accessories", "Release", "Deploy", "Post-deploy"}
	RestoreSteps = []string{"Prepare", "Image", "Accessories", "Restore data", "Safety snapshot", "Switch", Switched}
	CopySteps    = []string{"Test", "Secrets", "Build", "Snapshot", "Accessories", "Copy data", "Release", "Deploy", "Handover", "Post-deploy"}
)

// Steps are its kind's steps.
func (d Deploy) Steps() []string {
	switch d.Kind {
	case "restore":
		return RestoreSteps
	case "copy":
		return CopySteps
	}
	return DeploySteps
}

// StepState is one step and where it stands: done, current, failed,
// pending, or skipped (a hand houston deploy runs no tests).
type StepState struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// StepStates are its steps and where each stands.
func (d Deploy) StepStates() []StepState {
	steps := d.Steps()
	out := make([]StepState, len(steps))
	at := -1
	for i, s := range steps {
		if s == d.Step {
			at = i
			break
		}
	}
	if at < 0 {
		at = 1
		if d.Runner != "" || d.Restore() {
			at = 0
		}
	}
	for i, name := range steps {
		state := "current"
		switch {
		case d.Status == "queued":
			state = "pending"
		case name == "Test" && d.Runner == "":
			state = "skipped"
		case d.Status == "go" || i < at:
			state = "done"
		case i > at:
			state = "pending"
		case d.Status == "no_go":
			state = "failed"
		}
		out[i] = StepState{Name: name, State: state}
	}
	return out
}

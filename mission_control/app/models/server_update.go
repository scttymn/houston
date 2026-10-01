package models

// UpdateArgs is the server update's job: Follow when it follows a running
// update every few seconds, rather than the minute's look.
type UpdateArgs struct {
	Follow bool `json:"follow,omitempty"`
}

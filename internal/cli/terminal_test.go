package cli

import (
	"os"
	"testing"
)

// /dev/null is a character device but not a terminal: `houston console <
// /dev/null` must not ask docker for a TTY, and init must not prompt.
func TestIsTerminal_DevNullIsNot(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Errorf("isTerminal(%s) = true, want false", os.DevNull)
	}
}

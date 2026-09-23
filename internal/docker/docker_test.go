package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutput_IncludesStderr(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'boom from docker' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	_, err := New().Output("ps")

	if err == nil || !strings.Contains(err.Error(), "boom from docker") {
		t.Errorf("err = %v, want docker's stderr in it", err)
	}
}

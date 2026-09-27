package cli

import (
	"context"
	"fmt"
	"io"
)

// runRepo shows the project's repo URL, or changes it to repoURL: the repo
// moved or was renamed on its git host. Mission Control checks it can read
// the new URL with the project's deploy key, and that its compose.yml names
// the project, before it changes anything.
func runRepo(file, projectFlag, repoURL string, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if repoURL == "" {
		p, err := client.Project(ctx, name)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		fmt.Fprintf(stdout, "%s (%s)\n", p.RepoURL, p.Branch)
		return 0
	}
	set, err := client.SetRepo(ctx, name, repoURL)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s's repo is now %s.\n", name, set)
	return 0
}

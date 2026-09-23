package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/sevenmoons/houston/internal/server"
)

// How often houston link --wait checks access, and for how long.
var (
	linkEvery   = 5 * time.Second
	linkTimeout = 10 * time.Minute
)

// runDeployServer implements houston deploy --server: queue the head of what
// the deploy rule matches; with follow, wait for its result.
func runDeployServer(file, projectFlag string, follow bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	d, err := client.DeployNow(context.Background(), name)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "Queued %s #%d: %s (%s)\n", name, d.Number, short(d.SHA), d.Ref)
	if !follow {
		return 0
	}
	return runDeployShow(file, name, strconv.Itoa(d.Number), true, false, stdout, stderr)
}

// runLink implements houston link: the Add project steps from the CLI.
func runLink(repoURL string, continueID int, branch, composePath string, wait bool, stdout, stderr io.Writer) int {
	client, _, code := remote("", "", false, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	id := continueID
	var access server.Access
	if id == 0 {
		if repoURL == "" {
			fmt.Fprintln(stderr, "houston link: give the repo URL (or --continue <id>)")
			return exitUsage
		}
		link, err := client.CreateLink(ctx, repoURL)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		id, access = link.ID, link.Access
		fmt.Fprintf(stdout, "Deploy key (add it to the repo, read-only):\n  %s\n", link.DeployKey)
	} else {
		var err error
		if access, err = client.LinkAccess(ctx, id); err != nil {
			return remoteFailed(err, stderr)
		}
	}
	deadline := time.Now().Add(linkTimeout)
	for !access.OK {
		if !wait || time.Now().After(deadline) {
			fmt.Fprintf(stderr, "houston link: NO-GO: %s\nAdd the deploy key, then: houston link --continue %d\n", access.Message, id)
			return exitFailure
		}
		time.Sleep(linkEvery)
		var err error
		if access, err = client.LinkAccess(ctx, id); err != nil {
			return remoteFailed(err, stderr)
		}
	}
	fmt.Fprintln(stdout, "GO: Houston can read the repo.")

	read, err := client.LinkRead(ctx, id, branch, composePath)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	if !read.OK {
		fmt.Fprint(stderr, read.Problems)
		return exitUsage
	}
	fmt.Fprintf(stdout, "Found %s at %s@%s\n", read.Found.Name, read.Found.Branch, short(read.Found.SHA))
	for _, v := range read.Found.Variables {
		need := "optional"
		if v.Required {
			need = "required"
		}
		fmt.Fprintf(stdout, "  secret %s (%s): houston secrets set %s --project %s\n", v.Name, need, v.Name, read.Found.Name)
	}
	saved, err := client.LinkSave(ctx, id)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "Linked %s.\nWebhook URL     %s\n", saved.Project, saved.WebhookURL)
	if saved.WebhookSecret != nil {
		fmt.Fprintf(stdout, "Webhook secret  %s\n(send push events as application/json; the secret is shown until the first push arrives)\n", *saved.WebhookSecret)
	}
	return 0
}

func runWebhook(file, projectFlag string, rotate bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	w, err := client.Webhook(context.Background(), name, rotate)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "Webhook URL     %s\n", w.URL)
	if w.Secret != nil {
		fmt.Fprintf(stdout, "Webhook secret  %s\n(shown until the first push arrives)\n", *w.Secret)
	} else {
		fmt.Fprintln(stdout, "Pushes are arriving (verified); the secret isn't shown again. --rotate makes a new one.")
	}
	return 0
}

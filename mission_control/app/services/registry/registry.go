// Package registry is Houston's own image registry, as a deletion needs
// it: a project's manifests
// deleted, then the space freed by the registry's garbage collection once
// nothing is being pushed.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
)

// manifestTypes are what a tag's manifest may be.
var manifestTypes = strings.Join([]string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
}, ", ")

// Config is the registry's configuration file, for its garbage collection.
const Config = "/etc/distribution/config.yml"

// Registry is where it answers, and how to find its container.
type Registry struct {
	// URL is HOUSTON_REGISTRY_URL: http://registry:5000.
	URL string
	// ComposeProject is HOUSTON_COMPOSE_PROJECT: houston.
	ComposeProject string
	Docker         dockercmd.Runner
	HTTP           *http.Client
}

// Error is the registry saying no, or not answering.
type Error string

func (e Error) Error() string { return string(e) }

func (r Registry) request(ctx context.Context, method, path, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.URL+path, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, Error("couldn't reach the registry (" + err.Error() + ")")
	}
	return resp, nil
}

// words is an answer's status and its error codes.
func words(resp *http.Response) string {
	var body struct {
		Errors []struct{ Code string } `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	var codes []string
	for _, e := range body.Errors {
		if e.Code != "" {
			codes = append(codes, e.Code)
		}
	}
	s := fmt.Sprint(resp.StatusCode)
	if codes != nil {
		s += ": " + strings.Join(codes, ", ")
	}
	return s
}

// DeleteRepository deletes every manifest of repository name, and is how
// many (none there: 0). The space is freed later, by Clean.
func (r Registry) DeleteRepository(ctx context.Context, name string) (int, error) {
	listed, err := r.request(ctx, "GET", "/v2/"+name+"/tags/list", "")
	if err != nil {
		return 0, err
	}
	defer listed.Body.Close()
	if listed.StatusCode == 404 {
		return 0, nil
	}
	if listed.StatusCode != 200 {
		return 0, Error(fmt.Sprintf("listing %s's tags: %s", name, words(listed)))
	}
	var tags struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&tags); err != nil {
		return 0, Error(name + "'s tag list wasn't JSON")
	}
	var digests []string
	for _, tag := range tags.Tags {
		head, err := r.request(ctx, "HEAD", "/v2/"+name+"/manifests/"+tag, manifestTypes)
		if err != nil {
			return 0, err
		}
		head.Body.Close()
		if head.StatusCode == 404 {
			continue
		}
		digest := head.Header.Get("Docker-Content-Digest")
		if head.StatusCode != 200 || digest == "" {
			return 0, Error(fmt.Sprintf("reading %s:%s: %d", name, tag, head.StatusCode))
		}
		if !contains(digests, digest) {
			digests = append(digests, digest)
		}
	}
	for _, digest := range digests {
		deleted, err := r.request(ctx, "DELETE", "/v2/"+name+"/manifests/"+digest, "")
		if err != nil {
			return 0, err
		}
		if deleted.StatusCode != 202 && deleted.StatusCode != 404 {
			msg := words(deleted)
			deleted.Body.Close()
			return 0, Error(fmt.Sprintf("deleting %s@%s: %s", name, digest, msg))
		}
		deleted.Body.Close()
	}
	return len(digests), nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Container is the running registry's container id, or "".
func (r Registry) Container(ctx context.Context) string {
	ran := r.Docker.Run(ctx, []string{"ps", "-q", "--filter", "label=com.docker.compose.project=" + r.ComposeProject,
		"--filter", "label=com.docker.compose.service=registry"}, dockercmd.Opts{Timeout: 10 * time.Second})
	if fields := strings.Fields(ran.Output); ran.OK && len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// DeletesEnabled is whether the registry lets manifests be deleted (the
// installer turns it on).
func (r Registry) DeletesEnabled(ctx context.Context) bool {
	id := r.Container(ctx)
	if id == "" {
		return false
	}
	ran := r.Docker.Run(ctx, []string{"inspect", "--format", "{{json .Config.Env}}", id}, dockercmd.Opts{Timeout: 10 * time.Second})
	var env []string
	return ran.OK && json.Unmarshal([]byte(ran.Output), &env) == nil && contains(env, "REGISTRY_STORAGE_DELETE_ENABLED=true")
}

// ErrBusy is the registry's clean-up waiting: a deploy is pushing, or a
// clean-up already runs.
var ErrBusy = errors.New("busy")

// Collect runs the registry's garbage collection: what it freed, in its
// words (or "").
func (r Registry) Collect(ctx context.Context) (string, error) {
	id := r.Container(ctx)
	if id == "" {
		return "", Error(fmt.Sprintf("no registry container (the %s project's registry service) is running", r.ComposeProject))
	}
	ran := r.Docker.Run(ctx, []string{"exec", id, "registry", "garbage-collect", Config}, dockercmd.Opts{Timeout: 30 * time.Minute})
	if !ran.OK {
		lines := strings.Split(strings.TrimSpace(ran.Output), "\n")
		msg := strings.TrimSpace(strings.Join(lines[max(0, len(lines)-3):], "\n"))
		if len(msg) > 1000 {
			msg = msg[:997] + "..."
		}
		return "", Error(msg)
	}
	if m := freed.FindString(ran.Output); m != "" {
		return m, nil
	}
	return "", nil
}

var freed = regexp.MustCompile(`\d+ blobs and \d+ manifests eligible for deletion`)

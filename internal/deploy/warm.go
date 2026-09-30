package deploy

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// Warm-up's limits: files in all, fetched at once, and time in all (a
// variable, for its test).
const (
	warmMaxFiles = 300
	warmWorkers  = 4
)

var warmTimeout = 30 * time.Second

// warmUp fetches each host's home page and every file it asks for from its
// own site, through the public address, so Cloudflare's cache has them
// before the first visitor asks (docs/plans/warm-up.md). A host that
// answers with anything but a page (a redirect) is skipped. Nothing it
// finds fails the deploy: it logs what it did.
func warmUp(ctx context.Context, client *http.Client, hosts []string, logf func(format string, args ...any)) {
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	budget := warmMaxFiles
	for _, host := range hosts {
		page := "https://" + host + "/"
		files, err := pageFiles(ctx, client, page)
		if err != nil {
			logf("warm-up: %s skipped: %v\n", page, err)
			continue
		}
		if len(files) > budget {
			logf("warm-up: %s asks for %d files; fetching the first %d\n", page, len(files), budget)
			files = files[:budget]
		}
		budget -= len(files)
		cold, failed := fetchAll(ctx, client, files)
		line := fmt.Sprintf("ok  warmed %s: %d file%s", page, len(files), plural(len(files)))
		if cold > 0 {
			line += fmt.Sprintf(", %d newly cached", cold)
		}
		logf("%s\n", line)
		for _, f := range failed {
			logf("warm-up: %s\n", f)
		}
		if ctx.Err() != nil {
			logf("warm-up: stopped after %s\n", warmTimeout)
			return
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// pageFiles are the files a page asks for from its own site, in order,
// each once.
func pageFiles(ctx context.Context, client *http.Client, page string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", page, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Houston deploy warm-up")
	resp, err := noRedirects(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("it answers %d", resp.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/html" {
		return nil, fmt.Errorf("it isn't a page (%s)", mt)
	}
	doc, err := html.Parse(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(page)
	seen := map[string]bool{}
	var files []string
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.HasPrefix(ref, "#") {
			return
		}
		u, err := base.Parse(ref) // a data: URL has no host: not this site's
		if err != nil || u.Host != base.Host || (u.Scheme != "https" && u.Scheme != "http") {
			return
		}
		u.Fragment = ""
		if s := u.String(); !seen[s] {
			seen[s] = true
			files = append(files, s)
		}
	}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		attr := func(name string) string {
			for _, a := range n.Attr {
				if a.Key == name {
					return a.Val
				}
			}
			return ""
		}
		switch n.Data {
		case "link":
			add(attr("href"))
		case "script":
			add(attr("src"))
		case "img", "source":
			add(attr("src"))
			for _, candidate := range strings.Split(attr("srcset"), ",") {
				if fields := strings.Fields(candidate); len(fields) > 0 {
					add(fields[0])
				}
			}
		case "video":
			add(attr("src"))
			add(attr("poster"))
		}
	}
	return files, nil
}

// noRedirects is client without following redirects: a host that redirects
// isn't this host's page.
func noRedirects(client *http.Client) *http.Client {
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// fetchAll fetches files, warmWorkers at a time: cold is how many Cloudflare
// fetched from here (cf-cache-status other than HIT), failed each that
// didn't load.
func fetchAll(ctx context.Context, client *http.Client, files []string) (cold int, failed []string) {
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range warmWorkers {
		wg.Go(func() {
			for f := range jobs {
				status, cache, err := fetch(ctx, client, f)
				mu.Lock()
				switch {
				case err != nil:
					failed = append(failed, fmt.Sprintf("%s: %v", f, err))
				case status != http.StatusOK:
					failed = append(failed, fmt.Sprintf("%s: %d", f, status))
				case cache != "" && cache != "HIT":
					cold++
				}
				mu.Unlock()
			}
		})
	}
	for _, f := range files {
		select {
		case jobs <- f:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return cold, failed
}

func fetch(ctx context.Context, client *http.Client, file string) (status int, cache string, err error) {
	if ctx.Err() != nil {
		return 0, "", ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", file, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("User-Agent", "Houston deploy warm-up")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // the whole file, so Cloudflare keeps it
	return resp.StatusCode, resp.Header.Get("Cf-Cache-Status"), nil
}

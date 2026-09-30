// Package release checks for Houston's latest release on GitHub (the Rails
// app's LatestRelease), so the flight board can say when one is out.
package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// Timeout is how long GitHub may take.
const Timeout = 10 * time.Second

var tagFormat = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// Checker asks GitHub.
type Checker struct {
	// API is GitHub's API ("https://api.github.com", or a test's).
	API string
	// Repo is Houston's (HOUSTON_REPO, default scttymn/houston).
	Repo string
	// Version is this Houston's, for the User-Agent.
	Version string
	HTTP    *http.Client
}

// Latest is the latest release's tag and page, checked: a release tag, and
// this repo's release page.
func (c Checker) Latest(ctx context.Context) (tag, url string, err error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.API+"/repos/"+c.Repo+"/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "houston/"+c.Version)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	var release struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", "", err
	}
	if !tagFormat.MatchString(release.Tag) {
		return "", "", fmt.Errorf("%q isn't a release tag", release.Tag)
	}
	if !strings.HasPrefix(release.URL, "https://github.com/"+c.Repo+"/releases/") {
		return "", "", fmt.Errorf("%q isn't this repo's release page", release.URL)
	}
	return release.Tag, release.URL, nil
}

// Check asks GitHub for the latest release and keeps it on the
// installation: its tag, and whether it's news (the flight board says so).
// A failed check keeps what was known.
func (c Checker) Check(ctx context.Context, d *db.DB, now time.Time) (tag string, changed bool, err error) {
	inst, err := models.New(d.Read).CurrentInstallation(ctx)
	if err != nil {
		return "", false, err
	}
	tag, url, err := c.Latest(ctx)
	if err != nil {
		return "", false, err
	}
	err = models.New(d.Write).SetLatestRelease(ctx, models.SetLatestReleaseParams{LatestRelease: tag, LatestReleaseUrl: url,
		LatestReleaseCheckedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now})
	return tag, tag != inst.LatestRelease, err
}

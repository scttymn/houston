package api

import (
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
)

// Webhooks' limits, a minute at a time (docs/plans/security-fixes.md,
// M4): requests that don't verify are counted per client address and name,
// and checked before the body is read, so junk can't block real pushes
// from another address, nor (sent through GitHub, from GitHub's addresses)
// to another project. Verified ones are counted per project.
const (
	maxWebhookBody = 5 << 20
	unverifiedMax  = 30
	verifiedMax    = 60
)

var webhookName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Webhook is POST hooks.<base>/{name}: the doorbell. A verified push only
// makes Houston look at the repo's refs itself (the change check); the
// payload is never read. Anything unverified gets the same empty 404 as
// an unknown project. The polling check catches a push whose ring was lost.
func (c Controller) Webhook(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("name")
	if !webhookName.MatchString(name) {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	now := time.Now()
	window := now.Truncate(time.Minute)
	sender := "webhooks:unverified:" + web.ClientIP(r) + ":" + name
	if c.Limits.Count(sender, window) >= unverifiedMax {
		w.WriteHeader(http.StatusTooManyRequests)
		return nil
	}
	unverified := func(status int) error {
		c.Limits.Allow(sender, unverifiedMax, time.Minute, window)
		w.WriteHeader(status)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		return unverified(http.StatusRequestEntityTooLarge)
	}
	ctx := r.Context()
	q := models.New(c.DB.Read)
	project, err := models.WebhookTarget(ctx, q, name)
	if err != nil || !models.WebhookVerified(r.Header, body, project.WebhookSecret.Reveal()) {
		return unverified(http.StatusNotFound)
	}
	if !c.Limits.Allow("webhooks:verified:"+project.Name, verifiedMax, time.Minute, window) {
		w.WriteHeader(http.StatusTooManyRequests)
		return nil
	}
	if err := models.New(c.DB.Write).MarkWebhookVerified(ctx, models.MarkWebhookVerifiedParams{WebhookVerifiedAt: sqlTime(now), ID: project.ID}); err != nil {
		return err
	}
	if _, err := c.Check.Enqueue(ctx, models.CheckArgs{ProjectID: project.ID}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}

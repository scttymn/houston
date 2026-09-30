// Package projects is the flight board and a project's page (Rails'
// ProjectsController and its views).
package projects

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/gantry/text"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

var stateLabels = map[string]string{"queued": "QUEUED", "in_flight": "IN FLIGHT", "no_go": "NO-GO", "go": "GO", "standby": "STANDBY", "hold": "HOLD"}

// StateLabel is a status as the pages write it: "IN FLIGHT".
func StateLabel(status string) string { return stateLabels[status] }

// Dasherize is Rails': in_flight → in-flight.
func Dasherize(s string) string { return strings.ReplaceAll(s, "_", "-") }

var notWord = regexp.MustCompile(`[^a-z0-9]+`)

// Parameterize is Rails': "DNS PENDING" → "dns-pending".
func Parameterize(s string) string {
	return strings.Trim(notWord.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// DurationWords is 108 s as "1m 48s".
func DurationWords(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", s/60, s%60)
}

// MissionElapsed is 52 s as "T+00:52", as the deploy page counts.
func MissionElapsed(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("T+%02d:%02d", s/60, s%60)
}

// ServicesWords are a project's other services with their images: "db ·
// postgres:17, cache".
func ServicesWords(p models.Project) string {
	var parts []string
	for _, s := range p.Accessories() {
		if image := p.ServiceImage(s); image != "" {
			s += " · " + image
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "no services"
	}
	return strings.Join(parts, ", ")
}

// RefName is a ref without its kind: refs/heads/main → main.
func RefName(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
}

// ShortTime is Rails' l(t, format: :short): "30 Sep 14:01".
func ShortTime(t time.Time) string { return t.Format("02 Jan 15:04") }

var trailingZeros = regexp.MustCompile(`\.?0+$`)

// CoresWords is 0.15 as "0.15 cores" and 1 as "1 core"; under a hundredth,
// three places, and "<0.001 cores" below that, so a running app never
// reads as none.
func CoresWords(cores float64) string {
	format := "%.2f"
	if cores > 0 && cores < 0.01 {
		format = "%.3f"
	}
	n := trailingZeros.ReplaceAllString(fmt.Sprintf(format, cores), "")
	if n == "" {
		n = "0"
	}
	if n == "0" && cores > 0 {
		return "<0.001 cores"
	}
	if n == "1" {
		return "1 core"
	}
	return n + " cores"
}

// SizeWords is a size as the board writes it: "0 B", "1.21 KB".
func SizeWords(bytes int64) string {
	s := text.ByteSize(bytes)
	s = strings.TrimSuffix(strings.TrimSuffix(s, " Bytes"), " Byte")
	if !strings.Contains(s, " ") {
		s += " B"
	}
	return s
}

// PercentWords is 42.4 as "42%", 0.04 as "<1%", and none as "—".
func PercentWords(percent float64, ok bool) string {
	switch {
	case !ok:
		return "—"
	case percent > 0 && percent < 0.5:
		return "<1%"
	}
	return fmt.Sprintf("%d%%", int(math.Round(percent)))
}

// Ticks is how many of a gauge's ten ticks light: one per 10% of the whole,
// and at least one for any use.
func Ticks(percent float64, ok bool) int {
	if !ok || percent <= 0 {
		return 0
	}
	return max(1, min(10, int(math.Round(percent/10))))
}

// near is where a gauge turns amber.
const near = 85

// UsageTitle is a gauge's hover: what it's a share of.
func UsageTitle(label, amount, limit string, percent float64, ok, ofHost bool) string {
	switch {
	case limit == "":
		return label + " " + amount + " · no limit set"
	case ofHost:
		return label + " " + amount + " of the host's " + limit + " (" + PercentWords(percent, ok) + ") · no limit set"
	}
	return label + " " + amount + " of " + limit + " (" + PercentWords(percent, ok) + ")"
}

// Ago is Rails' time_ago_in_words.
func Ago(t, now time.Time) string { return text.TimeAgo(t, now) }

// ruleShort is the deploy rule as the board's row writes it: "main", "tags
// matching v*".
func ruleShort(p models.Project) string {
	return strings.TrimPrefix(strings.ToLower(p.DeployRuleWords()), "every commit to ")
}

// chipState is a host chip's modifier: its state's, or ok without one.
func chipState(state string) string {
	if s := Parameterize(state); s != "" {
		return s
	}
	return "ok"
}

// finished is when a deploy ended (or began, before it has).
func finished(d *models.Deploy) time.Time {
	if d.FinishedAt.Valid {
		return d.FinishedAt.Time
	}
	return d.CreatedAt
}

// wholeWords is what a share is of, in words, or "" when unknown.
func wholeWords(whole float64, words func(float64) string) string {
	if whole <= 0 {
		return ""
	}
	return words(whole)
}

func sizeOf(bytes float64) string { return SizeWords(int64(bytes)) }

// connectionsWords is "2 connections to Cloudflare".
func connectionsWords(n int) string { return text.Count(n, "connection") + " to Cloudflare" }

// adminGo is what a GO route to admin.<base> means from here.
func adminGo(onLAN bool) string {
	if onLAN {
		return "Reaches this Mission Control through Cloudflare"
	}
	return "You're using it now"
}

// upcaseFirst is Rails' upcase_first.
func upcaseFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// backupWords are a backup's kind and size: "auto · 1.24 MB".
func backupWords(b models.BackupRun) string {
	if b.Bytes.Valid {
		return b.Kind + " · " + SizeWords(b.Bytes.Int64)
	}
	return b.Kind
}

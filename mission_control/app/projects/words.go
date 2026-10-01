// Package projects is the flight board and a project's page.
package projects

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/text"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/backup"
	"github.com/scttymn/houston/mission_control/app/services/removal"
	"github.com/scttymn/houston/mission_control/app/shared/layout"
)

var stateLabels = map[string]string{"queued": "QUEUED", "in_flight": "IN FLIGHT", "no_go": "NO-GO", "go": "GO", "standby": "STANDBY", "hold": "HOLD"}

// StateLabel is a status as the pages write it: "IN FLIGHT".
func StateLabel(status string) string { return stateLabels[status] }

// Dasherize is s with dashes for underscores: in_flight → in-flight.
func Dasherize(s string) string { return strings.ReplaceAll(s, "_", "-") }

var notWord = regexp.MustCompile(`[^a-z0-9]+`)

// Parameterize is s as a class name: "DNS PENDING" → "dns-pending".
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

// ShortTime is t, short: "30 Sep 14:01".
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

// Ago is how long ago t was, in words: "about 2 hours".
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

// upcaseFirst is s with its first letter a capital.
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

// hostState is a hostname's DNS state on the project page: its own, or
// WILDCARD for its name under the base while a wildcard serves them.
func hostState(s Show, host string) string {
	if state := s.P.DomainStates.V[host].State; state != "" {
		return state
	}
	if host == s.P.Host(s.Inst.BaseDomain) && s.Inst.DnsMode != "per_host" {
		return "WILDCARD"
	}
	return ""
}

func orUnknown(s string) string { return orWords(s, "unknown") }

// orWords is s, or words when it's blank.
func orWords(s, words string) string {
	if s == "" {
		return words
	}
	return s
}

// sections are the project page's menu: alphabetical, to scan, the Danger
// zone last, where nobody lands on it by accident.
func sections(s Show) [][2]string {
	var out [][2]string
	if s.P.RepoUrl != "" {
		out = append(out, [2]string{"Connect pushes", "webhook"})
	}
	out = append(out, [2]string{"Deploy history", "history"})
	if !s.P.MaintenanceSince.Valid {
		out = append(out, [2]string{"Maintenance page", "maintenance"})
	}
	out = append(out, [2]string{"Secrets", "secrets"}, [2]string{"Snapshots", "snapshots"})
	if len(s.P.Volumes.V) > 0 {
		out = append(out, [2]string{"Volumes", "volumes"})
	}
	return append(out, [2]string{"Danger zone", "danger"})
}

func maintenanceWords(p models.Project, page layout.Page) string {
	since := p.MaintenanceSince.Time
	words := p.Name + " shows a maintenance page since " + ShortTime(since.In(zoneOf(page))) + " (" + p.MaintenanceBy + ")"
	if p.MaintenanceMessage != "" {
		words += ": “" + p.MaintenanceMessage + "”"
	}
	return words + ". It stays up until you turn it off."
}

// deletionStep is the step a deletion is at, in words: " (final
// snapshot)".
func deletionStep(d *models.ProjectDeletion) string {
	words := removal.StepWords[d.Step]
	if words == "" {
		return ""
	}
	return " (" + strings.ToLower(words[:1]) + words[1:] + ")"
}

func servesWords(c *models.ProjectCopy) string {
	if len(c.HandedOver.V) == 0 {
		return ""
	}
	return ", which serves " + web.Sentence(c.HandedOver.V) + " now"
}

func runningWords(d *models.Deploy) string {
	if d == nil {
		return "what it served"
	}
	return d.ShortSha()
}

// lastLines are s's last n lines.
func lastLines(s string, n int) string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines[max(0, len(lines)-n):], "")
}

func countWords(n int, word string) string { return text.Count(n, word) }

func historyWhat(d models.Deploy) string {
	what := RefName(d.Ref)
	if d.Restore() {
		what = "Restore"
	}
	if d.Fresh {
		what += " · Rebuild"
	}
	return what
}

func historyNote(d models.Deploy, now time.Time, page layout.Page) string {
	if d.Error != "" {
		return d.Error
	}
	return Ago(d.CreatedAt, now) + " ago · " + ShortTime(d.CreatedAt.In(zoneOf(page)))
}

func historyTime(d models.Deploy, now time.Time) string {
	switch d.Status {
	case "queued":
		return "—"
	case "in_flight":
		return MissionElapsed(d.Duration(now))
	}
	return DurationWords(d.Duration(now))
}

func missingWords(missing []string) string {
	verb := "have"
	if len(missing) == 1 {
		verb = "has"
	}
	return "The next deploy will stop until " + web.Sentence(missing) + " " + verb + " a value."
}

func removeWords(v models.Variable) string {
	if v.Required {
		return "Remove " + v.Name + "? The next deploy will stop until it has a value again."
	}
	return "Remove " + v.Name + "? The app gets it blank."
}

func valuePlaceholder(set bool) string {
	if set {
		return "New value"
	}
	return "Paste value"
}

func saveLabel(set bool) string {
	if set {
		return "Replace"
	}
	return "Save value"
}

// choiceLabel is a location as a choice: "nas (NFS, 10.0.1.20:/volume1)".
func choiceLabel(l models.StorageLocation) string {
	kind := strings.ToUpper(l.Kind)
	if l.Kind == "local" {
		kind = "local folder"
	}
	return l.Name + " (" + kind + ", " + models.WhereItIs(l) + ")"
}

func nfsWords(s Show, row VolumeRow) string {
	words := "Live SQLite over a network share risks corruption"
	for _, v := range s.SQLiteVolumes {
		if v == row.Volume.Name {
			return words + ": this volume holds SQLite databases."
		}
	}
	return words + "."
}

func snapshotsSrc(s Show) string {
	src := "/projects/" + s.P.Name + "/snapshots"
	if s.SnapshotsKind == "deploy" || s.SnapshotsKind == "settings" {
		src += "?kind=" + s.SnapshotsKind
	}
	return src
}

func runStatus(run *models.BackupRun) string {
	if run == nil {
		return ""
	}
	return run.Status
}

func runState(run *models.BackupRun, now time.Time) string {
	if run == nil {
		return ""
	}
	if run.Stale(now) {
		return "stale"
	}
	return run.Status
}

func startedAt(run *models.BackupRun) time.Time {
	if run.StartedAt.Valid {
		return run.StartedAt.Time
	}
	return run.CreatedAt
}

func endedAt(run *models.BackupRun) time.Time {
	if run.FinishedAt.Valid {
		return run.FinishedAt.Time
	}
	return run.UpdatedAt
}

func humanSize(bytes sql.NullInt64) string {
	if !bytes.Valid {
		return ""
	}
	return text.ByteSize(bytes.Int64)
}

// zoneOf is the page's time zone (the installation's, as the page filter
// set it).
func zoneOf(page layout.Page) *time.Location {
	if page.Zone != nil {
		return page.Zone
	}
	return time.UTC
}

func snapshotsTab(p models.Project, kind string) string {
	path := "/projects/" + p.Name + "/snapshots"
	if kind != "auto" {
		path += "?kind=" + kind
	}
	return path
}

// snapshotWhy is why a snapshot was taken.
func snapshotWhy(s backup.Snapshot) string {
	switch s.Reason {
	case "manual":
		return "created by hand"
	case "schedule":
		return "daily"
	case "restore":
		return "before a restore"
	case "delete":
		return "before it was deleted"
	}
	if s.Deploy != nil {
		return "before deploy #" + strconv.FormatInt(*s.Deploy, 10)
	}
	return s.Reason
}

func snapshotSize(s backup.Snapshot) string {
	if s.Bytes == nil {
		return "—"
	}
	return text.ByteSize(*s.Bytes)
}

func downloadTitle(s backup.Snapshot) string {
	title := "Download everything in this snapshot"
	if s.Bytes != nil {
		title += ", about " + text.ByteSize(*s.Bytes)
	}
	return title
}

func defaultName(l *models.StorageLocation) string {
	if l == nil {
		return "none yet"
	}
	return l.Name
}

// notBackedUp are the services a backup doesn't hold: they start empty
// after a restore.
func notBackedUp(p models.Project) []string {
	var out []string
	for _, s := range p.Accessories() {
		held := false
		for _, d := range p.Databases.V {
			held = held || d.Service == s
		}
		if !held {
			out = append(out, s)
		}
	}
	return out
}

func runningWordsOr(d *models.Deploy, none string) string {
	if d == nil {
		return none
	}
	return d.ShortSha()
}

// inWhere is where the backups are, put into format ("" without any).
func inWhere(locations []string, format string) string {
	if len(locations) == 0 {
		return ""
	}
	return fmt.Sprintf(format, web.Sentence(locations))
}

func repoWhere(p models.Project) string {
	if p.RepoUrl == "" {
		return ""
	}
	return " (" + p.RepoUrl + ")"
}

func backupsToo(d models.ProjectDeletion) string {
	if d.DeleteBackups {
		return " · backups deleted too"
	}
	return ""
}

func otherBackups(d models.ProjectDeletion) string {
	if d.DeleteBackups {
		return ""
	}
	return ", with its other backups"
}

var deletionStateWords = map[string]string{"done": "DONE", "current": "RUNNING", "failed": "FAILED", "pending": ""}

// deletionSteps are a deletion's steps in words, each with its state, as a
// deploy page shows them.
func deletionSteps(d models.ProjectDeletion) [][2]string {
	at := -1
	for i, s := range removal.Steps {
		if s == d.Step {
			at = i
		}
	}
	out := make([][2]string, len(removal.Steps))
	for i, s := range removal.Steps {
		state := "pending"
		switch {
		case d.Status == "go":
			state = "done"
		case at < 0 || i > at:
		case i < at:
			state = "done"
		case d.Status == "running":
			state = "current"
		case d.Status == "no_go":
			state = "failed"
		}
		out[i] = [2]string{removal.StepWords[s], state}
	}
	return out
}

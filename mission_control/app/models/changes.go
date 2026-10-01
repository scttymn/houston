package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
)

// DeployRule is compose.yml's x-houston deploy rule: every commit to a
// branch, or tags matching a pattern.
type DeployRule struct {
	On     string `json:"on"`
	Branch string `json:"branch"`
	Tags   string `json:"tags"`
}

// Rule is p's deploy rule.
func (p Project) Rule() DeployRule {
	var r DeployRule
	json.Unmarshal(p.DeployRule.V, &r)
	return r
}

// Words are the rule in words: "every commit to main", "tags matching v*".
func (r DeployRule) Words() string {
	if r.On == "tag" {
		return "Tags matching " + or(r.Tags, "v*")
	}
	return "Every commit to " + or(r.Branch, "main")
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Matching are the refs the rule deploys, ref → commit. An annotated tag's
// peeled ref (^{}) names the commit it points at.
func (r DeployRule) Matching(refs map[string]string) map[string]string {
	out := map[string]string{}
	if r.On == "tag" {
		glob := fnmatch(or(r.Tags, "v*"))
		for ref, sha := range refs {
			tag, ok := strings.CutPrefix(ref, "refs/tags/")
			if !ok || strings.Contains(tag, "^") || !glob.MatchString(tag) {
				continue
			}
			if peeled, ok := refs[ref+"^{}"]; ok {
				sha = peeled
			}
			out[ref] = sha
		}
		return out
	}
	ref := "refs/heads/" + or(r.Branch, "main")
	if sha, ok := refs[ref]; ok {
		out[ref] = sha
	}
	return out
}

// fnmatch is a shell pattern as Ruby's File.fnmatch reads it (no flags):
// * any run of characters, ? one, [...] a class.
func fnmatch(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			if end := strings.IndexByte(pattern[i:], ']'); end > 1 {
				class := pattern[i+1 : i+end]
				if strings.HasPrefix(class, "!") {
					class = "^" + class[1:]
				}
				b.WriteString("[" + class + "]")
				i += end
				continue
			}
			b.WriteString(`\[`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile(`^$.`) // a pattern that can't be read matches nothing
	}
	return re
}

// newest is the ref of wanted to deploy now: the highest by its numbers
// (v1.10 after v1.9), then by name.
func newest(wanted map[string]string) string {
	refs := make([]string, 0, len(wanted))
	for ref := range wanted {
		refs = append(refs, ref)
	}
	numbers := regexp.MustCompile(`\d+`)
	key := func(ref string) []int {
		var n []int
		for _, s := range numbers.FindAllString(ref, -1) {
			v, _ := strconv.Atoi(s)
			n = append(n, v)
		}
		return n
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := key(refs[i]), key(refs[j])
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return refs[i] < refs[j]
	})
	return refs[len(refs)-1]
}

// QueueDeploy queues a deploy of sha for a runner, in tx: one queued a
// project, so a newer push switches it (a burst of pushes deploys once, the
// newest). fresh is a rebuild, without Docker's layer cache; a deploy
// already queued becomes one when one is asked for, and a push never undoes
// that.
func QueueDeploy(ctx context.Context, tx *db.Tx, p Project, sha, ref string, fresh bool, now time.Time) (Deploy, error) {
	q := New(tx)
	if err := refuseWhileDeleting(ctx, q, p); err != nil {
		return Deploy{}, err
	}
	queued, err := q.QueuedDeploy(ctx, p.ID)
	if err == nil {
		log := queued.Log
		if !(fresh && queued.Sha == sha && queued.Ref == ref) {
			log += "A newer push switched to " + sha[:min(7, len(sha))] + " (" + ref + ").\n"
		}
		if fresh && !queued.Fresh {
			log += "Rebuild asked for: it builds without Docker's layer cache.\n"
		}
		return q.RequeueDeploy(ctx, RequeueDeployParams{Sha: sha, Ref: ref, Fresh: queued.Fresh || fresh, Log: log, UpdatedAt: now, ID: queued.ID})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Deploy{}, err
	}
	number, err := q.NextDeployNumber(ctx, p.ID)
	if err != nil {
		return Deploy{}, err
	}
	d, err := q.CreateDeploy(ctx, CreateDeployParams{ProjectID: p.ID, Number: number, Kind: "deploy", Status: "queued", Sha: sha, Ref: ref,
		Generation: p.DataGeneration, HeartbeatAt: now})
	if err != nil || !fresh {
		return d, err
	}
	return q.RequeueDeploy(ctx, RequeueDeployParams{Sha: sha, Ref: ref, Fresh: true, Log: d.Log, UpdatedAt: now, ID: d.ID})
}

// RefReader reads a project's repo's refs: ref → commit, or why not (a
// gitremote.Git's Refs).
type RefReader func(ctx context.Context, p Project) (map[string]string, string)

// CheckForChanges reads p's refs and queues a deploy for each the rule
// matches that moved since the last check; what was seen is saved in the
// same transaction. While a restore or copy is underway nothing queues and
// what was seen stays, so the check after it queues what was pushed
// meanwhile.
func CheckForChanges(ctx context.Context, d *db.DB, refs RefReader, p Project, now time.Time) ([]Deploy, error) {
	q := New(d.Read)
	if underway, err := q.RestoreOrCopyUnderway(ctx, p.ID); err != nil || underway {
		return nil, err
	}
	if deleting, err := Deleting(ctx, q, p.ID); err != nil || deleting {
		return nil, err
	}
	found, problem := refs(ctx, p)
	if problem != "" {
		return nil, New(d.Write).SetCheckError(ctx, SetCheckErrorParams{LastCheckError: problem, LastCheckedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: p.ID})
	}
	wanted := p.Rule().Matching(found)
	var queued []Deploy
	err := d.Tx(ctx, func(tx *db.Tx) error {
		// Another check (a webhook's, the poll's) may have queued since this
		// one read the project: compare with what's seen now, in the write.
		current, err := New(tx).ProjectByID(ctx, p.ID)
		if err != nil {
			return err
		}
		refs := make([]string, 0, len(wanted))
		for ref := range wanted {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			if current.SeenRefs.V[ref] == wanted[ref] {
				continue
			}
			d, err := QueueDeploy(ctx, tx, current, wanted[ref], ref, false, now)
			if err != nil {
				return err
			}
			queued = append(queued, d)
		}
		return New(tx).SetChecked(ctx, SetCheckedParams{SeenRefs: Refs{V: wanted}, LastCheckedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: p.ID})
	})
	return queued, err
}

// QueueHead is deploy now: the current head of what the rule deploys,
// moved or not (after a HOLD is fixed, nothing moved, but it still has to
// deploy): the branch's head, or the highest tag. Refused while a restore
// or copy is underway, or when nothing matches.
func QueueHead(ctx context.Context, d *db.DB, refs RefReader, p Project, fresh bool, now time.Time) (Deploy, error) {
	q := New(d.Read)
	underway, err := q.RestoreOrCopyUnderway(ctx, p.ID)
	if err != nil {
		return Deploy{}, err
	}
	if underway {
		return Deploy{}, Refused{"a restore of " + p.Name + " is queued or in flight; deploy after it"}
	}
	if err := refuseWhileDeleting(ctx, q, p); err != nil {
		return Deploy{}, err
	}
	found, problem := refs(ctx, p)
	if problem != "" {
		return Deploy{}, Refused{problem}
	}
	wanted := p.Rule().Matching(found)
	if len(wanted) == 0 {
		return Deploy{}, Refused{"nothing in the repo matches the deploy rule (" + strings.ToLower(p.Rule().Words()) + ")"}
	}
	ref := newest(wanted)
	var queued Deploy
	err = d.Tx(ctx, func(tx *db.Tx) (err error) {
		if queued, err = QueueDeploy(ctx, tx, p, wanted[ref], ref, fresh, now); err != nil {
			return err
		}
		return New(tx).SetChecked(ctx, SetCheckedParams{SeenRefs: Refs{V: wanted}, LastCheckedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: p.ID})
	})
	return queued, err
}

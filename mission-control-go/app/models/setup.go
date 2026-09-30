package models

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scttymn/gantry/auth"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"
	"golang.org/x/crypto/bcrypt"
)

// setupAlphabet has no 0/O or 1/I, so a code read off a terminal can't be
// mistyped that way.
const setupAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// ErrSetUp is setup asked for once the admin exists.
var ErrSetUp = errors.New("setup is complete")

// IssueSetupCode replaces any code with a new one and returns it, like
// "K7QM-2XHD"; only its digest is kept. None once the admin exists.
func IssueSetupCode(ctx context.Context, d *db.DB) (string, error) {
	code := make([]byte, 8)
	for i := range code {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(setupAlphabet))))
		if err != nil {
			return "", err
		}
		code[i] = setupAlphabet[n.Int64()]
	}
	digest, err := auth.Hash(string(code))
	if err != nil {
		return "", err
	}
	err = d.Tx(ctx, func(tx *db.Tx) error {
		q := New(tx)
		switch set, err := q.UserExists(ctx); {
		case err != nil:
			return err
		case set:
			return ErrSetUp
		}
		if err := q.ForgetSetupCodes(ctx); err != nil {
			return err
		}
		return q.AddSetupCode(ctx, digest)
	})
	if err != nil {
		return "", err
	}
	return string(code[:4]) + "-" + string(code[4:]), nil
}

// normalCode is a typed code as it's kept: case and the dash don't matter.
func normalCode(typed string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return -1
	}, strings.ToUpper(typed))
}

// matchingSetupCode is the kept code a typed one matches: its id, or 0.
func matchingSetupCode(ctx context.Context, q *Queries, typed string) (int64, error) {
	code := normalCode(typed)
	if len(code) != 8 {
		return 0, nil
	}
	codes, err := q.SetupCodes(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range codes {
		if bcrypt.CompareHashAndPassword([]byte(c.CodeDigest), []byte(code)) == nil {
			return c.ID, nil
		}
	}
	return 0, nil
}

// email is Ruby's URI::MailTo::EMAIL_REGEXP, which the Rails app checked.
var email = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// Admin is first run's first step: the setup code the installer printed,
// and the admin's email and password.
type Admin struct {
	Code, EmailAddress, Password, PasswordConfirmation string
}

// check is what's wrong with the form, by field ("base" for the form as a
// whole), before the code is looked at.
func (a Admin) check() web.Invalid {
	errs := web.Invalid{}
	if a.EmailAddress == "" {
		errs["email_address"] = append(errs["email_address"], "can't be blank")
	}
	if !email.MatchString(a.EmailAddress) {
		errs["email_address"] = append(errs["email_address"], "doesn't look like an email address")
	}
	switch {
	case utf8.RuneCountInString(a.Password) < 12:
		errs["password"] = append(errs["password"], "needs at least 12 characters")
	case len(a.Password) > 72: // all bcrypt reads
		errs["password"] = append(errs["password"], "needs at most 72 bytes")
	}
	if a.PasswordConfirmation != a.Password {
		errs["password_confirmation"] = append(errs["password_confirmation"], "doesn't match")
	}
	return errs
}

// Save makes the admin, using the code up in the same write, so a failed
// save leaves it good and a second setup at the same time can't make a
// second admin. The admin's id, or web.Invalid.
func (a Admin) Save(ctx context.Context, d *db.DB, now time.Time) (int64, error) {
	errs := a.check()
	id, err := matchingSetupCode(ctx, New(d.Read), a.Code)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		errs["code"] = append(errs["code"], "doesn't match the one the installer printed")
	}
	if len(errs) > 0 {
		return 0, errs
	}
	digest, err := auth.Hash(a.Password)
	if err != nil {
		return 0, err
	}
	var user int64
	err = d.Tx(ctx, func(tx *db.Tx) error {
		q := New(tx)
		switch used, err := q.UseSetupCode(ctx, id); {
		case err != nil:
			return err
		case used != 1:
			return web.Invalid{"base": {"Setup was already completed. Sign in instead."}}
		}
		user, err = q.AddUser(ctx, AddUserParams{EmailAddress: auth.Normalize(a.EmailAddress), PasswordDigest: digest, CreatedAt: now, UpdatedAt: now})
		return err
	})
	return user, err
}

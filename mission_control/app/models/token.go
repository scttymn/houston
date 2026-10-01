package models

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// TokenPrefix begins every personal token.
const TokenPrefix = "hou_"

// IssueToken makes a named personal token: the token itself, shown once,
// and its row (only its digest is kept). A bad name is Refused, in words.
func IssueToken(ctx context.Context, q *Queries, name string) (string, ApiToken, error) {
	name = strings.TrimSpace(name)
	var problems []string
	switch {
	case name == "":
		problems = append(problems, "Name can't be blank")
	case utf8.RuneCountInString(name) > 50:
		problems = append(problems, "Name is too long (maximum is 50 characters)")
	}
	if name != "" {
		taken, err := q.TokenNameTaken(ctx, name)
		if err != nil {
			return "", ApiToken{}, err
		}
		if taken {
			problems = append(problems, "Name has already been taken")
		}
	}
	if problems != nil {
		return "", ApiToken{}, Refused{sentence(problems)}
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	row, err := q.CreateToken(ctx, CreateTokenParams{Name: name, TokenDigest: Digest(token)})
	return token, row, err
}

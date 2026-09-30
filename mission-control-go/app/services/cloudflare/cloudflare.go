// Package cloudflare is a small client for Cloudflare's v4 API: JSON in,
// the result out, and an Error with Cloudflare's own words when it says no.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// API is Cloudflare's v4 API.
const API = "https://api.cloudflare.com/client/v4"

// Error is Cloudflare saying no (Status its HTTP status), or not answering
// (Status 0).
type Error struct {
	Msg    string
	Status int
}

func (e *Error) Error() string { return e.Msg }

// Client calls the API with an API token.
type Client struct {
	Token string
	// Base is the API's address: API, or a test's fake.
	Base string
	// HTTP is the client that calls it; nil: one with a 10 s timeout.
	HTTP *http.Client
}

var defaultHTTP = &http.Client{Timeout: 10 * time.Second}

// Get is GET path with query, its result decoded into into.
func (c Client) Get(ctx context.Context, path string, query url.Values, into any) error {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return c.do(ctx, "GET", path, nil, into)
}

// Post, Put, Patch and Delete send body as JSON; into may be nil.
func (c Client) Post(ctx context.Context, path string, body, into any) error {
	return c.do(ctx, "POST", path, body, into)
}

func (c Client) Put(ctx context.Context, path string, body, into any) error {
	return c.do(ctx, "PUT", path, body, into)
}

func (c Client) Patch(ctx context.Context, path string, body, into any) error {
	return c.do(ctx, "PATCH", path, body, into)
}

func (c Client) Delete(ctx context.Context, path string) error {
	return c.do(ctx, "DELETE", path, nil, nil)
}

func (c Client) do(ctx context.Context, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	base := c.Base
	if base == "" {
		base = API
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = defaultHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return &Error{Msg: fmt.Sprintf("couldn't reach Cloudflare (%s)", reachError(err)), Status: 0}
	}
	defer resp.Body.Close()
	var answer struct {
		Success bool                       `json:"success"`
		Errors  []struct{ Message string } `json:"errors"`
		Result  json.RawMessage            `json:"result"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	json.Unmarshal(data, &answer) // not JSON: no success
	if resp.StatusCode/100 != 2 || !answer.Success {
		var messages []string
		for _, e := range answer.Errors {
			if e.Message != "" {
				messages = append(messages, e.Message)
			}
		}
		msg := strings.Join(messages, "; ")
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return &Error{Msg: msg, Status: resp.StatusCode}
	}
	if into == nil || len(answer.Result) == 0 {
		return nil
	}
	return json.Unmarshal(answer.Result, into)
}

// reachError names why Cloudflare didn't answer, briefly.
func reachError(err error) string {
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timed out"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err.Error()
	}
	return err.Error()
}

// Managed is the start of the comment on every DNS record Houston made; it
// never changes a record without it.
const Managed = "managed-by:houston"

// Record is a DNS record.
type Record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

// IsManaged is whether Houston made it.
func (r Record) IsManaged() bool { return strings.HasPrefix(r.Comment, Managed) }

// Records are the DNS records of one zone.
type Records struct {
	Client Client
	Zone   string
}

// Find is name's record, or nil.
func (r Records) Find(ctx context.Context, name string) (*Record, error) {
	var found []Record
	if err := r.Client.Get(ctx, "/zones/"+r.Zone+"/dns_records", url.Values{"name": {name}}, &found); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// Point makes name a proxied CNAME to the tunnel, commented comment: it
// updates existing (one of Houston's, the caller has checked) or makes it.
func (r Records) Point(ctx context.Context, name, tunnelID string, existing *Record, comment string) error {
	record := Record{Type: "CNAME", Name: name, Content: tunnelID + ".cfargotunnel.com", Proxied: true, Comment: comment}
	if existing != nil {
		return r.Client.Patch(ctx, "/zones/"+r.Zone+"/dns_records/"+existing.ID, record, nil)
	}
	return r.Client.Post(ctx, "/zones/"+r.Zone+"/dns_records", record, nil)
}

// Delete removes record.
func (r Records) Delete(ctx context.Context, record Record) error {
	return r.Client.Delete(ctx, "/zones/"+r.Zone+"/dns_records/"+record.ID)
}

// Zone is a Cloudflare zone: its status is "active" once its nameservers
// are Cloudflare's.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// FindZone is the zone named name, or nil.
func (c Client) FindZone(ctx context.Context, name string) (*Zone, error) {
	var found []Zone
	if err := c.Get(ctx, "/zones", url.Values{"name": {name}}, &found); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// ZoneOf is the zone holding domain: the domain itself or its nearest
// parent, never a TLD alone; nil when the token sees none.
func (c Client) ZoneOf(ctx context.Context, domain string) (*Zone, error) {
	labels := strings.Split(domain, ".")
	for i := 0; i < len(labels)-1; i++ {
		zone, err := c.FindZone(ctx, strings.Join(labels[i:], "."))
		if err != nil || zone != nil {
			return zone, err
		}
	}
	return nil, nil
}

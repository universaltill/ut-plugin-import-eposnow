// Package eposnow is a read-only client for the Epos Now V4 REST API
// (ut-docs#4430): Basic auth, 1-based `?page=N` lists returning bare
// arrays, bounded retries. Transport is a Doer, so tests fake it and the
// plugin passes StreamDoer (http:stream, no 256 KiB body cap).
//
// Errors never carry a response body or a credential: an Epos Now error
// body can echo the request, and the message reaches the operator.
package eposnow

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// BaseURL is the V4 API root.
const BaseURL = "https://api.eposnowhq.com/api/v4/"

// MaxPages bounds one list walk: an API that ignored ?page= would
// otherwise page forever. 500 pages is 100,000 records at the documented
// 200 per page, well past the till's 20,000-row run cap.
const MaxPages = 500

// MaxListRecords bounds what listAll holds in memory across its pages.
const MaxListRecords = 100_000

// pageLimitMiB is the page limit the operator is told: the till's default
// http:stream body limit (limits.http_body_mb, 8 MiB), which this plugin
// does not raise. The host answers ErrQuota one byte past it.
const pageLimitMiB = 8

// MaxPageBytes bounds one page's body. StreamDoer reads one byte past it,
// so it is one byte under the host's limit: that read (8 MiB) never trips
// the host's quota, and a body of 8 MiB or more is refused here, never cut.
// 8 MiB is ~40x a 200-product page.
const MaxPageBytes = pageLimitMiB<<20 - 1

// RetryBudget bounds the total wait on retries of one request, backoff
// and Retry-After together (the run's deadline bounds it further).
const RetryBudget = 30 * time.Second

// retryBackoff is the wait before each retry of a 429/5xx answer.
var retryBackoff = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}

// Doer sends one HTTP request.
type Doer interface {
	Do(plugin.HTTPRequest) (*plugin.HTTPResponse, error)
}

// DoerFunc adapts a function (plugin.HTTP) to Doer.
type DoerFunc func(plugin.HTTPRequest) (*plugin.HTTPResponse, error)

// Do calls f.
func (f DoerFunc) Do(r plugin.HTTPRequest) (*plugin.HTTPResponse, error) { return f(r) }

// ErrAuth: Epos Now refused the credentials (401/403).
var ErrAuth = errors.New("Epos Now did not accept the credentials — check the API key and secret in this plugin's settings")

// ErrPageTooLarge: a page body at or past the page limit (the Doer's own
// bound, or the till's http_read quota).
var ErrPageTooLarge = errors.New("Epos Now page too large")

// ErrSlow: the run's deadline passed (or a retry would cross it) — the
// run stops itself before the till ends the ask.
var ErrSlow = errors.New("Epos Now is slow — the import stopped before the till's time limit; try again later")

// StatusError is an answer the import cannot use (after any retries).
type StatusError struct {
	Resource string
	Status   int
	Retries  int
}

func (e *StatusError) Error() string {
	if e.Retries > 0 {
		n := fmt.Sprintf("%d retries", e.Retries)
		if e.Retries == 1 {
			n = "1 retry"
		}
		return fmt.Sprintf("Epos Now is busy or unavailable (HTTP %d for %s, after %s) — try again later", e.Status, e.Resource, n)
	}
	return fmt.Sprintf("Epos Now answered HTTP %d for %s", e.Status, e.Resource)
}

// Client reads one Epos Now account. Deadline (zero: none) is when the
// run must stop: no request starts, and no retry waits, past it.
type Client struct {
	doer     Doer
	auth     string
	Sleep    func(time.Duration)
	Now      func() time.Time
	Deadline time.Time
}

// New returns a Client authenticating with key and secret (HTTP Basic).
func New(d Doer, key, secret string) *Client {
	return &Client{
		doer:  d,
		auth:  "Basic " + base64.StdEncoding.EncodeToString([]byte(key+":"+secret)),
		Sleep: time.Sleep,
		Now:   time.Now,
	}
}

// get fetches one resource path and decodes its JSON into out.
func (c *Client) get(path, resource string, out any) error {
	// A GET carries no body, so no Content-Type.
	req := plugin.HTTPRequest{
		Method: "GET",
		URL:    BaseURL + path,
		Headers: map[string]string{
			"Authorization": c.auth,
			"Accept":        "application/json",
		},
	}
	var waited time.Duration
	for attempt := 0; ; attempt++ {
		if c.pastDeadline(0) {
			return ErrSlow
		}
		resp, err := c.doer.Do(req)
		if errors.Is(err, ErrPageTooLarge) {
			return tooLarge(resource)
		}
		if err != nil {
			// The till's own refusal or a network failure: not retried
			// (a denied egress never heals within the run).
			return fmt.Errorf("could not reach Epos Now (%s): %w", resource, err)
		}
		switch s := resp.Status; {
		case s == 401 || s == 403:
			return ErrAuth
		case s == 429 || s >= 500:
			if attempt >= len(retryBackoff) || waited >= RetryBudget {
				return &StatusError{Resource: resource, Status: s, Retries: attempt}
			}
			wait := retryBackoff[attempt]
			if ra, ok := retryAfter(resp.Headers); ok && s == 429 {
				wait = ra
			}
			wait = min(wait, RetryBudget-waited)
			if c.pastDeadline(wait) {
				return ErrSlow
			}
			c.Sleep(wait)
			waited += wait
			continue
		case s != 200:
			return &StatusError{Resource: resource, Status: s}
		}
		if len(resp.Body) > MaxPageBytes {
			return tooLarge(resource)
		}
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return fmt.Errorf("Epos Now sent an unexpected %s answer", resource)
		}
		return nil
	}
}

func tooLarge(resource string) error {
	return fmt.Errorf("an Epos Now %s page is larger than the %d MiB page limit", resource, pageLimitMiB)
}

// pastDeadline: would waiting d from now reach the run's deadline?
func (c *Client) pastDeadline(d time.Duration) bool {
	return !c.Deadline.IsZero() && !c.Now().Add(d).Before(c.Deadline)
}

// retryAfter is a 429's Retry-After in whole seconds (the HTTP-date form
// is not used by Epos Now and falls back to the backoff).
func retryAfter(h map[string]string) (time.Duration, bool) {
	for k, v := range h {
		if strings.EqualFold(k, "Retry-After") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return 0, false
			}
			return time.Duration(min(n, 86_400)) * time.Second, true
		}
	}
	return 0, false
}

// walk reads resource page 1, 2, … and hands each non-empty page to fn. It
// stops on an empty page only: the documented page size (200) is not
// relied on, so a short page is not taken as the end.
func walk[T any](c *Client, resource string, fn func([]T) error) error {
	for page := 1; page <= MaxPages; page++ {
		var items []T
		if err := c.get(resource+"?page="+strconv.Itoa(page), resource, &items); err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		if err := fn(items); err != nil {
			return err
		}
	}
	return fmt.Errorf("Epos Now %s has more than %d pages; stopped", resource, MaxPages)
}

// listAll reads every page of resource into memory, bounded by
// MaxListRecords (MaxPages alone would allow any page size).
func listAll[T any](c *Client, resource string) ([]T, error) {
	var all []T
	err := walk(c, resource, func(p []T) error {
		if len(all)+len(p) > MaxListRecords {
			return fmt.Errorf("Epos Now %s has more than 100,000 records; stopped", resource)
		}
		all = append(all, p...)
		return nil
	})
	return all, err
}

// TokenInfo reads the account the credentials belong to — the cheap
// "test connection" call.
func (c *Client) TokenInfo() (TokenInfo, error) {
	var ti TokenInfo
	err := c.get("TokenInfo", "TokenInfo", &ti)
	return ti, err
}

// Locations lists every location.
func (c *Client) Locations() ([]Location, error) { return listAll[Location](c, "Location") }

// Categories lists every category.
func (c *Client) Categories() ([]Category, error) { return listAll[Category](c, "Category") }

// TaxGroups lists every tax group.
func (c *Client) TaxGroups() ([]TaxGroup, error) { return listAll[TaxGroup](c, "TaxGroup") }

// Stock lists the per-product stock records at one location.
func (c *Client) Stock(locationID int64) ([]ProductStock, error) {
	return listAll[ProductStock](c, "ProductStock/Location/"+strconv.FormatInt(locationID, 10))
}

// EachProductPage calls fn with every non-empty page of products, in
// order, so the caller maps and submits without holding the whole catalogue.
func (c *Client) EachProductPage(fn func([]Product) error) error {
	return walk(c, "Product", fn)
}

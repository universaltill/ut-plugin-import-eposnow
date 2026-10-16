//go:build !wasip1

package eposnow

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// fakeAPI answers like the Epos Now V4 API: pages[resource] is the list
// split into pages (page N = pages[N-1], past the end = "[]"). status, when
// set for "<resource>?page=N", is answered instead (consumed one per call).
type fakeAPI struct {
	pages    map[string][]string
	status   map[string][]int
	single   map[string]string
	requests []plugin.HTTPRequest
	err      error
	headers  map[string]string // sent with every status answer
	onDo     func()            // runs on every request (a clock tick)
}

func (f *fakeAPI) Do(r plugin.HTTPRequest) (*plugin.HTTPResponse, error) {
	f.requests = append(f.requests, r)
	if f.onDo != nil {
		f.onDo()
	}
	if f.err != nil {
		return nil, f.err
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		return nil, err
	}
	res := strings.TrimPrefix(u.Path, "/api/v4/")
	key := res
	if p := u.Query().Get("page"); p != "" {
		key += "?page=" + p
	}
	if s := f.status[key]; len(s) > 0 {
		f.status[key] = s[1:]
		return &plugin.HTTPResponse{Status: s[0], Headers: f.headers, Body: []byte(`"Invalid credentials: key 'abc' secret 'xyz'"`)}, nil
	}
	if b, ok := f.single[res]; ok {
		return &plugin.HTTPResponse{Status: 200, Body: []byte(b)}, nil
	}
	pages, ok := f.pages[res]
	if !ok {
		return &plugin.HTTPResponse{Status: 404, Body: []byte(`"not found"`)}, nil
	}
	n, _ := strconv.Atoi(u.Query().Get("page"))
	if n < 1 || n > len(pages) {
		return &plugin.HTTPResponse{Status: 200, Body: []byte(`[]`)}, nil
	}
	return &plugin.HTTPResponse{Status: 200, Body: []byte(pages[n-1])}, nil
}

func newTestClient(f *fakeAPI) (*Client, *[]time.Duration) {
	c := New(f, "the-key", "the-secret")
	var slept []time.Duration
	c.Sleep = func(d time.Duration) { slept = append(slept, d) }
	return c, &slept
}

func TestRequestsCarryBasicAuthAndJSON(t *testing.T) {
	f := &fakeAPI{single: map[string]string{"TokenInfo": `{"CompanyName":"Corner Café","AppLocationId":3,"AppLocationName":"High St"}`}}
	c, _ := newTestClient(f)
	ti, err := c.TokenInfo()
	if err != nil {
		t.Fatal(err)
	}
	if Str(ti.CompanyName) != "Corner Café" {
		t.Fatalf("token info = %+v", ti)
	}
	if len(f.requests) != 1 {
		t.Fatalf("%d requests", len(f.requests))
	}
	r := f.requests[0]
	if r.URL != BaseURL+"TokenInfo" {
		t.Errorf("url = %q", r.URL)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("the-key:the-secret"))
	if r.Headers["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", r.Headers["Authorization"], want)
	}
	// A GET has no body, so no Content-Type.
	if r.Headers["Accept"] != "application/json" || len(r.Headers) != 2 {
		t.Errorf("headers = %v", r.Headers)
	}
	if r.Method != "GET" {
		t.Errorf("method = %q", r.Method)
	}
}

// A list is read page 1, 2, … until an EMPTY page: a short page is not
// the end, because the page size (200 in the docs) is not guaranteed.
func TestListPagesUntilEmpty(t *testing.T) {
	f := &fakeAPI{pages: map[string][]string{"Category": {
		`[{"Id":1,"ParentId":null,"Name":"Drinks"},{"Id":2,"ParentId":1,"Name":"Hot"}]`,
		`[{"Id":3,"ParentId":2,"Name":"Coffee"}]`,
	}}}
	c, _ := newTestClient(f)
	cats, err := c.Categories()
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 3 || Str(cats[2].Name) != "Coffee" {
		t.Fatalf("categories = %+v", cats)
	}
	var got []string
	for _, r := range f.requests {
		got = append(got, strings.TrimPrefix(r.URL, BaseURL))
	}
	if strings.Join(got, ",") != "Category?page=1,Category?page=2,Category?page=3" {
		t.Fatalf("requests = %v", got)
	}
}

func TestProductPagesStreamInOrder(t *testing.T) {
	f := &fakeAPI{pages: map[string][]string{"Product": {
		`[{"Id":10,"Name":"A"},{"Id":11,"Name":"B"}]`,
		`[{"Id":12,"Name":"C"}]`,
	}}}
	c, _ := newTestClient(f)
	var sizes []int
	if err := c.EachProductPage(func(p []Product) error { sizes = append(sizes, len(p)); return nil }); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != "[2 1]" {
		t.Fatalf("pages = %v", sizes)
	}
	stop := errors.New("stop")
	if err := c.EachProductPage(func([]Product) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("fn error = %v", err)
	}
}

// An API that ignores ?page= would page forever: the walk is bounded.
func TestListIsBoundedWhenPagingNeverEnds(t *testing.T) {
	f := &fakeAPI{}
	f.pages = map[string][]string{}
	page := `[{"Id":1,"Name":"Same"}]`
	many := make([]string, MaxPages+5)
	for i := range many {
		many[i] = page
	}
	f.pages["TaxGroup"] = many
	c, _ := newTestClient(f)
	_, err := c.TaxGroups()
	if err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("err = %v, want a too-many-pages error", err)
	}
	if len(f.requests) != MaxPages {
		t.Fatalf("%d requests, want %d", len(f.requests), MaxPages)
	}
}

func TestStockAndLocationPaths(t *testing.T) {
	f := &fakeAPI{pages: map[string][]string{
		"ProductStock/Location/7": {`[{"ProductId":10,"LocationId":7,"ProductStockBatches":[{"CurrentStock":4},{"CurrentStock":3}]}]`},
		"Location":                {`[{"Id":7,"Name":"High St"}]`},
	}}
	c, _ := newTestClient(f)
	st, err := c.Stock(7)
	if err != nil || len(st) != 1 || len(st[0].ProductStockBatches) != 2 {
		t.Fatalf("stock = %+v, %v", st, err)
	}
	locs, err := c.Locations()
	if err != nil || len(locs) != 1 || *locs[0].ID != 7 {
		t.Fatalf("locations = %+v, %v", locs, err)
	}
}

// 401/403 → ErrAuth, with no retry, and the body (it may echo the key)
// never reaches the error.
func TestAuthErrors(t *testing.T) {
	for _, code := range []int{401, 403} {
		f := &fakeAPI{status: map[string][]int{"Category?page=1": {code}}, pages: map[string][]string{"Category": {`[]`}}}
		c, slept := newTestClient(f)
		_, err := c.Categories()
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("%d: err = %v, want ErrAuth", code, err)
		}
		if !strings.Contains(err.Error(), "check the API key and secret") {
			t.Errorf("%d: message %q", code, err)
		}
		if strings.Contains(err.Error(), "abc") || strings.Contains(err.Error(), "xyz") {
			t.Errorf("%d: body leaked into %q", code, err)
		}
		if len(f.requests) != 1 || len(*slept) != 0 {
			t.Errorf("%d: %d requests, %d sleeps — auth errors are not retried", code, len(f.requests), len(*slept))
		}
	}
}

// 429 and 5xx retry three times with growing backoff, then succeed…
func TestRetriesThenSucceeds(t *testing.T) {
	f := &fakeAPI{
		status: map[string][]int{"Category?page=1": {429, 503, 500}},
		pages:  map[string][]string{"Category": {`[{"Id":1,"Name":"X"}]`}},
	}
	c, slept := newTestClient(f)
	cats, err := c.Categories()
	if err != nil || len(cats) != 1 {
		t.Fatalf("categories = %+v, %v", cats, err)
	}
	if fmt.Sprint(*slept) != "[1s 2s 4s]" {
		t.Fatalf("backoff = %v", *slept)
	}
}

// …or fail after the third retry, naming the status, never the body.
func TestRetriesExhausted(t *testing.T) {
	f := &fakeAPI{status: map[string][]int{"Product?page=1": {502, 502, 502, 502, 200}}, pages: map[string][]string{"Product": {`[]`}}}
	c, slept := newTestClient(f)
	called := false
	err := c.EachProductPage(func([]Product) error { called = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "abc") {
		t.Errorf("body leaked: %v", err)
	}
	if len(f.requests) != 4 || len(*slept) != 3 || called {
		t.Fatalf("%d requests, %d sleeps, fn called %v", len(f.requests), len(*slept), called)
	}
}

// A failed later page fails the walk; pages already handed over stay the
// caller's to discard (the plugin then answers ok:false, so core applies
// nothing).
func TestLaterPageFailure(t *testing.T) {
	f := &fakeAPI{
		status: map[string][]int{"Product?page=2": {404}},
		pages:  map[string][]string{"Product": {`[{"Id":1,"Name":"A"}]`, `[{"Id":2,"Name":"B"}]`}},
	}
	c, _ := newTestClient(f)
	n := 0
	err := c.EachProductPage(func(p []Product) error { n += len(p); return nil })
	if err == nil || !strings.Contains(err.Error(), "404") || n != 1 {
		t.Fatalf("err = %v, rows = %d", err, n)
	}
}

func TestTransportErrorIsNotRetried(t *testing.T) {
	f := &fakeAPI{err: plugin.ErrDenied}
	c, slept := newTestClient(f)
	_, err := c.Locations()
	if err == nil || !strings.Contains(err.Error(), "could not reach Epos Now") {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != 1 || len(*slept) != 0 {
		t.Fatalf("%d requests, %d sleeps", len(f.requests), len(*slept))
	}
}

func TestMalformedAndOversizedBodies(t *testing.T) {
	f := &fakeAPI{pages: map[string][]string{"Location": {`{"not":"an array"}`}}}
	c, _ := newTestClient(f)
	if _, err := c.Locations(); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("malformed: %v", err)
	}
	// A page past the plugin's own bound is refused, never half-parsed.
	big := "[" + strings.Repeat(`{"Id":1,"Name":"x"},`, MaxPageBytes/18) + `{"Id":1}]`
	f = &fakeAPI{pages: map[string][]string{"Location": {big}}}
	c, _ = newTestClient(f)
	if _, err := c.Locations(); err == nil || !strings.Contains(err.Error(), "MiB page limit") {
		t.Fatalf("oversized: %v", err)
	}
}

// The till's http_read answers ErrQuota one byte past its body limit
// (limits.http_body_mb, default 8 MiB): that is the same oversized page,
// with the same message, not a "could not reach" error.
func TestHostBodyQuotaIsAnOversizedPage(t *testing.T) {
	if MaxPageBytes+1 > 8<<20 {
		t.Fatalf("MaxPageBytes+1 = %d reads past the host's default 8 MiB stream limit", MaxPageBytes+1)
	}
	quota := &plugin.Error{Op: "http_read", Code: plugin.ErrQuota.Code}
	_, err := readBody(io.MultiReader(strings.NewReader("[{"), errReader{quota}))
	if !errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("readBody on ErrQuota = %v, want ErrPageTooLarge", err)
	}
	other := errors.New("boom")
	if _, err := readBody(errReader{other}); !errors.Is(err, other) || errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("readBody on another error = %v", err)
	}
	f := &fakeAPI{err: ErrPageTooLarge}
	c, slept := newTestClient(f)
	_, err = c.Locations()
	if err == nil || err.Error() != "an Epos Now Location page is larger than the 8 MiB page limit" {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != 1 || len(*slept) != 0 {
		t.Fatalf("%d requests, %d sleeps: an oversized page is not retried", len(f.requests), len(*slept))
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// clock is a fake time source; Sleep advances it.
type clock struct{ t time.Time }

func (c *clock) now() time.Time              { return c.t }
func (c *clock) sleep(d time.Duration)       { c.t = c.t.Add(d) }
func (c *clock) tick(d time.Duration) func() { return func() { c.t = c.t.Add(d) } }

func clockedClient(f *fakeAPI, deadlineIn time.Duration) (*Client, *clock, *[]time.Duration) {
	ck := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	c := New(f, "k", "s")
	var slept []time.Duration
	c.Now = ck.now
	c.Sleep = func(d time.Duration) { slept = append(slept, d); ck.sleep(d) }
	c.Deadline = ck.t.Add(deadlineIn)
	return c, ck, &slept
}

// Past the run's deadline no further request is made: the run fails with
// a clear "slow" error before the till kills the ask.
func TestDeadlineStopsTheWalk(t *testing.T) {
	pages := make([]string, 10)
	for i := range pages {
		pages[i] = `[{"Id":1,"Name":"A"}]`
	}
	f := &fakeAPI{pages: map[string][]string{"Product": pages}}
	c, ck, _ := clockedClient(f, 4*time.Minute)
	f.onDo = ck.tick(61 * time.Second)
	n := 0
	err := c.EachProductPage(func(p []Product) error { n++; return nil })
	if !errors.Is(err, ErrSlow) || !strings.Contains(err.Error(), "Epos Now is slow") {
		t.Fatalf("err = %v", err)
	}
	// Requests at 0, 61, 122, 183 s; the fifth would start at 244 s > 240.
	if len(f.requests) != 4 || n != 4 {
		t.Fatalf("%d requests, %d pages handed over", len(f.requests), n)
	}
}

// A retry whose wait would cross the deadline is not made.
func TestDeadlineStopsRetrying(t *testing.T) {
	f := &fakeAPI{status: map[string][]int{"Category?page=1": {503, 503, 503}}, pages: map[string][]string{"Category": {`[]`}}}
	c, _, slept := clockedClient(f, 2500*time.Millisecond)
	_, err := c.Categories()
	if !errors.Is(err, ErrSlow) {
		t.Fatalf("err = %v, want ErrSlow", err)
	}
	// 1 s backoff fits, the 2 s one would end at 3 s > 2.5 s.
	if fmt.Sprint(*slept) != "[1s]" || len(f.requests) != 2 {
		t.Fatalf("slept %v, %d requests", *slept, len(f.requests))
	}
}

// 429 with Retry-After (seconds) waits that long instead of the backoff,
// within the per-request retry budget.
func TestRetryAfterIsHonoured(t *testing.T) {
	f := &fakeAPI{
		status:  map[string][]int{"Category?page=1": {429, 429}},
		headers: map[string]string{"retry-after": "5"},
		pages:   map[string][]string{"Category": {`[{"Id":1,"Name":"X"}]`}},
	}
	c, _, slept := clockedClient(f, 4*time.Minute)
	if _, err := c.Categories(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(*slept) != "[5s 5s]" {
		t.Fatalf("slept %v, want [5s 5s]", *slept)
	}
	// A huge Retry-After is capped by the retry budget; once the budget
	// is spent the request fails instead of retrying again.
	f = &fakeAPI{
		status:  map[string][]int{"Category?page=1": {429, 503}},
		headers: map[string]string{"Retry-After": "3600"},
		pages:   map[string][]string{"Category": {`[]`}},
	}
	c, _, slept = clockedClient(f, 4*time.Minute)
	_, err := c.Categories()
	if err == nil || !strings.Contains(err.Error(), "HTTP 503 for Category, after 1 retry)") {
		t.Fatalf("err = %v", err)
	}
	if fmt.Sprint(*slept) != fmt.Sprint([]time.Duration{RetryBudget}) || len(f.requests) != 2 {
		t.Fatalf("slept %v, %d requests", *slept, len(f.requests))
	}
	// Garbage Retry-After falls back to the backoff.
	f = &fakeAPI{
		status:  map[string][]int{"Category?page=1": {429}},
		headers: map[string]string{"Retry-After": "Wed, 21 Oct 2026 07:28:00 GMT"},
		pages:   map[string][]string{"Category": {`[]`}},
	}
	c, _, slept = clockedClient(f, 4*time.Minute)
	if _, err := c.Categories(); err != nil || fmt.Sprint(*slept) != "[1s]" {
		t.Fatalf("err=%v slept %v", err, *slept)
	}
}

// listAll holds every record in memory: it stops past MaxListRecords.
func TestListAllIsBoundedByRecords(t *testing.T) {
	page := "[" + strings.Repeat(`{"Id":1},`, 999) + `{"Id":1}]`
	pages := make([]string, MaxListRecords/1000+5)
	for i := range pages {
		pages[i] = page
	}
	f := &fakeAPI{pages: map[string][]string{"Category": pages}}
	c, _ := newTestClient(f)
	_, err := c.Categories()
	if err == nil || !strings.Contains(err.Error(), "more than 100,000") {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != MaxListRecords/1000+1 {
		t.Fatalf("%d requests", len(f.requests))
	}
}

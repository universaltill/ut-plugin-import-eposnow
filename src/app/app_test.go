//go:build !wasip1

package app

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// api is a spec-shaped fake of the Epos Now V4 API behind the FakeHost's
// http_request hook. Lists are split into pages of pageSize; past the end
// is "[]". fail["<path>?page=N"] answers that status instead.
type api struct {
	pageSize int
	lists    map[string][]string // resource → JSON objects
	single   map[string]string
	fail     map[string]int
	urls     []string
}

func (a *api) serve(r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
	a.urls = append(a.urls, r.URL)
	u, err := url.Parse(r.URL)
	if err != nil || u.Host != "api.eposnowhq.com" || !strings.HasPrefix(u.Path, "/api/v4/") {
		return plugin.HTTPResponse{Status: 404}, nil
	}
	res := strings.TrimPrefix(u.Path, "/api/v4/")
	if s, ok := a.fail[res+"?"+u.RawQuery]; ok {
		return plugin.HTTPResponse{Status: s, Body: []byte(`"bad"`)}, nil
	}
	if b, ok := a.single[res]; ok {
		return plugin.HTTPResponse{Status: 200, Body: []byte(b)}, nil
	}
	items, ok := a.lists[res]
	if !ok {
		return plugin.HTTPResponse{Status: 404, Body: []byte(`"no such resource"`)}, nil
	}
	n, _ := strconv.Atoi(u.Query().Get("page"))
	size := a.pageSize
	if size == 0 {
		size = 200
	}
	lo, hi := (n-1)*size, n*size
	if n < 1 || lo >= len(items) {
		return plugin.HTTPResponse{Status: 200, Body: []byte(`[]`)}, nil
	}
	hi = min(hi, len(items))
	return plugin.HTTPResponse{Status: 200, Body: []byte("[" + strings.Join(items[lo:hi], ",") + "]")}, nil
}

// shopAPI is a small account: two locations, a category tree, a 20% VAT
// group, stock at location 7, and nProducts products.
func shopAPI(nProducts int) *api {
	a := &api{
		single: map[string]string{"TokenInfo": `{"CompanyGuid":"7d0f8b4e-0000-4000-8000-000000000001","CompanyName":"Corner Café","AppId":1,"AppLocationId":7,"AppLocationName":"High St"}`},
		lists: map[string][]string{
			"Location":                {`{"Id":7,"Name":"High St","Description":null,"Address1":"1 High St","Town":"Leeds","PostCode":"LS1 1AA","LocationAreaId":1}`, `{"Id":8,"Name":"Market","LocationAreaId":1}`},
			"Category":                {`{"Id":1,"ParentId":null,"RootParentId":null,"Name":"Drinks","ShowOnTill":true}`, `{"Id":2,"ParentId":1,"RootParentId":1,"Name":"Hot","ShowOnTill":true}`},
			"TaxGroup":                {`{"Id":5,"Name":"Standard","TaxRates":[{"TaxGroupId":5,"TaxRateId":1,"LocationId":null,"Priority":1,"Percentage":20.0,"Name":"VAT","Description":"","TaxCode":"S"}]}`},
			"ProductStock/Location/7": {`{"Id":1,"ProductId":1,"LocationId":7,"MinStock":0,"MaxStock":0,"ProductStockBatches":[{"Id":1,"ProductStockId":1,"CurrentStock":6}]}`},
			"ProductStock/Location/8": {`{"Id":2,"ProductId":1,"LocationId":8,"ProductStockBatches":[{"Id":2,"ProductStockId":2,"CurrentStock":40}]}`},
		},
	}
	for i := 1; i <= nProducts; i++ {
		inc := i != 2 // product 2 has a net price
		a.lists["Product"] = append(a.lists["Product"], fmt.Sprintf(
			`{"Id":%d,"Name":"Item %d","SalePrice":%.2f,"IsSalePriceIncTax":%v,"CategoryId":2,"Barcode":"50000%05d","SalePriceTaxGroupId":5,"Sku":"SKU-%d","SellOnTill":true,"IsArchived":false,"VariantGroupId":null}`,
			i, i, 1.5, inc, i, i))
	}
	return a
}

func newHost(t *testing.T, a *api, settings map[string]string) *plugin.FakeHost {
	t.Helper()
	h := plugin.NewFakeHost()
	h.PluginID = PluginID
	h.SecretSettings["api_key"] = true
	h.SecretSettings["api_secret"] = true
	h.Settings["api_key"] = "KEY-s3cr3t-k"
	h.Settings["api_secret"] = "SECRET-s3cr3t-s"
	for k, v := range settings {
		if v == "" {
			delete(h.Settings, k)
		} else {
			h.Settings[k] = v
		}
	}
	h.ImportEntities = map[string]bool{"items": true}
	if a != nil {
		h.HTTP = a.serve
	}
	plugin.UseFakeHost(t, h)
	old := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = old })
	return h
}

type answer struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message"`
	Error   string         `json:"error"`
	Counts  map[string]int `json:"counts"`
}

func ask(t *testing.T, payload map[string]any) answer {
	t.Helper()
	p, _ := json.Marshal(payload)
	raw, _ := json.Marshal(map[string]any{"id": "e1", "type": "import.requested.ask", "timestamp": "2026-10-15T12:00:00Z", "payload": json.RawMessage(p)})
	out, err := plugin.Dispatch(Handlers(), raw, "")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var a answer
	if err := json.Unmarshal(out, &a); err != nil {
		t.Fatalf("answer %s: %v", out, err)
	}
	return a
}

func payload(mode string, entities ...string) map[string]any {
	return map[string]any{"entry_key": "eposnow", "entities": entities, "mode": mode, "source": "api"}
}

func rows(t *testing.T, h *plugin.FakeHost) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range h.ImportRows["items"] {
		var m map[string]any
		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// noSecretsOrRows: credentials and row contents never reach the log or
// the answer.
func noSecretsOrRows(t *testing.T, h *plugin.FakeHost, a answer) {
	t.Helper()
	all := strings.Join(h.Logs, "\n") + "\n" + a.Message + "\n" + a.Error
	for _, s := range []string{"s3cr3t", "Item 1", "SKU-", "50000"} {
		if strings.Contains(all, s) {
			t.Errorf("%q leaked into log/answer: %s", s, all)
		}
	}
}

func TestCommitSubmitsMappedRowsInBatches(t *testing.T) {
	a := shopAPI(1203)
	a.pageSize = 200
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items", "stock"))
	if !got.OK {
		t.Fatalf("answer = %+v", got)
	}
	rs := rows(t, h)
	if len(rs) != 1203 {
		t.Fatalf("%d rows submitted", len(rs))
	}
	// ≤500 per call: 500 + 500 + 203.
	if n := h.Calls["import_rows_submit"]; n != 3 {
		t.Fatalf("%d submit calls, want 3", n)
	}
	first, _ := json.Marshal(rs[0])
	want := `{"active":true,"barcodes":["5000000001"],"category_path":["Drinks","Hot"],"external_id":"1","name":"Item 1","price_minor":150,"sku":"SKU-1","stock_qty":6,"vat_rate_bp":2000}`
	if string(first) != want {
		t.Fatalf("row 1\n got %s\nwant %s", first, want)
	}
	if rs[1]["price_minor"] != float64(180) { // 1.50 net + 20% VAT
		t.Errorf("net price row: %v", rs[1]["price_minor"])
	}
	if _, ok := rs[2]["stock_qty"]; ok {
		t.Error("a product without a stock record gets no stock_qty")
	}
	if got.Counts["items"] != 1203 {
		t.Errorf("counts = %v", got.Counts)
	}
	for _, s := range []string{"Corner Café", "High St", "1203 products", "1 net price grossed up"} {
		if !strings.Contains(got.Message, s) {
			t.Errorf("message %q lacks %q", got.Message, s)
		}
	}
	// Every list paged to its empty page; stock only at the location used.
	for _, u := range []string{"Product?page=8", "Category?page=2", "TaxGroup?page=2", "Location?page=2", "ProductStock/Location/7?page=2", "TokenInfo"} {
		if !contains(a.urls, "https://api.eposnowhq.com/api/v4/"+u) {
			t.Errorf("no request for %s", u)
		}
	}
	if contains(a.urls, "https://api.eposnowhq.com/api/v4/Product?page=9") {
		t.Error("paged past the empty page")
	}
	noSecretsOrRows(t, h, got)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Preview is also the connection test: it names the account from
// TokenInfo, and the till plans the rows without writing.
func TestPreviewNamesTheAccount(t *testing.T) {
	h := newHost(t, shopAPI(3), nil)
	got := ask(t, payload("preview", "items"))
	if !got.OK || !strings.Contains(got.Message, `Connected to Epos Now account "Corner Café"`) {
		t.Fatalf("answer = %+v", got)
	}
	if len(rows(t, h)) != 3 {
		t.Fatalf("preview must hand the rows to the till to plan")
	}
}

func TestStockOnlyWhenTheRunIncludesIt(t *testing.T) {
	a := shopAPI(2)
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items"))
	if !got.OK {
		t.Fatalf("%+v", got)
	}
	for _, u := range a.urls {
		if strings.Contains(u, "ProductStock") {
			t.Fatalf("stock fetched without the stock entity: %s", u)
		}
	}
	for _, r := range rows(t, h) {
		if _, ok := r["stock_qty"]; ok {
			t.Fatalf("stock_qty without the stock entity: %v", r)
		}
	}
	if !strings.Contains(got.Message, "stock not imported") {
		t.Errorf("message %q", got.Message)
	}
}

func TestLocationSetting(t *testing.T) {
	a := shopAPI(1)
	h := newHost(t, a, map[string]string{"location_id": " 8 "})
	got := ask(t, payload("commit", "items", "stock"))
	if !got.OK || !strings.Contains(got.Message, "Market") {
		t.Fatalf("%+v", got)
	}
	if rs := rows(t, h); rs[0]["stock_qty"] != float64(40) {
		t.Fatalf("stock from location 8: %v", rs[0]["stock_qty"])
	}

	for _, bad := range []string{"99", "abc", "-1"} {
		h = newHost(t, shopAPI(1), map[string]string{"location_id": bad})
		got = ask(t, payload("commit", "items", "stock"))
		if got.OK || !strings.Contains(got.Error, "location") || len(rows(t, h)) != 0 {
			t.Errorf("location_id %q: %+v", bad, got)
		}
	}
	a = shopAPI(1)
	a.lists["Location"] = nil
	h = newHost(t, a, nil)
	if got = ask(t, payload("commit", "items")); got.OK || !strings.Contains(got.Error, "no locations") {
		t.Errorf("no locations: %+v", got)
	}
}

func TestBadCredentials(t *testing.T) {
	for _, code := range []int{401, 403} {
		a := shopAPI(2)
		a.fail = map[string]int{"TokenInfo?": code}
		h := newHost(t, a, nil)
		got := ask(t, payload("commit", "items"))
		if got.OK || !strings.Contains(got.Error, "check the API key and secret") {
			t.Fatalf("%d: %+v", code, got)
		}
		if len(a.urls) != 1 || len(rows(t, h)) != 0 {
			t.Fatalf("%d: %d requests, %d rows after an auth failure", code, len(a.urls), len(rows(t, h)))
		}
		noSecretsOrRows(t, h, got)
	}
}

func TestMissingCredentialsMakeNoRequest(t *testing.T) {
	for _, k := range []string{"api_key", "api_secret"} {
		a := shopAPI(1)
		h := newHost(t, a, map[string]string{k: ""})
		got := ask(t, payload("commit", "items"))
		if got.OK || !strings.Contains(got.Error, "API key and secret") || len(a.urls) != 0 || len(rows(t, h)) != 0 {
			t.Errorf("%s unset: %+v, %d requests", k, got, len(a.urls))
		}
	}
}

// A failed product page fails the run: the answer is ok:false, so the
// till applies none of the rows already handed over.
func TestFailedPageFailsTheRun(t *testing.T) {
	a := shopAPI(450)
	a.fail = map[string]int{"Product?page=3": 503}
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items"))
	if got.OK || !strings.Contains(got.Error, "503") || !strings.Contains(got.Error, "try again later") {
		t.Fatalf("%+v", got)
	}
	tries := 0
	for _, u := range a.urls {
		if strings.HasSuffix(u, "Product?page=3") {
			tries++
		}
	}
	if tries != 4 {
		t.Errorf("page 3 tried %d times, want 1 + 3 retries", tries)
	}
	// Pages 1–2 (400 rows) were mapped but never reached a 500-row batch:
	// nothing was handed over before the failure.
	if n := len(rows(t, h)); n != 0 {
		t.Errorf("%d rows submitted, want 0", n)
	}
	noSecretsOrRows(t, h, got)

	// With 300-row pages, the first 500-row batch went over before page 3
	// failed: exactly 500, and the answer is still ok:false (the till
	// applies none of them).
	a = shopAPI(900)
	a.pageSize = 300
	a.fail = map[string]int{"Product?page=3": 503}
	h = newHost(t, a, nil)
	if got = ask(t, payload("commit", "items")); got.OK {
		t.Fatalf("%+v", got)
	}
	if n := len(rows(t, h)); n != 500 {
		t.Errorf("%d rows submitted, want 500", n)
	}
}

func TestEmptyCatalogue(t *testing.T) {
	a := shopAPI(0)
	a.lists["Product"] = []string{} // the account answers "[]" for page 1
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items", "stock"))
	if !got.OK || !strings.Contains(got.Message, "0 products read") || got.Counts["items"] != 0 {
		t.Fatalf("%+v", got)
	}
	if n := h.Calls["import_rows_submit"]; n != 0 {
		t.Fatalf("%d submit calls for an empty catalogue", n)
	}
}

// The till's 20,000-row run cap answers ErrQuota: the run fails with the
// cap named.
func TestRunCapQuota(t *testing.T) {
	newHost(t, shopAPI(3), nil)
	old := submit
	submit = func(string, any) (int, error) {
		return 0, &plugin.Error{Op: "import_rows_submit", Code: plugin.ErrQuota.Code}
	}
	t.Cleanup(func() { submit = old })
	got := ask(t, payload("commit", "items"))
	if got.OK || !strings.Contains(got.Error, "20,000") {
		t.Fatalf("%+v", got)
	}
}

// Fields core would refuse are cleaned, the products still import, and
// the reply message counts each kind.
func TestCleanedFieldsAreCounted(t *testing.T) {
	a := shopAPI(1)
	a.lists["TaxGroup"] = append(a.lists["TaxGroup"], `{"Id":6,"Name":"Bad","TaxRates":[{"LocationId":null,"Priority":1,"Percentage":120}]}`)
	a.lists["Product"] = append(a.lists["Product"],
		`{"Id":2,"Name":"Bad code","SalePrice":1,"Barcode":"50 12","SalePriceTaxGroupId":5}`,
		`{"Id":3,"Name":"Bad code 2","SalePrice":1,"Barcode":"ABC/1","SalePriceTaxGroupId":6}`,
		`{"Id":4,"Name":"`+strings.Repeat("x", 210)+`","SalePrice":1,"Barcode":"ABC/2"}`)
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items"))
	if !got.OK || len(rows(t, h)) != 4 {
		t.Fatalf("%+v, %d rows", got, len(rows(t, h)))
	}
	for _, s := range []string{"3 barcodes dropped (not a valid barcode)", "1 VAT rate dropped (outside 0–100%)", "1 name shortened to the till's limit"} {
		if !strings.Contains(got.Message, s) {
			t.Errorf("message %q lacks %q", got.Message, s)
		}
	}
}

// The run stops itself before the till's 5-minute ask timeout: past ~4
// minutes on its own clock it answers a clear "slow" error.
func TestSlowAPIStopsBeforeTheTillDoes(t *testing.T) {
	a := shopAPI(30)
	a.pageSize = 1
	ck := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	oldNow := now
	now = func() time.Time { return ck }
	t.Cleanup(func() { now = oldNow })
	h := newHost(t, a, nil)
	h.HTTP = func(r plugin.HTTPRequest) (plugin.HTTPResponse, error) {
		ck = ck.Add(20 * time.Second)
		return a.serve(r)
	}
	got := ask(t, payload("commit", "items"))
	if got.OK || !strings.Contains(got.Error, "Epos Now is slow") {
		t.Fatalf("%+v", got)
	}
	// 12 requests fit in 240 s at 20 s each; the 13th is never made.
	if len(a.urls) != 12 {
		t.Errorf("%d requests, want 12", len(a.urls))
	}
}

func TestRefusedRequests(t *testing.T) {
	h := newHost(t, shopAPI(1), nil)
	for name, p := range map[string]map[string]any{
		"other entry": {"entry_key": "csv", "entities": []string{"items"}, "mode": "commit", "source": "api"},
		"file source": {"entry_key": "eposnow", "entities": []string{"items"}, "mode": "commit", "file_handle": 3},
		"bad mode":    {"entry_key": "eposnow", "entities": []string{"items"}, "mode": "apply", "source": "api"},
		"no items":    {"entry_key": "eposnow", "entities": []string{"stock"}, "mode": "commit", "source": "api"},
	} {
		if got := ask(t, p); got.OK || got.Error == "" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if len(rows(t, h)) != 0 {
		t.Error("a refused request submitted rows")
	}
}

func TestTillRefusesTheRows(t *testing.T) {
	h := newHost(t, shopAPI(2), nil)
	h.ImportEntities = map[string]bool{"customers": true} // items not resolved
	got := ask(t, payload("commit", "items"))
	if got.OK || !strings.Contains(got.Error, "items:write") {
		t.Fatalf("%+v", got)
	}
}

func TestProductsWithoutIDAreSkippedAndCounted(t *testing.T) {
	a := shopAPI(2)
	a.lists["Product"] = append(a.lists["Product"], `{"Id":null,"Name":"Ghost","SalePrice":1}`)
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items"))
	if !got.OK || len(rows(t, h)) != 2 || !strings.Contains(got.Message, "1 skipped") {
		t.Fatalf("%+v, %d rows", got, len(rows(t, h)))
	}
}

// http_request bodies stop at 256 KiB on the till, and a page of 200 Epos
// Now products with their nested objects can be bigger: every request goes
// over an http:stream instead, bounded by the plugin's own page limit.
func TestRequestsStreamSoLargePagesImport(t *testing.T) {
	a := shopAPI(200)
	a.pageSize = 200
	pad := strings.Repeat("x", 2000) // ~400 KiB page
	for i, p := range a.lists["Product"] {
		a.lists["Product"][i] = strings.Replace(p, `"VariantGroupId":null`, `"VariantGroupId":null,"Description":"`+pad+`"`, 1)
	}
	h := newHost(t, a, nil)
	got := ask(t, payload("commit", "items", "stock"))
	if !got.OK {
		t.Fatalf("answer = %+v", got)
	}
	if n := len(rows(t, h)); n != 200 {
		t.Fatalf("%d rows submitted, want 200", n)
	}
	if h.Calls["http_request"] != 0 || h.Calls["http_open"] == 0 {
		t.Fatalf("http_request=%d http_open=%d, want every request streamed", h.Calls["http_request"], h.Calls["http_open"])
	}
}

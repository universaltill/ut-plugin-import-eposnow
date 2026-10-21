// Package app answers the till's import.requested.ask for the Epos Now
// API-source entry (ADR-0161, ut-docs#4430): read the account over the V4
// API, map each product to an ADR-0051 item row, and hand the rows to the
// till with import_rows_submit. The till plans (preview) or applies
// (commit) them only after this plugin answers ok.
//
// Nothing here logs: credentials and row contents never reach the log, and
// the answer carries counts and the account/location names only.
package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"

	"github.com/universaltill/ut-plugin-import-eposnow/src/eposnow"
)

// PluginID is manifest.json's id.
const PluginID = "com.universaltill.import-eposnow"

// EntryKey is the manifest import entry's key.
const EntryKey = "eposnow"

// runDeadline is how long a run may take before it stops itself: the
// till ends an import ask at 5 minutes (wasm_runtime importTimeout), and
// a clear "Epos Now is slow" beats a killed ask.
const runDeadline = 4 * time.Minute

// batchSize is rows per import_rows_submit call (the till's per-call cap).
const batchSize = plugin.ImportRowsMaxPerCall

// sleep is the retry backoff's clock (tests replace it).
var sleep = time.Sleep

// now is the run's clock (tests replace it).
var now = time.Now

// submit hands a batch to the till (tests replace it).
var submit = plugin.ImportRowsSubmit

// doer is the HTTP transport: an http:stream per request, since a page of
// products can exceed http_request's 256 KiB body cap.
var doer eposnow.Doer = eposnow.StreamDoer{}

// Handlers is every event the plugin answers; each has a manifest hook.
func Handlers() plugin.Handlers { return plugin.Handlers{"import.requested.ask": handleImport} }

// request is the API-source "import.requested.ask" payload.
type request struct {
	EntryKey string   `json:"entry_key"`
	Entities []string `json:"entities"`
	Mode     string   `json:"mode"`
	Source   string   `json:"source"`
}

func fail(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }

func setting(key string) string {
	v, err := plugin.SettingsGet(key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func handleImport(ev plugin.Event) (any, error) {
	var req request
	if err := ev.Decode(&req); err != nil {
		return fail("the import request could not be read"), nil
	}
	if req.Source == "file" {
		return handleFile(ev)
	}
	switch {
	case req.EntryKey != EntryKey:
		return fail("unknown import entry"), nil
	case req.Source != "api":
		return fail("the Epos Now import reads the Epos Now API, not a file"), nil
	case req.Mode != "preview" && req.Mode != "commit":
		return fail("mode must be preview or commit"), nil
	}
	items, stock := false, false
	for _, e := range req.Entities {
		items = items || e == "items"
		stock = stock || e == "stock"
	}
	if !items {
		return fail("the Epos Now import needs the items entity (items:write)"), nil
	}
	key, secret := setting("api_key"), setting("api_secret")
	if key == "" || secret == "" {
		return fail("enter the Epos Now API key and secret in this plugin's settings first"), nil
	}
	c := eposnow.New(doer, key, secret)
	c.Sleep = sleep
	c.Now = now
	res, err := run(c, stock)
	if err != nil {
		return fail(err.Error()), nil
	}
	return map[string]any{"ok": true, "message": res.message(req.Mode, stock), "counts": map[string]int{"items": res.rows}}, nil
}

// result is one run's counts.
type result struct {
	company, location   string
	rows, skipped       int
	grossedUp, netNoVAT int
	badBarcodes, badVAT int
	cutNames            int
}

func (r result) message(mode string, stock bool) string {
	var b strings.Builder
	if mode == "preview" {
		fmt.Fprintf(&b, "Connected to Epos Now account %q. ", r.company)
	} else {
		fmt.Fprintf(&b, "Epos Now account %q: ", r.company)
	}
	fmt.Fprintf(&b, "%d products read at location %q", r.rows, r.location)
	if !stock {
		b.WriteString(", stock not imported")
	}
	var notes []string
	if r.grossedUp > 0 {
		notes = append(notes, plural(r.grossedUp, "net price", "net prices")+" grossed up with VAT")
	}
	if r.netNoVAT > 0 {
		notes = append(notes, plural(r.netNoVAT, "net price", "net prices")+" had no VAT rate and were imported as they are")
	}
	if r.badBarcodes > 0 {
		notes = append(notes, plural(r.badBarcodes, "barcode", "barcodes")+" dropped (not a valid barcode)")
	}
	if r.badVAT > 0 {
		notes = append(notes, plural(r.badVAT, "VAT rate", "VAT rates")+" dropped (outside 0–100%)")
	}
	if r.cutNames > 0 {
		notes = append(notes, plural(r.cutNames, "name", "names")+" shortened to the till's limit")
	}
	if r.skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d skipped without an Epos Now Id", r.skipped))
	}
	if len(notes) > 0 {
		b.WriteString(" (" + strings.Join(notes, "; ") + ")")
	}
	b.WriteString(".")
	return b.String()
}

func (r *result) count(n mapNotes) {
	if n.barcodeDropped {
		r.badBarcodes++
	}
	if n.vatDropped {
		r.badVAT++
	}
	if n.nameCut {
		r.cutNames++
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// run fetches the reference data, then streams the products through the
// mapping into batches of ≤500 rows. A page is mapped whole before any of
// it is submitted; any failure fails the run (the answer is ok:false, so
// the till applies nothing).
//
// The run's clock starts here: past runDeadline no request starts and no
// retry waits (eposnow.ErrSlow), so the till never has to end the ask.
func run(c *eposnow.Client, withStock bool) (result, error) {
	var res result
	c.Deadline = c.Now().Add(runDeadline)
	ti, err := c.TokenInfo()
	if err != nil {
		return res, err
	}
	res.company = eposnow.Str(ti.CompanyName)

	loc, err := chooseLocation(c)
	if err != nil {
		return res, err
	}
	res.location = eposnow.Str(loc.Name)
	cat := &Catalog{LocationID: *loc.ID, Categories: map[int64]eposnow.Category{}, TaxGroups: map[int64]eposnow.TaxGroup{}}

	cats, err := c.Categories()
	if err != nil {
		return res, err
	}
	for _, x := range cats {
		if x.ID != nil {
			cat.Categories[*x.ID] = x
		}
	}
	groups, err := c.TaxGroups()
	if err != nil {
		return res, err
	}
	for _, g := range groups {
		if g.ID != nil {
			cat.TaxGroups[*g.ID] = g
		}
	}
	if withStock {
		st, err := c.Stock(*loc.ID)
		if err != nil {
			return res, err
		}
		cat.Stock = StockByProduct(st, *loc.ID)
	}

	buf := make([]Row, 0, batchSize)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if _, err := submit("items", buf); err != nil {
			return submitError(err)
		}
		buf = buf[:0]
		return nil
	}
	err = c.EachProductPage(func(page []eposnow.Product) error {
		for _, p := range page {
			row, note, ok := cat.MapProduct(p)
			if !ok {
				res.skipped++
				continue
			}
			switch note.price {
			case noteGrossedUp:
				res.grossedUp++
			case noteNetNoVAT:
				res.netNoVAT++
			}
			res.count(note)
			res.rows++
			buf = append(buf, row)
			if len(buf) == batchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, flush()
}

// chooseLocation is the location_id setting's location, else the first
// one Epos Now lists.
func chooseLocation(c *eposnow.Client) (eposnow.Location, error) {
	want := int64(0)
	if s := setting("location_id"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return eposnow.Location{}, errors.New("the location_id setting must be an Epos Now location number, or empty for the first location")
		}
		want = n
	}
	locs, err := c.Locations()
	if err != nil {
		return eposnow.Location{}, err
	}
	for _, l := range locs {
		if l.ID != nil && (want == 0 || *l.ID == want) {
			return l, nil
		}
	}
	if want != 0 {
		return eposnow.Location{}, fmt.Errorf("Epos Now has no location %d (the location_id setting)", want)
	}
	return eposnow.Location{}, errors.New("this Epos Now account has no locations")
}

func submitError(err error) error {
	switch {
	case errors.Is(err, plugin.ErrQuota):
		return errors.New("this Epos Now account has more products than the till imports in one run (20,000)")
	case errors.Is(err, plugin.ErrDenied):
		return errors.New("the till did not accept item rows from this import — check that the plugin holds items:write")
	default:
		return errors.New("the till did not accept a batch of rows from this import")
	}
}

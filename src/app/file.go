// The Epos Now file entry (ut-docs#3377, ADR-0164 §2): reads the product
// export a shop downloads from the Epos Now Back Office, exactly as
// downloaded, and hands ADR-0051 item rows to the till. It runs in the
// import sandbox — on the till and in ut-cloud — where the plugin gets the
// uploaded file and row submits only: no network, settings or secrets.
//
// The export has no Id column, so a row's link is derived from what the
// file has: its barcode, else its name. The export has no stock column
// either (stock is a separate export), and its RRPrice has no home in the
// item row yet (ut-docs#4725): both are left out.
//
// Nothing here logs: names, barcodes and prices never reach the log or the
// answer, which carries counts only.
package app

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// FileEntryKey is the manifest's file-source import entry key.
const FileEntryKey = "eposnow-file"

// maxFileBytes is the biggest export read (core's file-rows cap).
const maxFileBytes = 32 << 20

// maxFileRows is core's per-run row limit (ut-docs#4430).
const maxFileRows = 20_000

// descriptionMaxRunes is core's description bound.
const descriptionMaxRunes = 1000

// skuMaxRunes is core's SKU bound; a longer one would refuse the row.
const skuMaxRunes = 64

// externalIDMaxRunes is core's external_id bound.
const externalIDMaxRunes = 128

// taxGroupRe reads a rate off an Epos Now tax group name: "20VAT", "5VAT",
// "0VAT", "12.5%".
var taxGroupRe = regexp.MustCompile(`(?i)^\s*(?:(\d+(?:\.\d+)?)\s*(?:%|vat|tax)|(?:vat|tax)\s*(\d+(?:\.\d+)?)\s*%?)\s*$`)

// decimalRe is a plain price: digits, optionally a "." fraction or a
// decimal comma with one or two digits ("2,50"; "1,234" is ambiguous).
var decimalRe = regexp.MustCompile(`^(?:\d+(?:\.\d+)?|\d+,\d{1,2})$`)

// fileNotes counts what the file run changed or dropped, for the answer.
type fileNotes struct {
	rows, noName                     int
	badBarcodes, unknownVAT, grossed int
	netNoVAT, cutText                int
}

func handleFile(ev plugin.Event) (any, error) {
	var req plugin.ImportRequest
	if err := ev.Decode(&req); err != nil {
		return fail("the import request could not be read"), nil
	}
	switch {
	case req.EntryKey != FileEntryKey:
		return fail("unknown import entry"), nil
	case req.Mode != "preview" && req.Mode != "commit":
		return fail("mode must be preview or commit"), nil
	}
	items := false
	for _, e := range req.Entities {
		items = items || e == "items"
	}
	if !items {
		return fail("the Epos Now import needs the items entity (items:write)"), nil
	}

	f := req.File()
	defer f.Close()
	n, err := runFile(&limitedReader{r: f, left: maxFileBytes + 1})
	if err != nil {
		return fail(err.Error()), nil
	}
	return map[string]any{"ok": true, "message": n.message(), "counts": map[string]int{"items": n.rows}}, nil
}

// limitedReader reads at most left bytes, then fails: the file is parsed
// as a stream (the plugin's memory is capped), so the size cap is checked
// as bytes arrive, not up front.
type limitedReader struct {
	r    io.Reader
	left int64
}

var errTooLarge = errors.New("this file is larger than the import reads (32 MiB)")

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left <= 0 && err == nil {
		err = errTooLarge
	}
	return n, err
}

// sniffDelimiter is the separator of the first record: the most frequent
// of "," ";" and tab outside quotes; a tie or none is the comma. Epos Now
// writes ";".
func sniffDelimiter(head []byte) rune {
	counts := map[byte]int{}
	inQuotes := false
scan:
	for _, c := range head {
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case inQuotes:
		case c == '\n' || c == '\r':
			break scan
		case c == ',' || c == ';' || c == '\t':
			counts[c]++
		}
	}
	best, bestN := byte(','), counts[',']
	for _, c := range []byte{';', '\t'} {
		if counts[c] > bestN {
			best, bestN = c, counts[c]
		}
	}
	return rune(best)
}

// foldHeader lower-cases a column name and drops its spaces.
func foldHeader(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

// runFile maps every product line of the export and submits the rows in
// batches of ≤500. Any failure fails the run (the answer is ok:false, so
// the till applies nothing).
func runFile(r io.Reader) (fileNotes, error) {
	var n fileNotes
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(64 << 10)
	if bytes.HasPrefix(head, []byte("\xef\xbb\xbf")) {
		_, _ = br.Discard(3)
		head = head[3:]
	}
	cr := csv.NewReader(br)
	cr.Comma = sniffDelimiter(head)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if errors.Is(err, errTooLarge) {
		return n, errTooLarge
	}
	if err != nil {
		return n, errors.New("the file is empty or is not a CSV export")
	}
	idx := map[string]int{}
	for i, h := range header {
		if k := foldHeader(h); k != "" {
			if _, dup := idx[k]; !dup {
				idx[k] = i
			}
		}
	}
	_, hasName := idx["name"]
	_, hasInc := idx["salepriceinctax"]
	_, hasEx := idx["salepriceextax"]
	if !hasName || (!hasInc && !hasEx) {
		return n, errors.New("this does not look like an Epos Now product export (it needs the Name and SalePriceIncTax or SalePriceExTax columns)")
	}

	get := func(rec []string, col string) string {
		if i, ok := idx[col]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	seen := map[string]int{}
	buf := make([]Row, 0, batchSize)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if _, err := submit("items", buf); err != nil {
			return fileSubmitError(err)
		}
		buf = buf[:0]
		return nil
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errTooLarge) {
			return n, errTooLarge
		}
		if err != nil {
			return n, errors.New("the file could not be read as a CSV export")
		}
		row, ok := mapFileRow(rec, get, seen, &n)
		if !ok {
			continue
		}
		if n.rows == maxFileRows {
			return n, errors.New("this file has more products than the till imports in one run (20,000)")
		}
		n.rows++
		buf = append(buf, row)
		if len(buf) == batchSize {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if n.rows == 0 {
		return n, errors.New("the file has no products to import")
	}
	return n, flush()
}

// fileSubmitError is submitError worded for a file run.
func fileSubmitError(err error) error {
	if errors.Is(err, plugin.ErrQuota) {
		return errors.New("this file has more products than the till imports in one run (20,000)")
	}
	return submitError(err)
}

// cleanText drops what core refuses in a name, category or description
// (invalid UTF-8, invisible format characters) and turns line breaks, tabs
// and other control characters into spaces, so one odd cell never costs
// the whole row.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r):
			return ' '
		case unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d':
			return -1
		}
		return r
	}, s)), " ")
}

// mapFileRow maps one export line. ok is false for a line without a name.
func mapFileRow(rec []string, get func([]string, string) string, seen map[string]int, n *fileNotes) (Row, bool) {
	if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
		return Row{}, false // a blank line
	}
	name, cut := cutRunes(cleanText(get(rec, "name")), nameMaxRunes)
	if name == "" {
		n.noName++
		return Row{}, false
	}
	if cut {
		n.cutText++
	}
	var row Row
	row.Name = name

	if b := get(rec, "barcode"); barcodeRe.MatchString(b) {
		row.Barcodes = []string{b}
	} else if b != "" {
		n.badBarcodes++
	}
	if sku := cleanText(get(rec, "articlecode")); sku != "" && utf8.RuneCountInString(sku) <= skuMaxRunes {
		row.SKU = sku
	}
	row.ExternalID = fileLink(row, seen)

	if cat, c := cutRunes(cleanText(get(rec, "categoryid")), categoryMaxRunes); cat != "" {
		row.CategoryPath = []string{cat}
		if c {
			n.cutText++
		}
	}
	if d := cleanText(get(rec, "description")); d != "" && d != name {
		var c bool
		if row.Description, c = cutRunes(d, descriptionMaxRunes); c {
			n.cutText++
		}
	}

	var bp int64
	hasVAT := false
	if g := get(rec, "salepricetaxgroupid"); g != "" {
		if bp, hasVAT = taxGroupBP(g); !hasVAT {
			n.unknownVAT++
		}
	}
	if hasVAT {
		row.VATRateBP = &bp
	}
	inc, ex := get(rec, "salepriceinctax"), get(rec, "salepriceextax")
	// A zero gross price beside a real net price is an unfilled gross
	// column, not a free product: fall back to the net price.
	if r, ok := parseDecimal(inc); ok && r.Sign() == 0 {
		if e, ok := parseDecimal(ex); ok && e.Sign() > 0 {
			inc = ""
		}
	}
	switch {
	case inc != "":
		if r, ok := parseDecimal(inc); ok {
			if v, ok := roundMinor(r); ok {
				row.PriceMinor = &v
			}
		}
	case ex != "":
		if r, ok := parseDecimal(ex); ok {
			if hasVAT {
				r.Mul(r, big.NewRat(10_000+bp, 10_000))
				n.grossed++
			} else {
				n.netNoVAT++
			}
			if v, ok := roundMinor(r); ok {
				row.PriceMinor = &v
			}
		}
	}

	active := true
	switch strings.ToLower(get(rec, "sellontill")) {
	case "no", "false", "0":
		active = false
	}
	row.Active = &active
	return row, true
}

// fileLink is the row's external_id: its barcode, else its ArticleCode,
// else its name. A rename keeps the link only while the item has a barcode
// or an ArticleCode; the export has no Id to link by. A second
// line with the same link gets "#2", "#3"… so two products stay two.
func fileLink(row Row, seen map[string]int) string {
	var id string
	switch {
	case len(row.Barcodes) > 0:
		id = "bc:" + row.Barcodes[0]
	case row.SKU != "":
		id = "sku:" + strings.ToLower(row.SKU)
	default:
		id = "name:" + strings.ToLower(strings.Join(strings.Fields(row.Name), " "))
	}
	id, _ = cutRunes(id, externalIDMaxRunes-8)
	seen[id]++
	if seen[id] > 1 {
		id += "#" + strconv.Itoa(seen[id])
	}
	return id
}

// taxGroupBP reads a rate in basis points off a tax group name.
func taxGroupBP(name string) (int64, bool) {
	m := taxGroupRe.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	num := m[1]
	if num == "" {
		num = m[2]
	}
	pct, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false
	}
	bp := int64(math.Round(pct * 100))
	return bp, bp >= 0 && bp <= 10_000
}

// parseDecimal reads a price as the decimal the file wrote; a decimal
// comma is accepted. Never float arithmetic.
func parseDecimal(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if !decimalRe.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(strings.Replace(s, ",", ".", 1))
	return r, ok
}

func (n fileNotes) message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Epos Now export: %s read, stock not in this file", plural(n.rows, "product", "products"))
	var notes []string
	add := func(c int, one, many, tail string) {
		if c > 0 {
			notes = append(notes, plural(c, one, many)+" "+tail)
		}
	}
	add(n.grossed, "net price", "net prices", "grossed up with VAT")
	add(n.netNoVAT, "net price", "net prices", "had no VAT rate and were imported as they are")
	add(n.badBarcodes, "barcode", "barcodes", "dropped (not a valid barcode)")
	add(n.unknownVAT, "VAT rate", "VAT rates", "not recognised (left to the till's default)")
	add(n.cutText, "text", "texts", "shortened to the till's limit")
	add(n.noName, "product", "products", "skipped without a name")
	if len(notes) > 0 {
		b.WriteString(" (" + strings.Join(notes, "; ") + ")")
	}
	b.WriteString(".")
	return b.String()
}

package app

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/ut-plugin-import-eposnow/src/eposnow"
)

// Row is one ADR-0051 item row exactly as core's internal/catsync parses
// it (wireRow): the same JSON names, no "source" (core stamps it). Core
// validates the bounds; a row outside them is a per-row issue there.
type Row struct {
	ExternalID   string   `json:"external_id"`
	Name         string   `json:"name"`
	SKU          string   `json:"sku,omitempty"`
	Barcodes     []string `json:"barcodes,omitempty"`
	PriceMinor   *int64   `json:"price_minor"`
	CategoryPath []string `json:"category_path,omitempty"`
	VATRateBP    *int64   `json:"vat_rate_bp,omitempty"`
	StockQty     *float64 `json:"stock_qty,omitempty"`
	Active       *bool    `json:"active"`
}

// categoryDepth is core's category_path limit (catsync categoryDepth).
const categoryDepth = 4

// maxMinor bounds a converted price to int64 range; core's own ceiling
// (catsync priceMinorMax) then reports anything implausible as an issue.
var maxMinor = big.NewInt(math.MaxInt64)

// decimalRat is v as the shortest decimal that round-trips the double —
// the number the API actually sent (2.675, not 2.67499999…).
func decimalRat(v float64) (*big.Rat, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
	return r, ok
}

// roundMinor rounds currency units r to minor units (2 dp), half away
// from zero ("half-up" for the non-negative prices that matter).
func roundMinor(r *big.Rat) (int64, bool) {
	x := new(big.Rat).Mul(r, big.NewRat(100, 1))
	neg := x.Sign() < 0
	x.Abs(x)
	// floor(x + 1/2) = (2·num + den) div (2·den)
	num := new(big.Int).Add(new(big.Int).Lsh(x.Num(), 1), x.Denom())
	q := new(big.Int).Quo(num, new(big.Int).Lsh(x.Denom(), 1))
	if q.Cmp(maxMinor) > 0 {
		return 0, false
	}
	if neg {
		q.Neg(q)
	}
	return q.Int64(), true
}

// PriceMinor converts an Epos Now price (currency units, double) to minor
// units, rounded half-up at 2 dp.
func PriceMinor(v float64) (int64, bool) {
	r, ok := decimalRat(v)
	if !ok {
		return 0, false
	}
	return roundMinor(r)
}

// GrossUpMinor is a tax-exclusive price plus VAT at bp basis points, in
// minor units, rounded once (no double rounding of the net price).
func GrossUpMinor(net float64, bp int64) (int64, bool) {
	r, ok := decimalRat(net)
	if !ok {
		return 0, false
	}
	r.Mul(r, big.NewRat(10_000+bp, 10_000))
	return roundMinor(r)
}

// VATRateBP picks a tax group's rate for location loc: the rates for that
// location, else the ones for every location (LocationId null or 0); among
// them the lowest Priority. Percentage×100 → basis points, rounded.
func VATRateBP(g eposnow.TaxGroup, loc int64) (int64, bool) {
	pick := func(match func(*int64) bool) (*eposnow.TaxGroupRate, bool) {
		var best *eposnow.TaxGroupRate
		for i := range g.TaxRates {
			r := &g.TaxRates[i]
			if r.Percentage == nil || !match(r.LocationID) {
				continue
			}
			if best == nil || prio(r) < prio(best) {
				best = r
			}
		}
		return best, best != nil
	}
	r, ok := pick(func(l *int64) bool { return l != nil && *l == loc && loc != 0 })
	if !ok {
		r, ok = pick(func(l *int64) bool { return l == nil || *l == 0 })
	}
	if !ok {
		return 0, false
	}
	return int64(math.Round(*r.Percentage * 100)), true
}

func prio(r *eposnow.TaxGroupRate) int64 {
	if r.Priority == nil {
		return math.MaxInt64
	}
	return *r.Priority
}

// CategoryPath is category id's ParentId chain, top level first. A missing
// parent or a cycle ends the chain, and so does a blank or null name: the
// valid levels above it are kept (core refuses a path with an empty
// level). Deeper than core's 4 levels, the 4 nearest the item are kept
// (the levels a shop picks from). An unknown id, or a blank top level, is
// nil: the item's category is left as it is.
func CategoryPath(cats map[int64]eposnow.Category, id int64) []string {
	var up []string // leaf first
	seen := map[int64]bool{}
	for cur := id; cur != 0 && !seen[cur]; {
		c, ok := cats[cur]
		if !ok {
			break
		}
		seen[cur] = true
		up = append(up, strings.TrimSpace(eposnow.Str(c.Name)))
		if c.ParentID == nil {
			break
		}
		cur = *c.ParentID
	}
	path := make([]string, 0, len(up))
	for i := len(up) - 1; i >= 0; i-- {
		if up[i] == "" {
			break
		}
		path = append(path, up[i])
	}
	if len(path) > categoryDepth {
		path = path[len(path)-categoryDepth:]
	}
	if len(path) == 0 {
		return nil
	}
	return path
}

// StockByProduct sums ProductStockBatches[].CurrentStock per product at
// location loc. A product with a stock record but no batches has 0.
func StockByProduct(st []eposnow.ProductStock, loc int64) map[int64]float64 {
	out := map[int64]float64{}
	for _, s := range st {
		if s.ProductID == nil || (s.LocationID != nil && *s.LocationID != loc) {
			continue
		}
		q := out[*s.ProductID]
		for _, b := range s.ProductStockBatches {
			if b.CurrentStock != nil {
				q += float64(*b.CurrentStock)
			}
		}
		out[*s.ProductID] = q
	}
	return out
}

// Catalog is the reference data a product is mapped against.
type Catalog struct {
	Categories map[int64]eposnow.Category
	TaxGroups  map[int64]eposnow.TaxGroup
	Stock      map[int64]float64 // nil: stock is not imported this run
	LocationID int64
}

// mapNote flags a price the reply message reports.
type mapNote int

const (
	noteNone      mapNote = iota
	noteGrossedUp         // net price grossed up with its VAT rate
	noteNetNoVAT          // net price with no VAT rate: imported as is
)

// mapNotes is what MapProduct changed or dropped, for the reply message.
type mapNotes struct {
	price          mapNote
	barcodeDropped bool // not core's barcode pattern: dropped, product kept
	vatDropped     bool // rate outside 0..10000 bp: dropped
	nameCut        bool // name or a category level cut to core's limit
}

// Core's text limits (catsync nameMaxRunes, categoryMaxRune).
const (
	nameMaxRunes     = 200
	categoryMaxRunes = 100
)

// barcodeRe is core's barcode pattern (catsync barcodeRe).
var barcodeRe = regexp.MustCompile(`^[0-9A-Za-z\-]{1,64}$`)

func trimmed(s *string) string { return strings.TrimSpace(eposnow.Str(s)) }

// cutRunes trims s and cuts it to max runes at a rune boundary (core
// refuses a longer one, the whole row with it); cut reports a cut.
func cutRunes(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	n := 0
	for i := range s {
		if n == max {
			return strings.TrimSpace(s[:i]), true
		}
		n++
	}
	return s, false
}

// MapProduct maps one Epos Now product to a row. ok is false for a record
// without an Id (it could never be linked). Field validation is core's.
func (c *Catalog) MapProduct(p eposnow.Product) (Row, mapNotes, bool) {
	var notes mapNotes
	if p.ID == nil {
		return Row{}, notes, false
	}
	row := Row{ExternalID: strconv.FormatInt(*p.ID, 10)}
	row.Name, notes.nameCut = cutRunes(eposnow.Str(p.Name), nameMaxRunes)
	if row.SKU = trimmed(p.Sku); row.SKU == "" {
		row.SKU = trimmed(p.ArticleCode)
	}
	if b := trimmed(p.Barcode); barcodeRe.MatchString(b) {
		row.Barcodes = []string{b}
	} else if b != "" {
		notes.barcodeDropped = true
	}
	if p.CategoryID != nil {
		row.CategoryPath = CategoryPath(c.Categories, *p.CategoryID)
		for i, lvl := range row.CategoryPath {
			var cut bool
			if row.CategoryPath[i], cut = cutRunes(lvl, categoryMaxRunes); cut {
				notes.nameCut = true
			}
		}
	}
	var bp int64
	hasVAT := false
	if p.SalePriceTaxGroupID != nil {
		if g, ok := c.TaxGroups[*p.SalePriceTaxGroupID]; ok {
			bp, hasVAT = VATRateBP(g, c.LocationID)
		}
	}
	if hasVAT && (bp < 0 || bp > 10_000) {
		hasVAT, notes.vatDropped = false, true
	}
	if hasVAT {
		row.VATRateBP = &bp
	}
	note := noteNone
	if p.SalePrice != nil {
		net := p.IsSalePriceIncTax != nil && !*p.IsSalePriceIncTax
		var v int64
		var ok bool
		switch {
		case net && hasVAT:
			v, ok = GrossUpMinor(*p.SalePrice, bp)
			note = noteGrossedUp
		case net:
			v, ok = PriceMinor(*p.SalePrice)
			note = noteNetNoVAT
		default:
			v, ok = PriceMinor(*p.SalePrice)
		}
		if ok {
			row.PriceMinor = &v
		}
	}
	active := (p.IsArchived == nil || !*p.IsArchived) && (p.SellOnTill == nil || *p.SellOnTill)
	row.Active = &active
	if c.Stock != nil {
		if q, ok := c.Stock[*p.ID]; ok {
			row.StockQty = &q
		}
	}
	notes.price = note
	return row, notes, true
}

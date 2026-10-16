//go:build !wasip1

package app

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/ut-plugin-import-eposnow/src/eposnow"
)

// decode builds a spec-shaped record from JSON using only the spec's
// field names (definitions.Product, Category, TaxGroup, IProductStock).
func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("fixture %s: %v", s, err)
	}
	return v
}

func TestPriceMinorRoundsHalfUpAtTwoDecimals(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int64
	}{
		{0, 0}, {1, 100}, {2.5, 250}, {9.99, 999},
		{2.675, 268}, // binary 2.67499999… — rounded as the decimal the API sent
		{1.005, 101}, {0.125, 13}, {0.124, 12}, {1234.5678, 123457},
		{0.1 + 0.2, 30},
	} {
		got, ok := PriceMinor(tc.in)
		if !ok || got != tc.want {
			t.Errorf("PriceMinor(%v) = %d, %v; want %d", tc.in, got, ok, tc.want)
		}
	}
	if _, ok := PriceMinor(1e30); ok {
		t.Error("PriceMinor(1e30) must refuse an out-of-range price")
	}
}

func TestGrossUpMinor(t *testing.T) {
	for _, tc := range []struct {
		net  float64
		bp   int64
		want int64
	}{
		{10, 2000, 1200},
		{0.99, 2000, 119},   // 1.188 → 1.19
		{4.1625, 2000, 500}, // exactly 4.995 → half-up 5.00, no double rounding
		{8.33, 2000, 1000},  // 9.996 → 10.00
		{3.5, 0, 350},
		{1.05, 550, 111}, // 1.10775 → 1.11
	} {
		got, ok := GrossUpMinor(tc.net, tc.bp)
		if !ok || got != tc.want {
			t.Errorf("GrossUpMinor(%v, %d) = %d, %v; want %d", tc.net, tc.bp, got, ok, tc.want)
		}
	}
}

func TestVATRateSelection(t *testing.T) {
	g := decode[eposnow.TaxGroup](t, `{"Id":5,"Name":"Standard","TaxRates":[
		{"TaxGroupId":5,"TaxRateId":1,"LocationId":null,"Priority":2,"Percentage":20.0,"Name":"VAT","TaxCode":"S"},
		{"TaxGroupId":5,"TaxRateId":2,"LocationId":0,"Priority":1,"Percentage":17.5,"Name":"Old","TaxCode":"S"},
		{"TaxGroupId":5,"TaxRateId":3,"LocationId":7,"Priority":3,"Percentage":5.0,"Name":"Loc7 B","TaxCode":"R"},
		{"TaxGroupId":5,"TaxRateId":4,"LocationId":7,"Priority":1,"Percentage":12.345,"Name":"Loc7 A","TaxCode":"R"},
		{"TaxGroupId":5,"TaxRateId":5,"LocationId":9,"Priority":0,"Percentage":0,"Name":"Loc9","TaxCode":"Z"}]}`)
	// The location's own rates win; among them the lowest Priority;
	// Percentage×100 rounded to basis points.
	if bp, ok := VATRateBP(g, 7); !ok || bp != 1235 {
		t.Errorf("location 7: %d, %v; want 1235", bp, ok)
	}
	// No rate for the location → LocationId null or 0, lowest Priority.
	if bp, ok := VATRateBP(g, 8); !ok || bp != 1750 {
		t.Errorf("location 8: %d, %v; want 1750", bp, ok)
	}
	if bp, ok := VATRateBP(g, 9); !ok || bp != 0 {
		t.Errorf("location 9 (zero-rated): %d, %v; want 0", bp, ok)
	}
	none := decode[eposnow.TaxGroup](t, `{"Id":6,"Name":"Elsewhere","TaxRates":[{"LocationId":3,"Priority":1,"Percentage":20}]}`)
	if _, ok := VATRateBP(none, 7); ok {
		t.Error("a group with no rate for this location or for all locations has no rate")
	}
	noPct := decode[eposnow.TaxGroup](t, `{"Id":6,"TaxRates":[{"LocationId":null,"Priority":1,"Percentage":null}]}`)
	if _, ok := VATRateBP(noPct, 7); ok {
		t.Error("a rate with no Percentage is no rate")
	}
}

func catsFixture(t *testing.T, s string) map[int64]eposnow.Category {
	m := map[int64]eposnow.Category{}
	for _, c := range decode[[]eposnow.Category](t, s) {
		m[*c.ID] = c
	}
	return m
}

func TestCategoryPath(t *testing.T) {
	cats := catsFixture(t, `[
		{"Id":1,"ParentId":null,"RootParentId":null,"Name":"Drinks","ShowOnTill":true},
		{"Id":2,"ParentId":1,"RootParentId":1,"Name":"Hot"},
		{"Id":3,"ParentId":2,"RootParentId":1,"Name":"Coffee"},
		{"Id":4,"ParentId":3,"Name":"Espresso"},
		{"Id":5,"ParentId":4,"Name":"Single origin"},
		{"Id":6,"ParentId":0,"Name":"Food"},
		{"Id":7,"ParentId":99,"Name":"Orphan"},
		{"Id":10,"ParentId":11,"Name":"Loop A"},
		{"Id":11,"ParentId":10,"Name":"Loop B"},
		{"Id":12,"ParentId":12,"Name":"Self"},
		{"Id":20,"ParentId":null,"Name":"Bakery"},
		{"Id":21,"ParentId":20,"Name":"  "},
		{"Id":22,"ParentId":21,"Name":"Bread"},
		{"Id":23,"ParentId":null,"Name":null},
		{"Id":24,"ParentId":23,"Name":"Cakes"}]`)
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{1, "Drinks"},
		{3, "Drinks/Hot/Coffee"},
		{4, "Drinks/Hot/Coffee/Espresso"},
		// Deeper than core's 4 levels: the 4 nearest the item are kept.
		{5, "Hot/Coffee/Espresso/Single origin"},
		{6, "Food"},
		{7, "Orphan"}, // a missing parent ends the chain
		{10, "Loop B/Loop A"},
		{11, "Loop A/Loop B"},
		{12, "Self"},
		{42, ""}, // unknown category → no path (leave as is)
		// A blank or null level ends the chain: the valid levels above it
		// stay; a blank leaf or root leaves no path.
		{22, "Bakery"},
		{21, "Bakery"},
		{24, ""},
	} {
		if got := strings.Join(CategoryPath(cats, tc.id), "/"); got != tc.want {
			t.Errorf("CategoryPath(%d) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestStockByProductSumsBatchesAtTheLocation(t *testing.T) {
	st := decode[[]eposnow.ProductStock](t, `[
		{"Id":1,"ProductId":10,"LocationId":7,"MinStock":0,"MaxStock":50,"ProductStockBatches":[
			{"Id":1,"ProductStockId":1,"CreatedDate":"2026-01-01T00:00:00Z","CurrentStock":4,"CurrentVolume":0,"CostPrice":1.2},
			{"Id":2,"ProductStockId":1,"CurrentStock":-1},
			{"Id":3,"ProductStockId":1,"CurrentStock":null}]},
		{"Id":2,"ProductId":10,"LocationId":7,"ProductStockBatches":[{"CurrentStock":2}]},
		{"Id":3,"ProductId":11,"LocationId":8,"ProductStockBatches":[{"CurrentStock":9}]},
		{"Id":4,"ProductId":12,"LocationId":7,"ProductStockBatches":[]},
		{"Id":5,"ProductId":null,"LocationId":7,"ProductStockBatches":[{"CurrentStock":1}]}]`)
	got := StockByProduct(st, 7)
	if got[10] != 5 {
		t.Errorf("product 10 = %v, want 5 (4 - 1 + 2)", got[10])
	}
	if _, ok := got[11]; ok {
		t.Error("another location's stock must not count")
	}
	if q, ok := got[12]; !ok || q != 0 {
		t.Errorf("a record with no batches is 0 stock, got %v, %v", q, ok)
	}
	if len(got) != 2 {
		t.Errorf("got %v", got)
	}
}

func TestMapProduct(t *testing.T) {
	c := &Catalog{
		Categories: catsFixture(t, `[{"Id":1,"ParentId":null,"Name":"Drinks"},{"Id":2,"ParentId":1,"Name":"Hot"}]`),
		TaxGroups: map[int64]eposnow.TaxGroup{
			5: decode[eposnow.TaxGroup](t, `{"Id":5,"Name":"Standard","TaxRates":[{"LocationId":null,"Priority":1,"Percentage":20}]}`),
		},
		Stock:      map[int64]float64{10: 12},
		LocationID: 7,
	}
	p := decode[eposnow.Product](t, `{"Id":10,"Name":"Flat white","Description":"","CostPrice":0.8,"IsCostPriceIncTax":false,
		"SalePrice":3.2,"IsSalePriceIncTax":true,"EatOutPrice":3.4,"IsEatOutPriceIncTax":true,"CategoryId":2,
		"Barcode":"5012345678900","SalePriceTaxGroupId":5,"EatOutPriceTaxGroupId":5,"Sku":"FW-1","ArticleCode":"A-9",
		"SellOnTill":true,"IsArchived":false,"VariantGroupId":null,"ProductType":0}`)
	row, note, ok := c.MapProduct(p)
	if !ok || note != (mapNotes{}) {
		t.Fatalf("ok=%v note=%v", ok, note)
	}
	b, _ := json.Marshal(row)
	want := `{"external_id":"10","name":"Flat white","sku":"FW-1","barcodes":["5012345678900"],"price_minor":320,"category_path":["Drinks","Hot"],"vat_rate_bp":2000,"stock_qty":12,"active":true}`
	if string(b) != want {
		t.Fatalf("row\n got %s\nwant %s", b, want)
	}

	// Sku blank → ArticleCode; archived or not sold on the till → inactive;
	// no stock record → no stock_qty (leave it); no barcode/category/tax.
	p2 := decode[eposnow.Product](t, `{"Id":11,"Name":"Old mug","SalePrice":5,"IsSalePriceIncTax":true,"CategoryId":null,
		"Barcode":"  ","SalePriceTaxGroupId":null,"Sku":" ","ArticleCode":"MUG-2","SellOnTill":true,"IsArchived":true}`)
	row, _, _ = c.MapProduct(p2)
	b, _ = json.Marshal(row)
	want = `{"external_id":"11","name":"Old mug","sku":"MUG-2","price_minor":500,"active":false}`
	if string(b) != want {
		t.Fatalf("row2\n got %s\nwant %s", b, want)
	}
	p3 := decode[eposnow.Product](t, `{"Id":12,"Name":"Hidden","SalePrice":1,"SellOnTill":false,"IsArchived":false}`)
	if row, _, _ = c.MapProduct(p3); row.Active == nil || *row.Active {
		t.Errorf("SellOnTill=false must be inactive: %+v", row)
	}
	// Stock not imported this run (no stock:write): no stock_qty at all.
	c2 := *c
	c2.Stock = nil
	if row, _, _ = c2.MapProduct(p); row.StockQty != nil {
		t.Errorf("stock_qty without a stock run: %v", *row.StockQty)
	}
	// A record without an Id cannot be linked: skipped.
	if _, _, ok := c.MapProduct(decode[eposnow.Product](t, `{"Id":null,"Name":"x","SalePrice":1}`)); ok {
		t.Error("a product without Id must be skipped")
	}
	// A missing SalePrice leaves price_minor null: core reports the row.
	if row, _, ok := c.MapProduct(decode[eposnow.Product](t, `{"Id":13,"Name":"No price","SalePrice":null}`)); !ok || row.PriceMinor != nil {
		t.Errorf("no SalePrice: ok=%v price=%v", ok, row.PriceMinor)
	}
}

// IsSalePriceIncTax=false → the net price is grossed up with the same VAT
// rate, and noted for the reply message.
func TestMapProductGrossesUpNetPrices(t *testing.T) {
	c := &Catalog{TaxGroups: map[int64]eposnow.TaxGroup{
		5: decode[eposnow.TaxGroup](t, `{"Id":5,"TaxRates":[{"LocationId":null,"Priority":1,"Percentage":20}]}`),
	}, LocationID: 7}
	row, note, _ := c.MapProduct(decode[eposnow.Product](t, `{"Id":1,"Name":"Net","SalePrice":0.99,"IsSalePriceIncTax":false,"SalePriceTaxGroupId":5}`))
	if row.PriceMinor == nil || *row.PriceMinor != 119 || note.price != noteGrossedUp {
		t.Fatalf("price=%v note=%v; want 119, grossed up", row.PriceMinor, note)
	}
	if row.VATRateBP == nil || *row.VATRateBP != 2000 {
		t.Fatalf("vat = %v", row.VATRateBP)
	}
	// Net price but no VAT rate to gross up with: imported as is, noted.
	row, note, _ = c.MapProduct(decode[eposnow.Product](t, `{"Id":2,"Name":"Net","SalePrice":2,"IsSalePriceIncTax":false,"SalePriceTaxGroupId":99}`))
	if *row.PriceMinor != 200 || note.price != noteNetNoVAT || row.VATRateBP != nil {
		t.Fatalf("price=%v note=%v vat=%v", *row.PriceMinor, note, row.VATRateBP)
	}
	// IsSalePriceIncTax null is taken as inclusive (the till's own model).
	row, note, _ = c.MapProduct(decode[eposnow.Product](t, `{"Id":3,"Name":"Null","SalePrice":2,"IsSalePriceIncTax":null,"SalePriceTaxGroupId":5}`))
	if *row.PriceMinor != 200 || note.price != noteNone {
		t.Fatalf("price=%v note=%v", *row.PriceMinor, note)
	}
}

// Fields core would refuse are cleaned here, so the product still imports:
// a barcode outside core's pattern is dropped, a VAT rate outside
// 0..10000 bp is dropped, an over-long name is cut at a rune boundary —
// each counted for the reply message.
func TestMapProductCleansFieldsToCoresRules(t *testing.T) {
	long := strings.Repeat("é", 250)
	c := &Catalog{
		Categories: map[int64]eposnow.Category{
			1: decode[eposnow.Category](t, `{"Id":1,"ParentId":null,"Name":"`+strings.Repeat("ß", 120)+`"}`),
		},
		TaxGroups: map[int64]eposnow.TaxGroup{
			5: decode[eposnow.TaxGroup](t, `{"Id":5,"TaxRates":[{"LocationId":null,"Priority":1,"Percentage":20}]}`),
			6: decode[eposnow.TaxGroup](t, `{"Id":6,"TaxRates":[{"LocationId":null,"Priority":1,"Percentage":150}]}`),
			7: decode[eposnow.TaxGroup](t, `{"Id":7,"TaxRates":[{"LocationId":null,"Priority":1,"Percentage":-5}]}`),
		},
		LocationID: 7,
	}
	for _, tc := range []struct {
		barcode string
		want    []string
		dropped bool
	}{
		{" 501-Ab ", []string{"501-Ab"}, false},
		{"50 12", nil, true},
		{"5012/34", nil, true},
		{"ünï", nil, true},
		{strings.Repeat("1", 64), []string{strings.Repeat("1", 64)}, false},
		{strings.Repeat("1", 65), nil, true},
		{"   ", nil, false}, // blank is no barcode, not a dropped one
	} {
		p := decode[eposnow.Product](t, `{"Id":1,"Name":"X","SalePrice":1,"Barcode":`+strconv.Quote(tc.barcode)+`}`)
		row, note, ok := c.MapProduct(p)
		if !ok || strings.Join(row.Barcodes, ",") != strings.Join(tc.want, ",") || note.barcodeDropped != tc.dropped {
			t.Errorf("barcode %q: ok=%v barcodes=%v dropped=%v", tc.barcode, ok, row.Barcodes, note.barcodeDropped)
		}
	}
	for _, g := range []string{"6", "7"} {
		p := decode[eposnow.Product](t, `{"Id":2,"Name":"X","SalePrice":2,"IsSalePriceIncTax":false,"SalePriceTaxGroupId":`+g+`}`)
		row, note, ok := c.MapProduct(p)
		if !ok || row.VATRateBP != nil || !note.vatDropped || note.price != noteNetNoVAT || *row.PriceMinor != 200 {
			t.Errorf("tax group %s: ok=%v vat=%v notes=%+v price=%v", g, ok, row.VATRateBP, note, row.PriceMinor)
		}
	}
	p := decode[eposnow.Product](t, `{"Id":3,"Name":"  `+long+`  ","SalePrice":1,"CategoryId":1,"SalePriceTaxGroupId":5}`)
	row, note, ok := c.MapProduct(p)
	if !ok || !note.nameCut || note.vatDropped || note.barcodeDropped {
		t.Fatalf("ok=%v notes=%+v", ok, note)
	}
	if row.Name != strings.Repeat("é", nameMaxRunes) {
		t.Errorf("name cut to %d runes, want %d", utf8.RuneCountInString(row.Name), nameMaxRunes)
	}
	if len(row.CategoryPath) != 1 || row.CategoryPath[0] != strings.Repeat("ß", categoryMaxRunes) {
		t.Errorf("category level cut to %v", row.CategoryPath)
	}
	// Exactly at the limit: kept whole, nothing counted.
	p = decode[eposnow.Product](t, `{"Id":4,"Name":"`+strings.Repeat("é", nameMaxRunes)+`","SalePrice":1}`)
	if row, note, _ = c.MapProduct(p); note.nameCut || utf8.RuneCountInString(row.Name) != nameMaxRunes {
		t.Errorf("a name at the limit: notes=%+v", note)
	}
}

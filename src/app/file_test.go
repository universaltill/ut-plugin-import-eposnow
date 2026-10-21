//go:build !wasip1

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// testdata/products.csv is invented data in the real Epos Now product
// export's shape: ";"-separated, CRLF, the 27-column header, a category
// NAME in CategoryId, no Id column, no stock column (ut-docs#3377).

func fileHost(t *testing.T, data []byte) *plugin.FakeHost {
	t.Helper()
	h := plugin.NewFakeHost()
	h.PluginID = PluginID
	h.FileImport = true
	h.ImportEntities = map[string]bool{"items": true}
	h.ImportFiles[1] = data
	plugin.UseFakeHost(t, h)
	return h
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/products.csv")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func filePayload(mode string, size int) map[string]any {
	return map[string]any{"entry_key": "eposnow-file", "entities": []string{"items"}, "file_handle": 1, "file_name": "ProductList.csv", "file_size": size, "mode": mode, "source": "file"}
}

func byName(t *testing.T, h *plugin.FakeHost) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range rows(t, h) {
		out[r["external_id"].(string)] = r
	}
	return out
}

func TestFileEntry_MapsTheExportRows(t *testing.T) {
	data := fixture(t)
	h := fileHost(t, data)
	got := ask(t, filePayload("preview", len(data)))
	if !got.OK {
		t.Fatalf("answer = %+v", got)
	}
	rs := byName(t, h)
	// 10 data rows, one with a blank name is skipped.
	if got.Counts["items"] != 9 || len(rs) != 9 {
		t.Fatalf("counts %v, %d rows submitted", got.Counts, len(rs))
	}

	blades, _ := json.Marshal(rs["bc:5000000000011"])
	want := `{"active":true,"barcodes":["5000000000011"],"category_path":["Shaving"],"external_id":"bc:5000000000011","name":"Acme Double Edge Blades","price_minor":899,"vat_rate_bp":2000}`
	if string(blades) != want {
		t.Errorf("blades\n got %s\nwant %s", blades, want)
	}

	comb := rs["sku:art-9"]
	if comb == nil {
		t.Fatalf("no row for the barcode-less comb: %v", keys(rs))
	}
	if comb["sku"] != "ART-9" || comb["description"] != "Comb, yellow, fits all Acme clippers" || comb["price_minor"] != float64(1075) {
		t.Errorf("comb = %v", comb)
	}
	if _, ok := comb["category_path"]; ok {
		t.Error("a blank category leaves the item's category alone (no category_path)")
	}
	if _, ok := comb["barcodes"]; ok {
		t.Error("no barcode, no barcodes")
	}
	if _, ok := rs["bc:5000000000011"]["description"]; ok {
		t.Error("a description equal to the name is not submitted")
	}

	if brush := rs["name:bad barcode brush"]; brush == nil || brush["barcodes"] != nil {
		t.Errorf("an invalid barcode is dropped, the product kept: %v", brush)
	}
	if spray := rs["bc:5000000000028"]; spray == nil || spray["active"] != false {
		t.Errorf("SellOnTill=no is inactive: %v", spray)
	}
	if mystery := rs["bc:5000000000035"]; mystery == nil || mystery["vat_rate_bp"] != nil || mystery["price_minor"] != float64(110) {
		t.Errorf("an unknown tax group gives no VAT rate: %v", mystery)
	}
	// Net-only price grossed up with the 5% group, rounded once.
	if tonic := rs["bc:5000000000042"]; tonic == nil || tonic["price_minor"] != float64(1050) || tonic["vat_rate_bp"] != float64(500) {
		t.Errorf("net-only tonic: %v", tonic)
	}
	if tea := rs["bc:5000000000059"]; tea == nil || tea["name"] != "Tea; Green" {
		t.Errorf("a quoted ';' stays in the name: %v", tea)
	}
	// Two barcode-less products with the same name stay two products.
	if rs["name:gift box"] == nil || rs["name:gift box#2"] == nil {
		t.Errorf("duplicate names need distinct ids: %v", keys(rs))
	}
	if rs["name:gift box"]["price_minor"] != float64(600) || rs["name:gift box#2"]["price_minor"] != float64(1080) {
		t.Errorf("gift boxes: %v %v", rs["name:gift box"], rs["name:gift box#2"])
	}

	for _, s := range []string{"9 products read", "1 barcode dropped", "1 VAT rate not recognised", "1 net price grossed up", "1 product skipped without a name"} {
		if !strings.Contains(got.Message, s) {
			t.Errorf("message %q lacks %q", got.Message, s)
		}
	}
	if strings.Contains(got.Message, "Acme") || strings.Contains(strings.Join(h.Logs, "\n"), "Acme") {
		t.Error("product names must not reach the answer or the log")
	}
}

func keys(m map[string]map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestFileEntry_PreviewAndCommitSubmitTheSameRows(t *testing.T) {
	data := fixture(t)
	var counts []int
	for _, mode := range []string{"preview", "commit"} {
		h := fileHost(t, data)
		if got := ask(t, filePayload(mode, len(data))); !got.OK {
			t.Fatalf("%s: %+v", mode, got)
		}
		counts = append(counts, len(rows(t, h)))
	}
	if counts[0] != counts[1] {
		t.Errorf("preview %d rows, commit %d", counts[0], counts[1])
	}
}

func TestFileEntry_NoBOMAndCommaSeparated(t *testing.T) {
	data := bytes.TrimPrefix(fixture(t), []byte("\xef\xbb\xbf"))
	h := fileHost(t, data)
	if got := ask(t, filePayload("preview", len(data))); !got.OK || got.Counts["items"] != 9 {
		t.Fatalf("no BOM: %+v", got)
	}
	_ = h
	csvText := "Name,SalePriceIncTax,SalePriceTaxGroupId,Barcode\nTea,2.40,20VAT,5000000000059\n"
	h2 := fileHost(t, []byte(csvText))
	if got := ask(t, filePayload("preview", len(csvText))); !got.OK || got.Counts["items"] != 1 {
		t.Fatalf("comma file: %+v", got)
	}
	if r := rows(t, h2)[0]; r["price_minor"] != float64(240) || r["vat_rate_bp"] != float64(2000) {
		t.Errorf("comma row: %v", r)
	}
}

func TestFileEntry_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		data string
		mod  func(map[string]any)
		want string
	}{
		"not an Epos Now file": {data: "Title,Cost\nTea,1\n", want: "Epos Now product export"},
		"no rows":              {data: "Name;SalePriceIncTax\r\n", want: "no products"},
		"wrong entry":          {data: "Name;SalePriceIncTax\r\nTea;1\r\n", mod: func(p map[string]any) { p["entry_key"] = "eposnow" }, want: "unknown import entry"},
		"bad mode":             {data: "Name;SalePriceIncTax\r\nTea;1\r\n", mod: func(p map[string]any) { p["mode"] = "x" }, want: "mode must be"},
		"no items entity":      {data: "Name;SalePriceIncTax\r\nTea;1\r\n", mod: func(p map[string]any) { p["entities"] = []string{"stock"} }, want: "items entity"},
	} {
		h := fileHost(t, []byte(tc.data))
		p := filePayload("preview", len(tc.data))
		if tc.mod != nil {
			tc.mod(p)
		}
		got := ask(t, p)
		if got.OK || !strings.Contains(got.Error, tc.want) {
			t.Errorf("%s: %+v, want error containing %q", name, got, tc.want)
		}
		if len(rows(t, h)) != 0 {
			t.Errorf("%s: rows submitted despite the refusal", name)
		}
	}
}

func TestFileEntry_AnApiAskStillNeedsTheApiEntry(t *testing.T) {
	h := fileHost(t, fixture(t))
	p := filePayload("preview", 10)
	p["entry_key"] = "eposnow"
	if got := ask(t, p); got.OK {
		t.Errorf("the API entry must not read a file: %+v", got)
	}
	_ = h
}

// Review findings (ut-docs#3377, 2026-10-21).

func TestFileEntry_TaxGroupNeedsAVATWord(t *testing.T) {
	for in, want := range map[string]int64{"20VAT": 2000, "5VAT": 500, "0VAT": 0, "12.5%": 1250, "VAT 20%": 2000, "20 vat": 2000} {
		if bp, ok := taxGroupBP(in); !ok || bp != want {
			t.Errorf("taxGroupBP(%q) = %d, %v, want %d", in, bp, ok, want)
		}
	}
	for _, in := range []string{"3", "12", "Standard", "VAT", "", "150VAT"} {
		if bp, ok := taxGroupBP(in); ok {
			t.Errorf("taxGroupBP(%q) = %d: a bare number or word is not a rate", in, bp)
		}
	}
}

func TestFileEntry_PricesArePlainDecimals(t *testing.T) {
	for in, want := range map[string]int64{"8.99": 899, "2": 200, "2,5": 250, "2,50": 250, "0.005": 1} {
		r, ok := parseDecimal(in)
		if !ok {
			t.Errorf("parseDecimal(%q) refused", in)
			continue
		}
		if v, _ := roundMinor(r); v != want {
			t.Errorf("parseDecimal(%q) = %d minor, want %d", in, v, want)
		}
	}
	for _, in := range []string{"1,234", "1/3", "0x10", "1e3", "-1", "", "£2.00", "1.234,56"} {
		if _, ok := parseDecimal(in); ok {
			t.Errorf("parseDecimal(%q) accepted", in)
		}
	}
}

func TestFileEntry_ZeroGrossFallsBackToNet(t *testing.T) {
	csvText := "Name;SalePriceExTax;SalePriceIncTax;SalePriceTaxGroupId;Barcode\r\nTea;2.00;0.00;20VAT;5000000000059\r\n"
	h := fileHost(t, []byte(csvText))
	if got := ask(t, filePayload("preview", len(csvText))); !got.OK {
		t.Fatal(got)
	}
	if r := rows(t, h)[0]; r["price_minor"] != float64(240) {
		t.Errorf("IncTax 0.00 with ExTax 2.00 at 20%%: %v", r["price_minor"])
	}
}

func TestFileEntry_LinksPreferBarcodeThenSKUThenName(t *testing.T) {
	csvText := "Name;SalePriceIncTax;Barcode;ArticleCode\r\nA;1;5000000000011;S1\r\nB;1;;S2\r\nC;1;;\r\n"
	h := fileHost(t, []byte(csvText))
	ask(t, filePayload("preview", len(csvText)))
	got := []string{}
	for _, r := range rows(t, h) {
		got = append(got, r["external_id"].(string))
	}
	if strings.Join(got, ",") != "bc:5000000000011,sku:s2,name:c" {
		t.Errorf("links = %v", got)
	}
}

func TestFileEntry_TextCoreWouldRefuseIsCleaned(t *testing.T) {
	csvText := "Name;SalePriceIncTax;Barcode;CategoryId;Description\r\n\"Tea\tGreen\";1;5000000000011;\"Hot\ndrinks\";\"Line one\nline two\x07\"\r\n"
	h := fileHost(t, []byte(csvText))
	if got := ask(t, filePayload("preview", len(csvText))); !got.OK {
		t.Fatal(got)
	}
	r := rows(t, h)[0]
	if r["name"] != "Tea Green" || r["category_path"].([]any)[0] != "Hot drinks" {
		t.Errorf("name/category not cleaned: %v", r)
	}
	if d, _ := r["description"].(string); strings.ContainsRune(d, 7) {
		t.Errorf("description keeps a control character: %q", d)
	}
}

func TestFileEntry_StreamsALargeFile(t *testing.T) {
	// 40 000 lines is far past the 20 000-row cap: the run stops at the cap
	// without reading the rest into memory first.
	var b strings.Builder
	b.WriteString("Name;SalePriceIncTax;Barcode\r\n")
	for i := 0; i < 40_000; i++ {
		fmt.Fprintf(&b, "Item %d;1.00;%013d\r\n", i, i)
	}
	h := fileHost(t, []byte(b.String()))
	got := ask(t, filePayload("preview", b.Len()))
	if got.OK || !strings.Contains(got.Error, "20,000") {
		t.Errorf("answer = %+v", got)
	}
	_ = h
}

func TestFileEntry_QuotaWording(t *testing.T) {
	h := fileHost(t, []byte("Name;SalePriceIncTax\r\nTea;1\r\n"))
	_ = h
	if err := fileSubmitError(plugin.ErrQuota); !strings.Contains(err.Error(), "file") {
		t.Errorf("quota error for a file run = %v", err)
	}
}

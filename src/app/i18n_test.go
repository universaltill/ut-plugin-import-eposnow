//go:build !wasip1

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func inLocale(p map[string]any, locale string) map[string]any {
	p["locale"] = locale
	return p
}

// A German operator gets the summary in German (ut-docs#4648).
func TestReplyInTheOperatorsLanguage(t *testing.T) {
	newHost(t, shopAPI(3), nil)
	got := ask(t, inLocale(payload("commit", "items"), "de"))
	if !got.OK {
		t.Fatalf("%+v", got)
	}
	for _, s := range []string{
		"Epos Now-Konto „Corner Café“:",
		"3 Produkte am Standort „High St“ gelesen",
		"Bestand nicht importiert",
		"1 Nettopreis um die MwSt. erhöht",
	} {
		if !strings.Contains(got.Message, s) {
			t.Errorf("message %q lacks %q", got.Message, s)
		}
	}

	newHost(t, shopAPI(1), nil)
	got = ask(t, inLocale(payload("preview", "items", "stock"), "de-DE"))
	if !got.OK || !strings.HasPrefix(got.Message, "Mit dem Epos Now-Konto „Corner Café“ verbunden. 1 Produkt am Standort „High St“ gelesen.") {
		t.Fatalf("%+v", got)
	}

	newHost(t, shopAPI(2), nil)
	got = ask(t, inLocale(payload("commit", "items"), "tr-TR"))
	if !got.OK || !strings.Contains(got.Message, "2 ürün okundu, konum: “High St”") {
		t.Fatalf("%+v", got)
	}
}

// Refusals and Epos Now failures are answered in the operator's language
// too.
func TestRefusalsInTheOperatorsLanguage(t *testing.T) {
	a := shopAPI(1)
	newHost(t, a, map[string]string{"api_key": ""})
	if got := ask(t, inLocale(payload("commit", "items"), "de")); !strings.HasPrefix(got.Error, "Geben Sie zuerst den Epos Now API-Schlüssel") {
		t.Errorf("no credentials: %+v", got)
	}

	newHost(t, shopAPI(1), nil)
	p := inLocale(payload("apply", "items"), "de")
	if got := ask(t, p); got.Error != "Der Modus muss preview oder commit sein" {
		t.Errorf("bad mode: %+v", got)
	}

	a = shopAPI(1)
	a.fail = map[string]int{"TokenInfo?": 401}
	newHost(t, a, nil)
	if got := ask(t, inLocale(payload("commit", "items"), "de")); !strings.Contains(got.Error, "Zugangsdaten nicht akzeptiert") {
		t.Errorf("auth: %+v", got)
	}

	a = shopAPI(450)
	a.fail = map[string]int{"Product?page=3": 503}
	newHost(t, a, nil)
	if got := ask(t, inLocale(payload("commit", "items"), "de")); !strings.Contains(got.Error, "HTTP 503") || !strings.Contains(got.Error, "nach 3 Wiederholungen") {
		t.Errorf("busy: %+v", got)
	}

	newHost(t, shopAPI(1), map[string]string{"location_id": "99"})
	if got := ask(t, inLocale(payload("commit", "items"), "de")); got.Error != "Epos Now hat keinen Standort 99 (Einstellung location_id)" {
		t.Errorf("no such location: %+v", got)
	}

	newHost(t, shopAPI(3), nil)
	old := submit
	submit = func(string, any) (int, error) {
		return 0, &plugin.Error{Op: "import_rows_submit", Code: plugin.ErrQuota.Code}
	}
	t.Cleanup(func() { submit = old })
	if got := ask(t, inLocale(payload("commit", "items"), "de")); !strings.Contains(got.Error, "(20.000)") {
		t.Errorf("quota: %+v", got)
	}
}

// An empty locale (an older till) or one the plugin has no file for gets
// English; the region and case of the tag do not matter.
func TestLocaleFallback(t *testing.T) {
	for loc, want := range map[string]string{
		"":      "Epos Now account \"Corner Café\": 1 product read",
		"xx":    "Epos Now account \"Corner Café\": 1 product read",
		"pt-BR": "Epos Now account \"Corner Café\": 1 product read",
		"../en": "Epos Now account \"Corner Café\": 1 product read",
		"DE":    "Epos Now-Konto „Corner Café“: 1 Produkt",
		"de_AT": "Epos Now-Konto „Corner Café“: 1 Produkt",
		"en-GB": "Epos Now account \"Corner Café\": 1 product read",
	} {
		newHost(t, shopAPI(1), nil)
		if got := ask(t, inLocale(payload("commit", "items"), loc)); !strings.HasPrefix(got.Message, want) {
			t.Errorf("locale %q: %q, want prefix %q", loc, got.Message, want)
		}
	}
}

// A value from Epos Now is inserted once: a company name that looks like
// a placeholder is not expanded again.
func TestPlaceholderValuesAreNotExpanded(t *testing.T) {
	a := shopAPI(1)
	a.single["TokenInfo"] = `{"CompanyName":"{{location}}","AppLocationId":7}`
	newHost(t, a, nil)
	got := ask(t, payload("commit", "items"))
	if !strings.HasPrefix(got.Message, `Epos Now account "{{location}}": 1 product read at location "High St"`) {
		t.Fatalf("%q", got.Message)
	}
}

// Every reply key the code names exists in every locale (a plural key as
// .one and .other), and every reply key in en.json is used.
func TestReplyKeysInEveryLocale(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Dir(here)
	srcs, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	keyRE := regexp.MustCompile(`"(import_eposnow\.reply\.[a-z_.]+)"`)
	for _, f := range srcs {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRE.FindAllStringSubmatch(string(b), -1) {
			used[m[1]] = true
		}
	}
	if len(used) < 20 {
		t.Fatalf("found only %d reply keys in the code", len(used))
	}
	files, err := filepath.Glob(filepath.Join(dir, "..", "..", "locales", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 { // en, de, tr at least
		t.Fatalf("locale files: %v", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		for k := range used {
			_, one := m[k+".one"]
			_, other := m[k+".other"]
			if _, ok := m[k]; !ok && !(one && other) {
				t.Errorf("%s: no %s", f, k)
			}
		}
		var orphans []string
		for k := range m {
			base := strings.TrimSuffix(strings.TrimSuffix(k, ".one"), ".other")
			if strings.HasPrefix(k, "import_eposnow.reply.") && !used[k] && !used[base] {
				orphans = append(orphans, k)
			}
		}
		sort.Strings(orphans)
		if len(orphans) > 0 {
			t.Errorf("%s: unused reply keys %v", f, orphans)
		}
	}
}

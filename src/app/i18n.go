package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/universaltill/ut-plugin-import-eposnow/locales"
	"github.com/universaltill/ut-plugin-import-eposnow/src/eposnow"
)

// texts is one locale's strings over English (ut-docs#4648): the reply
// answers in the operator's language, and any key the locale lacks, or a
// locale the plugin has no file for, falls back to English.
type texts map[string]string

func readLocale(lang string) map[string]string {
	b, err := locales.FS.ReadFile(lang + ".json")
	if err != nil {
		return nil
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// language is a BCP-47 tag's primary language subtag, lowercased ("de-DE",
// "de_AT", "DE" → "de"); anything that is not 2–3 letters is "".
func language(tag string) string {
	lang, _, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"), "-")
	lang = strings.ToLower(lang)
	if len(lang) < 2 || len(lang) > 3 {
		return ""
	}
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return lang
}

func textsFor(locale string) texts {
	t := texts{}
	for k, v := range readLocale("en") {
		t[k] = v
	}
	if lang := language(locale); lang != "" && lang != "en" {
		for k, v := range readLocale(lang) {
			t[k] = v
		}
	}
	return t
}

// s is key's text with each {{name}} replaced by its value; args are
// name, value pairs. Values are inserted in one pass, so a value that
// looks like a placeholder stays as it is.
func (t texts) s(key string, args ...string) string {
	v, ok := t[key]
	if !ok {
		return key
	}
	if len(args) == 0 {
		return v
	}
	pairs := make([]string, 0, len(args))
	for i := 0; i+1 < len(args); i += 2 {
		pairs = append(pairs, "{{"+args[i]+"}}", args[i+1])
	}
	return strings.NewReplacer(pairs...).Replace(v)
}

// n is the plural key's .one text for a count of 1, else .other, with
// {{count}} set.
func (t texts) n(key string, count int, args ...string) string {
	form := ".other"
	if count == 1 {
		form = ".one"
	}
	return t.s(key+form, append([]string{"count", strconv.Itoa(count)}, args...)...)
}

// replyErr is a refusal the reply names by key.
type replyErr struct {
	key  string
	args []string
}

func (e *replyErr) Error() string { return textsFor("en").s(e.key, e.args...) }

func refuse(key string, args ...string) error { return &replyErr{key: key, args: args} }

// errText is err in t's language.
func (t texts) errText(err error) string {
	var re *replyErr
	var se *eposnow.StatusError
	var ee *eposnow.Error
	switch {
	case errors.As(err, &re):
		return t.s(re.key, re.args...)
	case errors.Is(err, eposnow.ErrAuth):
		return t.s("import_eposnow.reply.error.auth")
	case errors.Is(err, eposnow.ErrSlow):
		return t.s("import_eposnow.reply.error.slow")
	case errors.As(err, &se):
		status := strconv.Itoa(se.Status)
		if se.Retries > 0 {
			return t.n("import_eposnow.reply.error.busy", se.Retries, "status", status, "resource", se.Resource)
		}
		return t.s("import_eposnow.reply.error.status", "status", status, "resource", se.Resource)
	case errors.As(err, &ee):
		switch ee.Code {
		case eposnow.CodeUnreachable:
			detail := ""
			if ee.Err != nil {
				detail = ee.Err.Error()
			}
			return t.s("import_eposnow.reply.error.unreachable", "resource", ee.Resource, "detail", detail)
		case eposnow.CodeUnexpected:
			return t.s("import_eposnow.reply.error.unexpected", "resource", ee.Resource)
		case eposnow.CodeTooLarge:
			return t.s("import_eposnow.reply.error.too_large", "resource", ee.Resource, "limit", strconv.Itoa(ee.Limit))
		case eposnow.CodeTooManyPages:
			return t.s("import_eposnow.reply.error.too_many_pages", "resource", ee.Resource, "limit", strconv.Itoa(ee.Limit))
		case eposnow.CodeTooManyRecords:
			return t.s("import_eposnow.reply.error.too_many_records", "resource", ee.Resource)
		}
	}
	return err.Error()
}

// Package locales embeds the plugin's own locale files, so the import can
// answer in the operator's language (ut-docs#4648). The till reads the
// same files from the installed bundle for the entry and settings labels.
package locales

import "embed"

// FS holds every <lang>.json.
//
//go:embed *.json
var FS embed.FS

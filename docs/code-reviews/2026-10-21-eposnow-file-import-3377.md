# Code review: Epos Now product-export file entry (ut-docs#3377)

**Change (plugin 0.2.0):** a second import entry, `eposnow-file`
(`source: file`, `.csv`, header claim `Name`, `SalePriceExTax`,
`SalePriceIncTax`, `SalePriceTaxGroupId`), parses the Back Office product
export in the import sandbox and submits ADR-0051 rows (ADR-0164 §2).
Streams the file (64 KiB buffer, 32 MiB cap, 20,000 rows, 500 per submit).
Link (`external_id`): barcode, else `ArticleCode`, else name; repeats get
`#2`, `#3`. Price: VAT-inclusive price, else the net price grossed up with
the rate read from a tax group named like `20VAT`/`5VAT`/`12.5%`/`VAT 20%`
(anything else is counted and left to the till). Category = the
`CategoryId` text, description only when it differs from the name, active
= `SellOnTill`. Stock and `RRPrice` are not in the row (no stock column in
the export; RRP is ut-docs#4725). SDK bumped to the first version with
`FakeHost.FileImport`; manifest, validate.sh, locales (en/de/tr), README
and CLAUDE.md updated. The fixture is invented data in the real export's
shape, never the shop's file.

**Tests (TDD, red first):** every mapping above, preview and commit submit
the same rows, BOM and comma-separated files, refusals (not an Epos Now
file, no rows, wrong entry, bad mode, no items entity), tax-group and
decimal parsing, zero gross price falls back to net, link order, cleaning
of control/invisible characters, a 40,000-line file stops at the cap,
quota wording. The API entry's tests pass unchanged.

**Review:** independent Opus 5.5 subagent (code by Sonnet 5.5), read only.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `min_pos_version` 0.34.13 lacks the separator fix, so a `;` file is never offered to the entry | blocker | fixed: 0.34.14, validate.sh floor; the plugin must not be released before that till |
| 2 | `io.ReadAll` of a 32 MiB file ran out of the plugin's 64 MiB memory (crash from 28 MiB) | high | fixed: streamed; re-probed a 32 MiB file under the same limit with a streaming guest, ok |
| 3 | A rename of a barcode-less item makes a new link; README claimed renames update | high | partly fixed: link by `ArticleCode` before name; the rest is inherent (the export has no Id) and the README now says so |
| 4 | API and file entries share one link namespace (core stamps the plugin id) | medium | accepted, documented, card ut-docs#4727 |
| 5 | A bare number in a tax group name read as a rate | medium | fixed: needs `VAT`, `tax` or `%` |
| 6 | `big.Rat` accepted `1,234`, `1/3`, `0x10`, `1e3` as prices | medium | fixed: plain decimals only |
| 7 | Gross `0.00` beside a real net price imported as free | low | fixed: falls back to net |
| 8 | Control/invisible characters make core refuse a row | low | fixed: `cleanText` |
| 9 | Quota error worded for the API run | low | fixed |
| 10 | `catimport.Parse` BOM before a quoted header | low, pre-existing | fixed in ut-docs#4726 |

**Verified:** `gofmt`, `go vet` (native and wasip1), `go test ./...`,
`scripts/build.sh`, `validate.sh`, `guard-plugin-i18n.sh`, `shellcheck`,
`check-version-bump.test.sh`; the manifest's header claim against the
fixture through the till's real `HeaderRow`/`HeadersClaim`.

**Not verified:** a live till run (the till's file-rows import page with a
real upload), the real export's encoding (read as UTF-8; if Epos Now writes
Windows-1252, a `£` or accent would be refused per row by core), the real
tax-group names beyond `20VAT`, other value formats (`SellOnTill`), and
ut-cloud's import sandbox.

**Verdict:** safe to merge, not to release until till 0.34.14 (the
separator fix) is out. Deferred: ut-docs#4727.

## Rebase onto main (ut-docs#4648)

Main localised every reply (`import_eposnow.reply.*`, `textsFor`). The file entry now answers the same way: its read summary, notes and refusals are `import_eposnow.reply.file.*` keys in en/de/tr (plural forms, `refuse`/`errText`), replacing the removed `plural` helper. SDK pin taken from main.

## Independent review 2026-11-03 (Opus 5.5; code by Sonnet 5.5)

The 2026-10-21 table above was already a different-model review (Opus
subagent on Sonnet code); the rebase commit (`reply.file.*` keys) was
not reviewed until now. This pass covers the whole PR at the rebased head.

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | A cell not valid UTF-8 (a Windows-1252 file) was silently stripped byte by byte: `Café £` imported as `Caf`, and the name link changed with it. The earlier record's "core refuses it per row" was wrong; `cleanText` hid it | high | fixed TDD: such a cell is decoded as Windows-1252 (`TestFileEntry_Windows1252CellsAreDecoded`, red first, red again with the fix reverted); README says so |
| 2 | No test held the ≤500-rows-per-submit batching (mutant `len(buf)==batchSize` → never survived the suite) | medium | fixed: `TestFileEntry_SubmitsInBatchesOf500` (1,201 rows), fails under that mutant |
| 3 | Unparsable price (`£2.00`, `-1`, `1,234.50`) leaves `price_minor` empty with no note in the plugin's answer | low | accepted: core's preview shows each such row as `price_invalid` (`catsync/row.go`), so it is not silent and nothing applies for it |
| 4 | Two lines with the same name and no barcode/ArticleCode link by file order (`#2`); reordering the export swaps their links | low | accepted, inherent (no Id in the export); duplicate barcodes/SKUs are surfaced by core as `barcode_duplicate`/`sku_duplicate` |
| 5 | Formula-looking names (`=…`) stored as text | info | not this plugin's surface (no CSV written here); core's exports own escaping |

**Checked, no finding:** i18n — every `import_eposnow.reply.file.*` key the
code uses exists in en/de/tr, key sets equal, placeholders identical, de/tr
wording and plural forms read correctly; manifest 0.2.0 + `min_pos_version`
0.34.14 match validate.sh; exact permission set unchanged (file entry adds
no egress); no secrets or row content logged; sandboxed entry never touches
Epos Now. Mutation checks (cp backup, restored): gross-up rate, SellOnTill,
duplicate suffix, zero-gross fallback, row cap, bad-barcode count all
killed; the BOM `head[3:]` mutant is equivalent (BOM bytes are never
separators).

**Gate:** gofmt, vet (native + wasip1), `go test ./...`, build.sh,
validate.sh, guard-plugin-i18n.sh, shellcheck, check-version-bump.test.sh —
all pass. No version bump needed (0.2.0 unreleased, still in this PR).

**Verdict:** safe to merge once these fixes are pushed; release still waits
on till 0.34.14.

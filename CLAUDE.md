# ut-plugin-import-eposnow — rules for working in this repo

WASM `import` plugin (ADR-0009 one-repo-per-plugin, ADR-0001, ADR-0121)
in Go on the official guest SDK `github.com/universaltill/universal-till/sdk/plugin`
— never hand-rolled `//go:wasmimport` lines. One API-source import entry
(ADR-0161, ut-docs#4430): on `import.requested.ask` it reads Epos Now's V4
API and hands ADR-0051 item rows back with `plugin.ImportRowsSubmit`; core
(`internal/catsync`) plans or applies them. `src/eposnow` is the read-only
API client, `src/app` the handler and the mapping, `src/main.go`
`plugin.Run`.

- **Never connect to a real shop's Epos Now account** from development or
  tests (owner, 2026-10-15). Tests use spec-shaped fixtures only — field
  names and types from the V4 Swagger `definitions`, never invented
  fields; live checks only against the developer/test account
  (ut-docs#4436).
- **Rows match core's parser exactly** (`internal/catsync/row.go`
  `wireRow`): same JSON names, no `source` (core stamps it), ≤500 rows per
  submit, 20,000 per run. Core validates rows; ordinary Epos Now data core
  would refuse (a free-text barcode, a blank category level, a VAT rate
  out of range, an over-long name) is dropped or cut and counted in the
  reply, so the product still imports.
- **Money**: prices convert through `big.Rat` from the decimal the API
  sent, rounded half-up once (gross-up included). Never float arithmetic.
- **Secrets**: `api_key`/`api_secret` are `type: "secret"` (ADR-0082),
  read per event with `plugin.SettingsGet`. Nothing logs: no credential,
  no response body, no row content. Errors name the status, never the body.
- **Egress**: `plugin.HTTP` to `net:api.eposnowhq.com` only. 401/403 →
  "check the API key and secret"; 429/5xx → 3 retries with backoff
  (Retry-After on 429, 30 s budget per request, 4-minute run deadline);
  any failure answers `ok:false`, so core applies nothing.
- **Settings are a contract with installed shops**: id, keys and defaults
  never change (`scripts/validate.sh` enforces them, and the exact
  permission set).
- **Locales**: every key in `locales/en.json`, `de.json`, `tr.json`,
  translated in the same change (`scripts/guard-plugin-i18n.sh`).
- **Tests run natively on `plugin.FakeHost`** (`*_test.go` are
  `//go:build !wasip1`). TDD: failing test first.

## Before committing

`gofmt -l .` (no output), `go vet ./...`, `GOOS=wasip1 GOARCH=wasm go vet
./...`, `go test ./...`, `scripts/build.sh`, `scripts/validate.sh`,
`scripts/guard-plugin-i18n.sh`, `shellcheck scripts/*.sh`,
`scripts/check-version-bump.test.sh`. Feature branch; review record in
`docs/code-reviews/<date>-<topic>.md`; then merge.

## Releases

Release = tag `v<version>` matching `manifest.json`, cut by
`auto-tag-release.yml` on merge. **Any PR that changes what ships —
`manifest.json`, `README.md`, `LICENSE`, `locales/`, anything under `src/`,
`go.mod`/`go.sum`, `scripts/build.sh` or `scripts/package.sh` — must bump `manifest.json`'s
`version` in the same PR** (ut-docs#1940), enforced by
`scripts/check-version-bump.sh`.

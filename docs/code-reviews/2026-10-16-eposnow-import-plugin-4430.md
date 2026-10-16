# Review: Epos Now import plugin, first version (ut-docs#4430)

- **Date:** 2026-10-16
- **Card:** ut-docs#4430 (epic ut-docs#4426)
- **Built by:** Opus 5.5
- **Reviewed by:** Fable 5.1 (independent, read-only)
- **Depends on:** universal-till#1978 (ADR-0161, `import_rows_submit`, SDK `ImportRowsSubmit`)

## What shipped

A new `import` plugin. It reads a shop's catalogue from the Epos Now V4 API using the shop's own API key and secret, and hands the rows to the till's import (ADR-0161 `source: "api"`). Preview and commit both go through the till's own import screen.

- **API client:** `src/eposnow` is built from the official V4 Swagger (`https://developer.eposnowhq.com/Docs/v4/json`). It covers:
  - Basic auth; responses are read through `http_open` streams.
  - Paging that stops at the first empty page, with a hard cap of 500 pages and 100,000 records per list.
  - Retries on 429 and 5xx only, with 1/2/4 s backoff. Retry-After is honoured on a 429. Each request has a 30 s total wait budget.
  - A run deadline of 4 minutes, inside the host's 5-minute timeout.
- **Mapping:** `src/app` maps each product to one import row. It covers price (net to gross through `big.Rat`), VAT (per location, then global, lowest priority), stock (the sum of batches at the chosen location), category path (up to 4 levels), one barcode, and active state.
- **Manifest:**
  - Permissions: `events:receive`, `http:stream`, `net:api.eposnowhq.com`, `items:write`, `stock:write`.
  - Settings: secret settings `api_key` and `api_secret`, plus `location_id`.
  - Version 0.1.0.
- **Locales:** en, de, tr.

No Epos Now server was contacted while building or reviewing it. The pilot shop's live account stays untouched (owner rule), and live testing waits for the Epos Now developer account (ut-docs#4436).

## Findings and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| B1 | Blocker | `go.mod` replaced the SDK with a local worktree path, and there was no `go.sum`; CI could not build | **Fixed** at merge time: pinned to the SDK pseudo-version of the universal-till#1978 merge commit, `go mod tidy`, `go.sum` committed |
| M1 | Major | A barcode that fails core's pattern, a blank category level or an over-long name made core reject the whole product | **Fixed:** bad barcodes are dropped and counted, blank levels end the path, names are cut at a rune boundary; the reply reports each count |
| m1 | Minor | An oversized page surfaced as a "network" error, because the host's 8 MiB quota fired first | **Fixed:** `MaxPageBytes = 8<<20 - 1`; the host's `ErrQuota` maps to the same "page too large" error |
| m2 | Minor | No deadline: a slow API let the host kill the ask with no message | **Fixed:** 4-minute run deadline with its own message; the clock can be injected for tests |
| m3 | Minor | An out-of-range VAT rate was still sent, so core rejected the row | **Fixed:** dropped and counted |
| m4 | Minor | `listAll` was bounded only by pages × 8 MiB | **Fixed:** 100,000-record cap |
| m5 | Minor | Test gaps: empty catalogue, the quota branch, and a loose assertion | **Fixed:** tests added and the assertion made exact |
| Nits | — | Content-Type on GET, Retry-After ignored, unused TokenInfo fields, LICENSE year, template fixture name, DE "API-Geheimnis" | **Fixed** |

## Verified beyond automated tests

The reviewer checked these by hand:
- **Spec:** every field name and type used matches the V4 Swagger definitions.
- **Rows:** the JSON matches core's `wireRow`.
- **Secrets:** read through `settings_get` and never logged or echoed in errors (a test asserts no leak).
- **Permissions:** the set is pinned by `validate.sh`.

## Gate

All of these pass: `gofmt -l`, `go vet` (host and wasip1), `go test -count=1 ./...`, `scripts/build.sh`, `scripts/validate.sh`, `scripts/guard-plugin-i18n.sh`, `shellcheck scripts/*.sh` and `scripts/check-version-bump.test.sh`.

## Deferred

- **Reply language:** the reply message is English only, because the ask payload carries no locale. It needs a core seam, so it is a card, not a plugin change.
- **Live test:** against the Epos Now developer account (ut-docs#4436). The base URL is `api.eposnowhq.com` per Epos Now's docs; the Swagger's `host` field says `developer.eposnowhq.com`.
- **`min_pos_version` 0.34.8:** v0.34.7 was tagged before universal-till#1978 merged (dec32007f), so the first till with API-source imports is 0.34.8.

## Release

- The merge auto-tags `v0.1.0`, and `release.yml` uploads it to the marketplace at `cloud.universaltill.com`. The repo secrets `MARKETPLACE_BASE_URL` and `MARKETPLACE_UPLOAD_TOKEN` were set from Key Vault and never printed.
- The repo variable `AUTO_APPROVE` is deliberately **not** set. The release waits for a human approval in the marketplace, so no till can install it until the live check against the Epos Now developer account (ut-docs#4436) passes.

## Verdict

Safe to merge once universal-till#1978 is on main and B1's pin is in.

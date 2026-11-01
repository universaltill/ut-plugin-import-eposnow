# Review — replies in the operator's language (ut-docs#4648)

**Date:** 2026-11-01 · **Branch:** `feat/4648-reply-in-operator-language` ·
**Author lane:** `lane:cloud-41` (Opus) · **Reviewer:** independent Sonnet
subagent (different model from the author), one round.

## What shipped

- The import reads `locale` from the ask (`plugin.ImportRequest.Locale`).
  It answers from the plugin's own `locales/<lang>.json`, which
  `locales/embed.go` compiles into the wasm. The primary subtag is used
  (`de-DE`, `de_AT` and `DE` all mean `de`). An empty or unknown locale gets
  English.
- Every reply moved to `import_eposnow.reply.*` keys in en/de/tr: the
  preview and commit summaries, the plural notes (`.one`/`.other`), each
  refusal, and the `chooseLocation`/`submitError` errors. So did the API
  client's failures. `eposnow.Error{Code, Resource, Limit, Err}` replaces
  the client's `fmt.Errorf` texts; its `Error()` keeps the same English, so
  the client tests are unchanged.
- `{{name}}` placeholders are filled in one pass (`strings.NewReplacer`).
  A value from Epos Now that looks like a placeholder is never expanded.
- The SDK moves to the universal-till `main` pseudo-version
  (`v1.1.1-0.20261101063128-a5bdead2b7f1`), because tag `sdk/plugin/v1.1.0`
  predates `Locale`. The only SDK change between the two pins is that
  field, plus a `channel` subpackage this plugin does not import.
- `scripts/package.sh` ships `locales/*.json` only, so `embed.go` stays out
  of the bundle. The version goes to 0.1.1. The README says which languages
  answer.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `textsFor` would panic on a nil en map if en.json failed to parse | **Fixed**: the result is built into a fresh map |
| 2 | minor | Locale JSON is re-parsed per ask; `{{detail}}` in "unreachable" is the host's English text | Accepted: at most a few parses per ask. The detail is technical diagnostics with no translation source |
| 3 | minor | Joiners `", "`, `" ("`, `"; "` are code, not keys | Accepted for en/de/tr. A future RTL/CJK pack would need a key |
| 4 | minor | Key-coverage test read files by cwd-relative path and pinned exactly 3 locales | **Fixed**: the path comes from `runtime.Caller`, and the check is `< 3` |
| 5 | nit | German "Id" should be "ID"; `0–100 %` should use a non-breaking space | **Fixed** |

The reviewer checked and found sound: the `errText` precedence, that no
English reply is left in `src/app`, placeholder injection, tag
normalisation (`../en` cannot traverse), the SDK diff, `package.sh` under
`set -euo pipefail`, and the German/Turkish grammar and terminology.

## Verified

- **TDD re-verified by the orchestrator.** The new tests ran in a separate
  worktree against `main`'s `app.go`, `client.go` and locales. They failed
  for the claimed reasons: English text for `de`, "1 products", and the
  locale ignored. They pass on the branch.
- Full gate on the final tree, all green: `gofmt -l .` (clean), `go vet`
  (native and wasip1), `go test ./...`, `scripts/build.sh`,
  `scripts/validate.sh`, `scripts/guard-plugin-i18n.sh` (46 keys × 3),
  `shellcheck scripts/*.sh`, `scripts/check-version-bump.test.sh`,
  `scripts/package.sh`. The tarball lists `locales/{de,en,tr}.json` and no
  `.go` file.
- Nobody ran it on a till. The reply text is shown as-is by the till's
  import screen, and the FakeHost tests drive the same dispatch path.

## Deferred

- Re-pin `sdk/plugin` to the next tag that carries `Locale` once one
  exists.
- Open PR #3 (file import, ut-docs#3377) adds its own English replies in
  `src/app/file.go`. That PR overlaps this one in `manifest.json` (version),
  `go.mod` and three locale lines. Whichever merges second rebases and
  moves those replies to keys.

**Verdict:** safe to merge.

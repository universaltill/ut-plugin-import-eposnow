#!/usr/bin/env bash
# Manifest sanity for the Epos Now import WASM plugin (ut-docs#4430):
# marketplace-required fields, the wasm runtime and its built module, the
# API-source import entry core dispatches (ADR-0161), least-privilege
# permissions, and the settings contract (secret credentials, ADR-0082).
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import json, os, re, sys
m = json.load(open("manifest.json"))
errs = []
if m.get("id") != "com.universaltill.import-eposnow": errs.append("id must stay com.universaltill.import-eposnow (installed shops' settings and source links)")
for f in ("name", "version", "description"):
    if not m.get(f): errs.append(f"missing {f}")
if not re.match(r'^\d+\.\d+\.\d+', m.get("version", "")): errs.append("bad version")
if m.get("canonical_type") != "import": errs.append("canonical_type must be 'import'")
if m.get("device_arch") != "any": errs.append("device_arch must be 'any'")
if m.get("runtime") != "wasm": errs.append("runtime must be 'wasm' (ADR-0001)")
ep = (m.get("entrypoint") or "").lstrip("./")
if not ep.endswith(".wasm"): errs.append("wasm runtime needs a .wasm entrypoint")
elif not os.path.isfile(ep): errs.append(f"module not found: {ep} (run scripts/build.sh)")
if not m.get("locales"): errs.append("missing locales")
# An older till fails signature verification on the entry's `source`
# field: name the first till with API-source imports (ADR-0161).
mpv = tuple(int(x) for x in re.findall(r'\d+', m.get("min_pos_version", "0"))[:3])
if mpv < (0, 34, 8): errs.append("min_pos_version must be >= 0.34.8 (first till with API-source imports, ADR-0161)")

# Least privilege: exactly what the import uses — the hook, the one API
# host, and the two write grants core checks (<entity>:write).
want_perms = ["events:receive", "http:stream", "net:api.eposnowhq.com", "items:write", "stock:write"]
if sorted(m.get("permissions", [])) != sorted(want_perms):
    errs.append(f"permissions must be exactly {want_perms}")

entries = m.get("entries", [])
if len(entries) != 1: errs.append("exactly one entry (the Epos Now import)")
else:
    e = entries[0]
    if e.get("type") != "import" or e.get("key") != "eposnow": errs.append("entry must be {type: import, key: eposnow}")
    if e.get("source") != "api": errs.append("entry source must be 'api' (ADR-0161)")
    if e.get("file_formats"): errs.append("an api source declares no file_formats")
    if e.get("entities") != ["items", "stock"]: errs.append("entry entities must be [items, stock]")
    if not e.get("label"): errs.append("entry needs a label")

en = json.load(open("locales/en.json"))
for e in entries:
    if e.get("label") and e["label"] not in en: errs.append(f"entry label {e['label']!r} missing from locales/en.json")
settings = {s["key"]: s for s in m.get("settings", [])}
if set(settings) != {"api_key", "api_secret", "location_id"}: errs.append("settings must be api_key, api_secret, location_id")
for k in ("api_key", "api_secret"):
    if settings.get(k, {}).get("type") != "secret": errs.append(f"{k} must be type secret (ADR-0082)")
for k, s in settings.items():
    if s.get("default_value") != "": errs.append(f"setting {k} defaults to empty")
    if not s.get("label"): errs.append(f"setting {k} needs a label")
    for f in ("label", "help"):
        if s.get(f) and s[f] not in en: errs.append(f"setting {k} {f} {s[f]!r} missing from locales/en.json")
if settings.get("location_id", {}).get("type"): errs.append("location_id stays plain text")

hooks = {h.get("event") for h in m.get("hooks", [])}
if hooks != {"import.requested.ask"}: errs.append("the one hook is import.requested.ask")
if errs:
    print("FAIL: " + "; ".join(errs)); sys.exit(1)
print(f"ok {m['id']} v{m['version']}")
PY

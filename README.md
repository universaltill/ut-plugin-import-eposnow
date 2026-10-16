# Epos Now import (`com.universaltill.import-eposnow`)

Moves a shop from **Epos Now** to Universal Till over the Epos Now V4 API:
products, their categories, VAT rates and stock levels. Run a **preview**
first, then **import**. Run it again whenever you like — each product
stays linked to its Epos Now record, so a changed price or name updates
the item instead of adding a duplicate. The plugin only *reads* from Epos
Now; it never changes anything there.

Needs Universal Till **0.34.8** or later (API-source imports, ADR-0161).

> **Status:** built from the published Epos Now V4 API specification and
> tested against spec-shaped data. It has not yet been confirmed against a
> live Epos Now account (ut-docs#4436).

## Setting up

1. In your Epos Now Back Office, create an **API device** and note its
   key and secret.
2. In Universal Till: **Plugins → Epos Now import → Settings**, enter
   them:

| key | default | meaning |
|---|---|---|
| `api_key` | *(empty)* | the API device's key. Stored masked and encrypted. |
| `api_secret` | *(empty)* | the API device's secret. Stored masked and encrypted. |
| `location_id` | *(empty)* | the Epos Now location whose VAT rates and stock you want. Empty = the first location Epos Now lists. |

3. Run the import from the till's import screen (the API-source import
   screen is ut-docs#4431): *Preview* checks the connection — it names
   your Epos Now account and location — and shows what would change;
   *Import* applies it.

## What comes across

| Epos Now | Universal Till |
|---|---|
| Product `Id` | the link to the Epos Now record |
| `Name` | name (longer than 200 characters: shortened, and counted) |
| `Sku`, else `ArticleCode` | SKU |
| `Barcode` | barcode (only digits, letters and `-`, up to 64 characters — the till's barcode rule; any other is dropped and counted, the product still imports) |
| `SalePrice` | price, rounded half-up to the penny/cent |
| `CategoryId` and its parent categories | category path (up to 4 levels; deeper trees keep the 4 levels nearest the product; a category with a blank name ends the path, keeping the levels above it; a level over 100 characters is shortened) |
| the sale price's tax group | VAT rate: the rate for your location, else the rate for all locations; the lowest priority wins (a rate outside 0–100% is dropped and counted) |
| not archived and sold on the till | active |
| stock batches at the location | stock (set through a stock movement) |

A price Epos Now stores **without VAT** is converted to a VAT-inclusive
price with that product's VAT rate; the import message says how many.
Variants import as separate items for now. Customers, sales history and
images are not imported.

The till applies at most 20,000 products per run. Each Epos Now page may
be up to 8 MiB, and a reference list (categories, VAT groups, locations,
stock) up to 100,000 records.

If Epos Now is busy (HTTP 429 or 5xx) the plugin retries up to three
times, waiting 1, 2 and 4 seconds — or as long as a 429's `Retry-After`
asks — at most 30 seconds per request. A run stops itself after about 4
minutes with "Epos Now is slow — try again later", before the till's
5-minute limit for an import; any failure imports nothing.

## What the plugin may do (permissions)

| permission | why |
|---|---|
| `events:receive` | answer the till's import request |
| `net:api.eposnowhq.com` | read your Epos Now data — and no other host |
| `items:write` | hand item rows to the till (the till applies them) |
| `stock:write` | include stock levels |

The key and secret go only to `api.eposnowhq.com`. Nothing is logged:
neither the credentials nor your product data.

## Development

Go on the guest SDK `github.com/universaltill/universal-till/sdk/plugin`.

```sh
go test ./...          # native, on plugin.FakeHost and a fake Epos Now API
scripts/build.sh       # bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm)
scripts/validate.sh    # manifest checks
scripts/package.sh     # dist/<id>_<version>_universal.tar.gz
```

`go.mod` temporarily points the SDK at a local checkout until
universal-till#1978 (the SDK's `ImportRowsSubmit`) merges.

## License

MIT — see [LICENSE](LICENSE).

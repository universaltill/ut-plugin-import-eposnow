# Epos Now import (`com.universaltill.import-eposnow`)

Moves a shop from **Epos Now** to Universal Till two ways: by uploading the
**product export** you download from the Epos Now Back Office (nothing to
edit, no key needed), or over the Epos Now V4 API (products, categories, VAT
rates and stock levels). Run a **preview** first, then **import**. Run it again whenever you like — each product
stays linked to its Epos Now record, so a changed price or name updates
the item instead of adding a duplicate. The plugin only *reads* from Epos
Now; it never changes anything there.

Needs Universal Till **0.34.14** or later (API-source imports, ADR-0161;
file imports, ADR-0164; `;`-separated exports, ut-docs#4726).

> **Status:** built from the published Epos Now V4 API specification and
> tested against spec-shaped data. It has not yet been confirmed against a
> live Epos Now account (ut-docs#4436).

## Importing the product export (no key needed)

1. In your Epos Now Back Office, export your products (the CSV download,
   one page at a time if Epos Now splits it) and keep the file as it is.
2. In Universal Till open the import screen and upload the file. The till
   recognises it as an Epos Now export from its column names
   (`Name`, `SalePriceExTax`, `SalePriceIncTax`, `SalePriceTaxGroupId`) and
   shows this plugin's preview. *Import* applies it.

The export has no Id column, so each product is linked by its barcode, else
by its `ArticleCode`, else by its name. Run it again and a changed price
updates the item instead of adding a duplicate; a **rename** keeps the link
only for a product that has a barcode or an `ArticleCode` (a renamed product
with neither is added as a new one), and two products with the same name
and neither are told apart by their order in the file. Use either this file
import or the API import for a shop, not both: each links products its own
way, so the second would not recognise the first's products
(ut-docs#4727). What comes across: name, category
(`CategoryId` holds the category's name), barcode, the VAT-inclusive price
(a net-only price is grossed up with the product's rate), the VAT rate read
from a tax group named like `20VAT`, `5VAT` or `0VAT` (any other group name
is counted and left to the till's default), `ArticleCode` as the SKU (the
till generates one when it is empty), a description that differs from the
name, and "sell on till" as active. **Not in this file:** stock (Epos Now
exports it separately) and the recommended retail price `RRPrice`
(ut-docs#4725). Files use `;`, `,` or tab as separator; up to 20,000
products and 32 MiB. Prices must be plain decimals (`8.99`, `8,99`); a
name, category or description is cleaned of line breaks and invisible
characters. The file is read as UTF-8.

The file entry runs in the till's import sandbox: it can read the uploaded
file and hand back rows, nothing else (no network, no settings).

## Setting up the API import

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

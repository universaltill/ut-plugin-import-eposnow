// Epos Now import (com.universaltill.import-eposnow): reads products,
// categories, VAT and stock from an Epos Now account over its V4 API and
// hands them to the till as item rows (ADR-0161, ut-docs#4430). A WASI
// module the till runs in-process (ADR-0001, ADR-0121); build with
// GOOS=wasip1 GOARCH=wasm (scripts/build.sh). The logic lives in src/app
// and src/eposnow, unit-tested natively on the SDK's FakeHost.
package main

import (
	"github.com/universaltill/universal-till/sdk/plugin"

	"github.com/universaltill/ut-plugin-import-eposnow/src/app"
)

func main() {
	plugin.Run(app.Handlers())
}

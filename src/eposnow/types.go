package eposnow

// The Epos Now V4 records this plugin reads. Field names and types are the
// official Swagger 2.0 spec's `definitions` (developer.eposnowhq.com
// /Docs/v4/json) — only the fields the import uses; encoding/json ignores
// the rest. Nullable fields are pointers: the spec marks none required.

// Product is definitions.Product.
type Product struct {
	ID                  *int64   `json:"Id"`
	Name                *string  `json:"Name"`
	SalePrice           *float64 `json:"SalePrice"`
	IsSalePriceIncTax   *bool    `json:"IsSalePriceIncTax"`
	CategoryID          *int64   `json:"CategoryId"`
	Barcode             *string  `json:"Barcode"`
	SalePriceTaxGroupID *int64   `json:"SalePriceTaxGroupId"`
	Sku                 *string  `json:"Sku"`
	ArticleCode         *string  `json:"ArticleCode"`
	SellOnTill          *bool    `json:"SellOnTill"`
	IsArchived          *bool    `json:"IsArchived"`
}

// Category is definitions.Category.
type Category struct {
	ID       *int64  `json:"Id"`
	ParentID *int64  `json:"ParentId"`
	Name     *string `json:"Name"`
}

// TaxGroup is definitions.TaxGroup.
type TaxGroup struct {
	ID       *int64         `json:"Id"`
	Name     *string        `json:"Name"`
	TaxRates []TaxGroupRate `json:"TaxRates"`
}

// TaxGroupRate is definitions.TaxGroupRate.
type TaxGroupRate struct {
	TaxGroupID *int64   `json:"TaxGroupId"`
	LocationID *int64   `json:"LocationId"`
	Priority   *int64   `json:"Priority"`
	Percentage *float64 `json:"Percentage"`
}

// Location is definitions.Location.
type Location struct {
	ID   *int64  `json:"Id"`
	Name *string `json:"Name"`
}

// ProductStock is definitions.IProductStock (the
// ProductStock/Location/{locationId} list's item type).
type ProductStock struct {
	ProductID           *int64              `json:"ProductId"`
	LocationID          *int64              `json:"LocationId"`
	ProductStockBatches []ProductStockBatch `json:"ProductStockBatches"`
}

// ProductStockBatch is definitions.IProductStockBatch.
type ProductStockBatch struct {
	CurrentStock *int64 `json:"CurrentStock"`
}

// TokenInfo is definitions.TokenInfo.
type TokenInfo struct {
	CompanyName *string `json:"CompanyName"`
}

// Str dereferences a nullable string ("" for null).
func Str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

package settings

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"
)

// DefaultProductName is the fallback product title when branding settings have not been customized.
const DefaultProductName = "Yearning"

// PublicBranding carries the public branding fields exposed to unauthenticated and authenticated clients.
type PublicBranding struct {
	ProductName      string `json:"product_name"`
	LoginDescription string `json:"login_description"`
}

func parsePublicBranding(raw []byte) PublicBranding {
	out := PublicBranding{
		ProductName: DefaultProductName,
	}
	if len(raw) == 0 {
		return out
	}
	var payload struct {
		ProductName      string `json:"product_name"`
		LoginDescription string `json:"login_description"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return out
	}
	if name := strings.TrimSpace(payload.ProductName); name != "" {
		out.ProductName = name
	}
	out.LoginDescription = strings.TrimSpace(payload.LoginDescription)
	return out
}

// ReadPublicBranding loads product_name and login_description from system_settings, returning defaults when absent.
func ReadPublicBranding(ctx context.Context, db *gorm.DB) PublicBranding {
	if db == nil {
		return PublicBranding{ProductName: DefaultProductName}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var row struct {
		Settings json.RawMessage
	}
	if err := db.WithContext(ctx).Raw(
		`select settings from system_settings where namespace = ?`,
		storageNamespace(NsBranding),
	).Scan(&row).Error; err != nil {
		return PublicBranding{ProductName: DefaultProductName}
	}
	return parsePublicBranding(row.Settings)
}

package application

import (
	"context"

	"github.com/cookieY/YearningX/backend/internal/database"
)

// policyCatalogSource is intentionally narrower than tool.FactSource. Raw
// catalog identities are authoritative privacy input and must never become a
// Provider-visible tool result.
type policyCatalogSource interface {
	PolicyCatalog(context.Context, []string) (database.PolicyCatalog, error)
}

func (f *factSource) PolicyCatalog(
	ctx context.Context,
	schemas []string,
) (database.PolicyCatalog, error) {
	if f == nil || f.adapter == nil || f.pool == nil ||
		f.pool.Purpose() != database.PurposeReview {
		return database.PolicyCatalog{}, ErrFactSourceUnavailable
	}
	return database.ReadPolicyCatalog(ctx, f.adapter, f.pool, schemas)
}

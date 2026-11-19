package application

import (
	"context"
	"errors"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/database"
)

func TestPolicyCatalogSourceIsNarrow(t *testing.T) {
	var _ policyCatalogSource = (*factSource)(nil)

	for _, source := range []*factSource{
		nil,
		{},
		{adapter: policyCatalogNilAdapter{}},
	} {
		if _, err := source.PolicyCatalog(context.Background(), []string{"app"}); !errors.Is(err, ErrFactSourceUnavailable) {
			t.Fatalf("source %#v error = %v", source, err)
		}
	}
}

type policyCatalogNilAdapter struct {
	database.Adapter
}

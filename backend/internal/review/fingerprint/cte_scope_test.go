package fingerprint

import (
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

func TestNonRecursiveCTEBodyCannotSeeItself(t *testing.T) {
	res := analyzeObjects(t,
		"WITH x AS (SELECT * FROM x) SELECT * FROM x",
		privacy.NewRunMapper())

	tables := []ObjectReference{}
	ctes := []ObjectReference{}
	for _, object := range res.Objects {
		switch object.Kind {
		case "table":
			tables = append(tables, object)
		case "cte":
			ctes = append(ctes, object)
		}
	}
	if len(tables) != 1 {
		t.Fatalf("non-recursive CTE body physical table objects = %+v, want one", tables)
	}
	if len(ctes) < 2 {
		t.Fatalf("CTE definition and outer reference = %+v, want at least two", ctes)
	}
}

func TestRecursiveCTEBodyCanSeeItself(t *testing.T) {
	res := analyzeObjects(t,
		"WITH RECURSIVE x AS (SELECT * FROM x) SELECT * FROM x",
		privacy.NewRunMapper())
	for _, object := range res.Objects {
		if object.Kind == "table" {
			t.Fatalf("recursive CTE self-reference became a physical table: %+v", object)
		}
	}
}

func TestPrecedingNonRecursiveCTEIsVisibleToLaterCTE(t *testing.T) {
	res := analyzeObjects(t,
		"WITH x AS (SELECT * FROM customer_orders), y AS (SELECT * FROM x) SELECT * FROM y",
		privacy.NewRunMapper())
	tables := 0
	for _, object := range res.Objects {
		if object.Kind == "table" {
			tables++
		}
	}
	if tables != 1 {
		t.Fatalf("physical table objects = %+v, want only customer_orders", res.Objects)
	}
}

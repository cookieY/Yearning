package fingerprint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cookieY/YearningX/backend/internal/review/privacy"
)

type classBGoldenDoc struct {
	ContractVersion int                 `json:"contract_version"`
	Cases           []classBGoldenEntry `json:"cases"`
}

type classBGoldenEntry struct {
	ID                     string `json:"id"`
	Engine                 string `json:"engine"`
	Mode                   string `json:"mode"`
	SQL                    string `json:"sql"`
	ExpectKind             string `json:"expect_kind"`
	ExpectQuality          string `json:"expect_quality"`
	ExpectMetadataComplete bool   `json:"expect_metadata_complete"`
}

func TestClassBDialectFingerprintGoldenCorpus(t *testing.T) {
	path := filepath.Join(corpusDir(t), "class_b_dialect_golden.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read class B golden corpus: %v", err)
	}
	var doc classBGoldenDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal class B golden corpus: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("class_b_dialect_golden.json must not be empty")
	}

	eng, err := NewEngine(testSigner(), Options{})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	seenCrossDBRename := false
	for _, tc := range doc.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			res, err := eng.Analyze(Profile{Engine: tc.Engine, Mode: tc.Mode}, []byte(tc.SQL), privacy.NewRunMapper())
			if err != nil {
				t.Fatalf("Analyze(%s/%s): %v", tc.Engine, tc.Mode, err)
			}
			if res.StatementKind != tc.ExpectKind {
				t.Fatalf("StatementKind = %q, want %q", res.StatementKind, tc.ExpectKind)
			}
			if res.Quality != tc.ExpectQuality {
				t.Fatalf("Quality = %q, want %q", res.Quality, tc.ExpectQuality)
			}
			if res.MetadataContextComplete != tc.ExpectMetadataComplete {
				t.Fatalf("MetadataContextComplete = %v, want %v", res.MetadataContextComplete, tc.ExpectMetadataComplete)
			}
			if tc.ID == "def21_c1cd3c0_mysql_cross_db_rename_table" {
				seenCrossDBRename = true
			}
		})
	}
	if !seenCrossDBRename {
		t.Fatal("missing DEF-21 c1cd3c0 cross-DB RENAME TABLE case")
	}
}

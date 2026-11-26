package rollback

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type classBRollbackGoldenDoc struct {
	ContractVersion int                    `json:"contract_version"`
	Cases           []classBRollbackSample `json:"cases"`
}

type classBRollbackSample struct {
	ID                string `json:"id"`
	Engine            string `json:"engine"`
	Mode              string `json:"mode"`
	SQL               string `json:"sql"`
	RollbackSupported bool   `json:"rollback_supported"`
	RollbackKind      string `json:"rollback_kind"`
	RollbackReason    string `json:"rollback_reason"`
}

func TestClassBRollbackSQLGoldenCorpus(t *testing.T) {
	corpusPath := filepath.Join("..", "..", "..", "..", "testdata", "fingerprint", "class_b_dialect_golden.json")
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read class B golden corpus: %v", err)
	}
	var doc classBRollbackGoldenDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal class B golden corpus: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("class_b_dialect_golden.json must not be empty")
	}

	seenCrossDBRename := false
	for _, tc := range doc.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			parsed, err := AnalyzeStatement(tc.Mode, "app_db", tc.SQL)
			if tc.RollbackSupported {
				if err != nil {
					t.Fatalf("AnalyzeStatement(%s) failed: %v", tc.Mode, err)
				}
				if string(parsed.Kind) != tc.RollbackKind {
					t.Fatalf("Kind = %q, want %q", parsed.Kind, tc.RollbackKind)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected fail-closed CapabilityError (%s), got parsed=%+v", tc.RollbackReason, parsed)
			}
			var capErr *CapabilityError
			if !errors.As(err, &capErr) {
				t.Fatalf("expected *CapabilityError, got %T: %v", err, err)
			}
			if tc.RollbackReason != "" && capErr.Reason != tc.RollbackReason {
				t.Fatalf("CapabilityError.Reason = %q, want %q", capErr.Reason, tc.RollbackReason)
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

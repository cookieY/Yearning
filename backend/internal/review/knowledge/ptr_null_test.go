package knowledge

import "testing"

func TestToModelEmptyScopeFieldsStayNilForSQLNull(t *testing.T) {
	model, err := toModel(&Entry{
		ID:              "k-1",
		Name:            "global-rule",
		Purpose:         "audit",
		State:           StateDraft,
		ScopeType:       ScopeGlobal,
		DatasourceID:    "",
		DatabaseName:    "",
		TableName:       "",
		SourceFindingID: "",
		CreatedByUserID: "u-1",
	})
	if err != nil {
		t.Fatalf("toModel failed: %v", err)
	}
	if model.DatasourceID != nil || model.DatabaseName != nil || model.TableRef != nil || model.SourceFindingID != nil {
		t.Fatalf("expected empty scope fields to stay nil (SQL NULL), got ds=%v db=%v tbl=%v src=%v",
			model.DatasourceID, model.DatabaseName, model.TableRef, model.SourceFindingID)
	}
}

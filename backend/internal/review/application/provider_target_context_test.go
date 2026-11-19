package application

import (
	"strings"
	"testing"
)

func TestProviderTargetContextContainsOnlyControlledFrozenFacts(t *testing.T) {
	target := FrozenDatasourceTarget{Engine: "tidb", CompatibilityMode: "mysql", EngineFamily: "mysql", ServerVersion: "8.0.36", Host: "private-host", DatabaseName: "secret_database", InstanceMarker: "secret-instance"}
	text, err := providerTargetContext(target)
	if err != nil || !strings.Contains(text, "8.0.36") || !strings.Contains(text, "tidb/mysql") || !strings.Contains(text, "matched_at_freeze") {
		t.Fatalf("controlled context missing: %q %v", text, err)
	}
	for _, raw := range []string{target.Host, target.DatabaseName, target.InstanceMarker} {
		if strings.Contains(text, raw) {
			t.Fatalf("private target value leaked")
		}
	}
	for _, invalid := range []FrozenDatasourceTarget{
		{Engine: "mysql", CompatibilityMode: "mysql", EngineFamily: "mysql", ServerVersion: "8.0.private-host"},
		{Engine: "private-host", CompatibilityMode: "mysql", EngineFamily: "mysql", ServerVersion: "8.0.36"},
		{Engine: "mysql", CompatibilityMode: "mysql", EngineFamily: "postgresql", ServerVersion: "8.0.36"},
		{Engine: "mysql", CompatibilityMode: "mysql", EngineFamily: "mysql"},
	} {
		if _, err := providerTargetContext(invalid); err == nil {
			t.Fatal("uncontrolled target context accepted")
		}
	}
}

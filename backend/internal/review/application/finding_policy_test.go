package application

import "testing"

func TestGovernFindingSeverity(t *testing.T) {
	policy := map[string]bool{
		"builtin.drop-table": true,
	}
	tests := []struct {
		name, key, severity, want string
		accept                    bool
	}{
		{name: "mandatory info", key: "builtin.drop-table", severity: "info", want: "critical", accept: true},
		{name: "mandatory low", key: "builtin.drop-table", severity: "low", want: "critical", accept: true},
		{name: "mandatory medium", key: "builtin.drop-table", severity: "medium", want: "critical", accept: true},
		{name: "mandatory high", key: "builtin.drop-table", severity: "high", want: "critical", accept: true},
		{name: "mandatory critical", key: "builtin.drop-table", severity: "critical", want: "critical", accept: true},
		{name: "observation high capped", key: "agent.lock-risk", severity: "high", want: "medium", accept: true},
		{name: "observation critical capped", key: "agent.data-loss", severity: "critical", want: "medium", accept: true},
		{name: "observation medium preserved", key: "agent.style", severity: "medium", want: "medium", accept: true},
		{name: "observation low preserved", key: "agent.style", severity: "low", want: "low", accept: true},
		{name: "invalid severity rejected", key: "builtin.drop-table", severity: "severe", accept: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := governFindingSeverity(tt.key, tt.severity, policy)
			if ok != tt.accept || got != tt.want {
				t.Fatalf("governFindingSeverity()=(%q,%v), want (%q,%v)", got, ok, tt.want, tt.accept)
			}
		})
	}
}

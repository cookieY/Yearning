package workflow

import "testing"

func changeFlow() *FlowWrite {
	rs := "rule-set-1"
	return &FlowWrite{
		Name: "Release Flow", FlowType: "change_review", Enabled: true, RuleSetID: &rs,
		Stages: []StageWrite{
			{
				Position: 1, DatasourceID: "ds-1",
				ApprovalSteps:   []ApprovalStepWrite{{Position: 1, Actors: []ActorRef{{UserID: "user-a"}}}},
				ExecutionActors: []ActorRef{{UserID: "user-b"}},
			},
			{
				Position: 2, DatasourceID: "ds-2",
				ApprovalSteps: []ApprovalStepWrite{
					{Position: 1, Actors: []ActorRef{{UserID: "user-c"}}},
					{Position: 2, Actors: []ActorRef{{UserID: "user-d"}, {UserID: "user-e"}}},
				},
				ExecutionActors: []ActorRef{{UserID: "user-f"}},
			},
		},
	}
}

func queryFlow() *FlowWrite {
	return &FlowWrite{
		Name: "Query Flow", FlowType: "query_access", Enabled: true,
		ApprovalSteps: []ApprovalStepWrite{{Position: 1, Actors: []ActorRef{{UserID: "user-a"}}}},
		QueryCapabilities: []QueryCapabilityWrite{
			{DatasourceID: "ds-1", CanQuery: true, CanExport: false},
		},
	}
}

func TestValidateFlowWriteAcceptsValidTemplates(t *testing.T) {
	if err := validateFlowWrite(changeFlow()); err != nil {
		t.Fatalf("change flow rejected: %v", err)
	}
	if err := validateFlowWrite(queryFlow()); err != nil {
		t.Fatalf("query flow rejected: %v", err)
	}
}

func TestValidateFlowWriteStructureRules(t *testing.T) {
	cases := map[string]func(*FlowWrite){
		"change missing rule set":      func(w *FlowWrite) { w.RuleSetID = nil },
		"change with query capability": func(w *FlowWrite) { w.QueryCapabilities = queryFlow().QueryCapabilities },
		"query with rule set":          func(w *FlowWrite) { w.RuleSetID = changeFlow().RuleSetID },
		"query with stages":            func(w *FlowWrite) { w.Stages = changeFlow().Stages },
		"query zero steps":             func(w *FlowWrite) { w.ApprovalSteps = nil },
		"query zero capability":        func(w *FlowWrite) { w.QueryCapabilities = nil },
		"stage duplicate position":     func(w *FlowWrite) { w.Stages[1].Position = 1 },
		"stage zero approval steps":    func(w *FlowWrite) { w.Stages[0].ApprovalSteps = nil },
		"stage zero executors":         func(w *FlowWrite) { w.Stages[0].ExecutionActors = nil },
		"step 11":                      func(w *FlowWrite) { w.Stages[0].ApprovalSteps = make([]ApprovalStepWrite, 11) },
		"step no actor":                func(w *FlowWrite) { w.Stages[0].ApprovalSteps[0].Actors = nil },
		"step duplicate actor":         func(w *FlowWrite) { w.Stages[0].ApprovalSteps[0].Actors = []ActorRef{{UserID: "u"}, {UserID: "u"}} },
		"unknown type":                 func(w *FlowWrite) { w.FlowType = "hybrid" },
		"empty name":                   func(w *FlowWrite) { w.Name = "" },
	}
	for name, mutate := range cases {
		base := changeFlow()
		if name == "query with rule set" || name == "query with stages" || name == "query zero steps" || name == "query zero capability" {
			base = queryFlow()
		}
		mutate(base)
		if err := validateFlowWrite(base); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

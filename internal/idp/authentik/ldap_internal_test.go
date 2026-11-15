package authentik

import (
	"context"
	"fmt"
	"testing"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// A flow being put back keeps every stage it wants at every moment (#204's review): the LDAP bind flow without its
// app-password stage, even for a moment, would bind anyone who names a person of the audience.
func TestPuttingAFlowBackNeverLeavesAStageOut(t *testing.T) {
	testenv.Isolate(t)
	f := newFake()
	a := f.open(t)
	stage := func(order int, s string, onPlan bool) map[string]any {
		return map[string]any{"pk": "fsb-" + s, "policybindingmodel_ptr_id": "pbm-" + s, "target": "flow-ldap", "stage": s,
			"order": order, "evaluate_on_plan": onPlan, "re_evaluate_policies": true, "invalid_response_action": "retry"}
	}
	// Drifted: the password stage moved and decided at planning, an extra stage before it.
	f.objs["/flows/bindings/"] = []map[string]any{stage(10, "stage-id", false), stage(15, "stage-extra", false),
		stage(25, "stage-password", true), stage(30, "stage-login", false)}
	f.after = func() {
		have := map[string]bool{}
		for _, b := range f.objs["/flows/bindings/"] {
			if b["target"] == "flow-ldap" {
				have[fmt.Sprint(b["stage"])] = true
			}
		}
		for _, s := range []string{"stage-id", "stage-password", "stage-login"} {
			if !have[s] {
				t.Errorf("after %s: the flow has no %s", f.changes[len(f.changes)-1], s)
			}
		}
	}
	if err := a.bindStages(context.Background(), "flow-ldap", []stageBinding{{Stage: "stage-id"}, {Stage: "stage-password"}, {Stage: "stage-login"}}); err != nil {
		t.Fatal(err)
	}
	f.after = nil
	var got []string
	for _, b := range f.objs["/flows/bindings/"] {
		got = append(got, fmt.Sprintf("%v@%v plan=%v", b["stage"], b["order"], b["evaluate_on_plan"]))
	}
	if fmt.Sprint(got) != "[stage-id@10 plan=false stage-password@20 plan=false stage-login@30 plan=false]" {
		t.Errorf("the flow, put back: %v", got)
	}
}

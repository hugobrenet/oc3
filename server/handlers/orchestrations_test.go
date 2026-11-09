package serverhandlers

import (
	"strings"
	"testing"

	"github.com/opensvc/oc3/server"
)

func TestValidateOrchestration(t *testing.T) {
	ids := func(l ...string) *[]string { return &l }
	n := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	freeze := server.OrchestrationStep{
		Name: "freeze", Action: "freeze", Kind: server.OrchestrationTargetKindService, Pace: server.All,
		Targets: server.OrchestrationTargets{Mode: server.Pick, Ids: ids("svc1")},
	}
	cases := []struct {
		name  string
		steps []server.OrchestrationStep
		err   string
	}{
		{"valid", []server.OrchestrationStep{freeze}, ""},
		{"no step", nil, "at least one step"},
		{"action of another kind", []server.OrchestrationStep{{
			Name: "x", Action: "reboot", Kind: server.OrchestrationTargetKindService, Pace: server.All,
			Targets: server.OrchestrationTargets{Mode: server.Pick, Ids: ids("svc1")},
		}}, `step 1: action "reboot" is not accepted on a service`},
		{"rid on a service", []server.OrchestrationStep{{
			Name: "x", Action: "start", Kind: server.OrchestrationTargetKindService, Pace: server.All, Rid: s("fs#1"),
			Targets: server.OrchestrationTargets{Mode: server.Pick, Ids: ids("svc1")},
		}}, "rid applies to instances only"},
		{"instance id", []server.OrchestrationStep{{
			Name: "x", Action: "restart", Kind: server.OrchestrationTargetKindInstance, Pace: server.One,
			Targets: server.OrchestrationTargets{Mode: server.Pick, Ids: ids("svc1")},
		}}, "is not svc_id@node_id"},
		{"batch without size", []server.OrchestrationStep{{
			Name: "x", Action: "checks", Kind: server.OrchestrationTargetKindNode, Pace: server.Batch,
			Targets: server.OrchestrationTargets{Mode: server.Pick, Ids: ids("n1")},
		}}, "batch_size"},
		{"same_as of a later step", []server.OrchestrationStep{{
			Name: "x", Action: "thaw", Kind: server.OrchestrationTargetKindService, Pace: server.All,
			Targets: server.OrchestrationTargets{Mode: server.SameAs, Step: n(0)},
		}}, "names an earlier step"},
		{"same_as of another kind", []server.OrchestrationStep{freeze, {
			Name: "x", Action: "checks", Kind: server.OrchestrationTargetKindNode, Pace: server.All,
			Targets: server.OrchestrationTargets{Mode: server.SameAs, Step: n(0)},
		}}, "whose targets are not nodes"},
		{"same_as", []server.OrchestrationStep{freeze, {
			Name: "thaw", Action: "thaw", Kind: server.OrchestrationTargetKindService, Pace: server.All,
			Targets: server.OrchestrationTargets{Mode: server.SameAs, Step: n(0)},
		}}, ""},
		{"failure threshold of 0", []server.OrchestrationStep{withThreshold(freeze, 0, server.Count)}, "failure_threshold must be 1 or more"},
		{"failure threshold over 100%", []server.OrchestrationStep{withThreshold(freeze, 101, server.Percent)}, "from 1 to 100"},
		{"failure threshold unit", []server.OrchestrationStep{withThreshold(freeze, 2, "half")}, `unknown failure_threshold unit "half"`},
		{"failure threshold", []server.OrchestrationStep{withThreshold(freeze, 10, server.Percent)}, ""},
		{"filterset without id", []server.OrchestrationStep{{
			Name: "x", Action: "freeze", Kind: server.OrchestrationTargetKindCluster, Pace: server.All,
			Targets: server.OrchestrationTargets{Mode: server.Filterset},
		}}, "no filterset chosen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOrchestration(server.OrchestrationInput{Name: "plan", Steps: tc.steps})
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("unexpected error: %s", err)
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("want an error with %q, got %v", tc.err, err)
			}
		})
	}
}

func withThreshold(step server.OrchestrationStep, value int, unit server.OrchestrationFailureThresholdUnit) server.OrchestrationStep {
	step.FailureThreshold = &server.OrchestrationFailureThreshold{Value: value, Unit: unit}
	return step
}

func TestFailuresTolerated(t *testing.T) {
	step := server.OrchestrationStep{}
	cases := []struct {
		name          string
		step          server.OrchestrationStep
		failed, total int
		want          bool
	}{
		{"no threshold, no failure", step, 0, 10, true},
		{"no threshold, one failure", step, 1, 10, false},
		{"under a count", withThreshold(step, 3, server.Count), 2, 10, true},
		{"at a count", withThreshold(step, 3, server.Count), 3, 10, false},
		{"under a percentage", withThreshold(step, 10, server.Percent), 1, 20, true},
		{"at a percentage", withThreshold(step, 10, server.Percent), 2, 20, false},
		{"percentage of few targets", withThreshold(step, 10, server.Percent), 1, 5, false},
		{"100%, one target left", withThreshold(step, 100, server.Percent), 4, 5, true},
		{"100%, all failed", withThreshold(step, 100, server.Percent), 5, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := failuresTolerated(tc.step, tc.failed, tc.total); got != tc.want {
				t.Fatalf("failuresTolerated(%d of %d) = %v, want %v", tc.failed, tc.total, got, tc.want)
			}
		})
	}
}

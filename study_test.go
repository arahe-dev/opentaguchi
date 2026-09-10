package opentaguchi

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/arahe-dev/emanator"
)

func TestGenerateTaguchiDesigns(t *testing.T) {
	variables := []Variable{{Name: "a", Levels: []float64{10, 20, 30}}, {Name: "b", Levels: []float64{1, 2, 3}}, {Name: "c", Levels: []float64{4, 5, 6}}, {Name: "d", Levels: []float64{7, 8, 9}}}
	l9, err := GenerateCandidates(MethodTaguchiL9, variables)
	if err != nil || len(l9) != 9 {
		t.Fatalf("L9 = %d, err=%v", len(l9), err)
	}
	if l9[0].Values["a"] != 10 || l9[1].Values["d"] != 8 || l9[8].ID != "candidate-09" {
		t.Fatalf("unexpected L9 rows: %#v", l9)
	}
	l27, err := GenerateCandidates(MethodTaguchiL27, variables)
	if err != nil || len(l27) != 27 {
		t.Fatalf("L27 = %d, err=%v", len(l27), err)
	}
	thirteen := make([]Variable, 13)
	for index := range thirteen {
		thirteen[index] = Variable{Name: string(rune('a' + index)), Levels: []float64{0, 1, 2}}
	}
	l27, err = GenerateCandidates(TaguchiL27, thirteen)
	if err != nil {
		t.Fatal(err)
	}
	for left := 0; left < 13; left++ {
		for right := left + 1; right < 13; right++ {
			counts := make(map[[2]int]int)
			for _, candidate := range l27 {
				counts[[2]int{int(candidate.Values[thirteen[left].Name]), int(candidate.Values[thirteen[right].Name])}]++
			}
			for pair, count := range counts {
				if count != 3 {
					t.Fatalf("L27 columns %d/%d pair %v occurred %d times", left, right, pair, count)
				}
			}
		}
	}
	full, err := GenerateCandidates(MethodFullFactorial, variables[:2])
	if err != nil || len(full) != 9 {
		t.Fatalf("full factorial = %d, err=%v", len(full), err)
	}
}

type fakeExecutor struct {
}

func (f fakeExecutor) StartWorkflow(_ context.Context, spec emanator.WorkflowSpec) (emanator.WorkflowState, error) {
	state := emanator.WorkflowState{ID: spec.ID, Spec: spec, Status: emanator.WorkflowSucceeded}
	value := float64(len(spec.ID))
	state.Tasks = []emanator.WorkflowTaskStatus{{ID: "solve", State: emanator.WorkflowTaskSucceeded, Result: &emanator.JobResult{Status: emanator.JobSucceeded, Metrics: map[string]float64{"mass": value, "fos": 3, "disp": 0.001}}}}
	return state, nil
}

func (f fakeExecutor) WaitWorkflow(_ context.Context, id string) (emanator.WorkflowState, error) {
	return emanator.WorkflowState{ID: id, Status: emanator.WorkflowSucceeded, Tasks: []emanator.WorkflowTaskStatus{{ID: "solve", State: emanator.WorkflowTaskSucceeded, Result: &emanator.JobResult{Status: emanator.JobSucceeded, Metrics: map[string]float64{"mass": float64(len(id)), "fos": 3, "disp": 0.001}}}}}, nil
}

func TestRunStudyRanksFeasibleCandidates(t *testing.T) {
	executor := fakeExecutor{}
	spec := StudySpec{
		ID: "test-study", Method: MethodFullFactorial,
		Variables: []Variable{{Name: "x", Levels: []float64{1, 2}}},
		Workflow: WorkflowTemplate{Build: func(studyID string, candidate Candidate) (emanator.WorkflowSpec, error) {
			return emanator.WorkflowSpec{ID: studyID + "-" + candidate.ID, Tasks: []emanator.WorkflowTask{{ID: "solve", Tool: "fake", Resources: emanator.ResourceRequest{CPU: 1, RAMMB: 1}}}}, nil
		}},
		Objectives:  []Objective{{Name: "mass", TaskID: "solve", Metric: "mass", Direction: Minimize}},
		Constraints: []Constraint{{Name: "fos", TaskID: "solve", Metric: "fos", Operator: GreaterEqual, Limit: 2.5}},
	}
	report, err := RunStudy(context.Background(), spec, executor, RunOptions{})
	if err != nil || len(report.Candidates) != 2 || report.Best == nil || report.Candidates[0].Rank != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestTaguchiPoliciesProposeFixedDesigns(t *testing.T) {
	variables := []Variable{{Name: "x", Levels: []float64{1, 2, 3}}, {Name: "y", Levels: []float64{4, 5, 6}}}
	policy := NewTaguchiL9Policy(variables)
	candidates, err := policy.Propose(context.Background(), StudyState{}, 0)
	if err != nil || len(candidates) != 9 {
		t.Fatalf("candidates=%d err=%v", len(candidates), err)
	}
	if err := policy.Observe(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !policy.Done(StudyState{}) || candidates[0].ID != "candidate-01" {
		t.Fatalf("unexpected fixed policy state: done=%t first=%q", policy.Done(StudyState{}), candidates[0].ID)
	}
}

type adaptiveFakeExecutor struct {
	mu     sync.Mutex
	states map[string]emanator.WorkflowState
}

func (e *adaptiveFakeExecutor) StartWorkflow(_ context.Context, spec emanator.WorkflowSpec) (emanator.WorkflowState, error) {
	values := make(map[string]float64)
	if err := json.Unmarshal(spec.Tasks[0].Params, &values); err != nil {
		return emanator.WorkflowState{}, err
	}
	score := (values["x"]-30)*(values["x"]-30) + (values["y"]-15)*(values["y"]-15)
	state := emanator.WorkflowState{
		ID:     spec.ID,
		Spec:   spec,
		Status: emanator.WorkflowSucceeded,
		Tasks:  []emanator.WorkflowTaskStatus{{ID: "solve", State: emanator.WorkflowTaskSucceeded, Result: &emanator.JobResult{Status: emanator.JobSucceeded, Metrics: map[string]float64{"score": score}}}},
	}
	e.mu.Lock()
	if e.states == nil {
		e.states = make(map[string]emanator.WorkflowState)
	}
	e.states[spec.ID] = state
	e.mu.Unlock()
	return state, nil
}

func (e *adaptiveFakeExecutor) WaitWorkflow(_ context.Context, id string) (emanator.WorkflowState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.states[id], nil
}

func TestRunAdaptiveStudyRecentersSecondL9(t *testing.T) {
	spec := StudySpec{
		ID: "adaptive-arm", Method: TaguchiL9,
		Variables: []Variable{
			{Name: "x", Levels: []float64{20, 30, 40}},
			{Name: "y", Levels: []float64{10, 15, 20}},
		},
		Workflow: WorkflowTemplate{Build: func(studyID string, candidate Candidate) (emanator.WorkflowSpec, error) {
			params, err := json.Marshal(candidate.Values)
			if err != nil {
				return emanator.WorkflowSpec{}, err
			}
			return emanator.WorkflowSpec{ID: studyID + "-" + candidate.ID, Tasks: []emanator.WorkflowTask{{ID: "solve", Tool: "fake", Resources: emanator.ResourceRequest{CPU: 1, RAMMB: 1}, Params: params}}}, nil
		}},
		Objectives: []Objective{{Name: "score", TaskID: "solve", Metric: "score", Direction: Minimize}},
	}
	policy, err := NewSuccessiveRefinementPolicy(spec, RefinementPolicyOptions{Rounds: 2, ShrinkFactor: 0.3, ClampToInitialBounds: true})
	if err != nil {
		t.Fatal(err)
	}
	report, err := RunAdaptiveStudy(context.Background(), spec, policy, &adaptiveFakeExecutor{}, AdaptiveRunOptions{MaxConcurrent: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rounds) != 2 || len(report.Candidates) != 18 || report.State.Round != 2 || report.Best == nil {
		t.Fatalf("unexpected adaptive report: rounds=%d candidates=%d state=%d best=%#v", len(report.Rounds), len(report.Candidates), report.State.Round, report.Best)
	}
	secondRoundIDs := make(map[string]struct{}, len(report.Rounds[1].Candidates))
	for _, result := range report.Rounds[1].Candidates {
		secondRoundIDs[result.Candidate.ID] = struct{}{}
	}
	if _, ok := secondRoundIDs["round-02-candidate-01"]; !ok {
		t.Fatalf("second round ids=%v", secondRoundIDs)
	}
	foundRefinedLevel := false
	for _, result := range report.Rounds[1].Candidates {
		if result.Candidate.Values["x"] == 27 && result.Candidate.Values["y"] == 13.5 {
			foundRefinedLevel = true
			break
		}
	}
	if !foundRefinedLevel {
		t.Fatalf("second round did not use recentered levels: %#v", report.Rounds[1].Candidates)
	}
	if report.Best.ObjectiveValues["score"] != 0 {
		t.Fatalf("best=%#v", report.Best.ObjectiveValues)
	}
}

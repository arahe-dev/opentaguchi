package opentaguchi

import (
	"context"
	"fmt"
	"sort"
)

// Policy chooses which candidates should be evaluated next. Implementations
// must derive their proposal solely from their immutable configuration and the
// supplied StudyState; a newly constructed policy must produce the same next
// proposal for the same state.
type Policy interface {
	Propose(context.Context, StudyState, int) ([]Candidate, error)
	Done(StudyState) bool
}

// StudyState is the serializable, solver-independent state of an adaptive
// study. Round is the number of completed rounds; a new study starts at zero.
// Rounds preserves the per-round observations needed by restartable policies.
type StudyState struct {
	StudyID      string            `json:"study_id"`
	Method       Method            `json:"method"`
	Round        int               `json:"round"`
	Results      []CandidateResult `json:"results"`
	RoundResults []CandidateResult `json:"round_results,omitempty"`
	RoundBest    *CandidateResult  `json:"round_best,omitempty"`
	Best         *CandidateResult  `json:"best,omitempty"`
	Rounds       []AdaptiveRound   `json:"rounds,omitempty"`
}

// AdaptiveRound records one policy proposal/execution cycle.
type AdaptiveRound struct {
	Number     int               `json:"number"`
	Candidates []CandidateResult `json:"candidates"`
}

// AdaptiveStudyReport contains the complete history and final state of an
// adaptive run. Candidates is also available through State.Results; it is
// kept as a top-level convenience for callers that consume StudyReport.
type AdaptiveStudyReport struct {
	StudyID    string            `json:"study_id"`
	Method     Method            `json:"method"`
	Rounds     []AdaptiveRound   `json:"rounds"`
	Candidates []CandidateResult `json:"candidates"`
	Best       *CandidateResult  `json:"best,omitempty"`
	State      StudyState        `json:"state"`
}

// AdaptiveRunOptions controls the execution and stopping envelope around a
// Policy. A zero MaxRounds means the policy alone controls termination.
// CandidatesPerRound is an advisory proposal budget; atomic DOE policies
// intentionally ignore it and emit their complete design.
type AdaptiveRunOptions struct {
	MaxConcurrent      int
	CandidatesPerRound int
	MaxRounds          int
	// InitialState resumes a run from a previously persisted StudyState. An
	// empty state starts a new study.
	InitialState StudyState
}

// FixedDOEPolicy is an atomic one-shot DOE policy. It emits the complete
// design in one proposal; the CandidatesPerRound budget is intentionally
// ignored so that an L9 or L27 remains an intact orthogonal array.
type FixedDOEPolicy struct {
	Method    Method
	Variables []Variable
}

func NewFixedDOEPolicy(method Method, variables []Variable) *FixedDOEPolicy {
	return &FixedDOEPolicy{Method: method, Variables: cloneVariables(variables)}
}

func (p *FixedDOEPolicy) Propose(ctx context.Context, state StudyState, _ int) ([]Candidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if state.Round > 0 {
		return nil, nil
	}
	candidates, err := GenerateCandidates(p.Method, p.Variables)
	if err != nil {
		return nil, err
	}
	return cloneCandidates(candidates), nil
}

func (p *FixedDOEPolicy) Done(state StudyState) bool {
	return state.Round > 0
}

// TaguchiL9Policy proposes the complete deterministic L9 once.
type TaguchiL9Policy struct{ *FixedDOEPolicy }

func NewTaguchiL9Policy(variables []Variable) *TaguchiL9Policy {
	return &TaguchiL9Policy{FixedDOEPolicy: NewFixedDOEPolicy(TaguchiL9, variables)}
}

// TaguchiL27Policy proposes the complete deterministic L27 once.
type TaguchiL27Policy struct{ *FixedDOEPolicy }

func NewTaguchiL27Policy(variables []Variable) *TaguchiL27Policy {
	return &TaguchiL27Policy{FixedDOEPolicy: NewFixedDOEPolicy(TaguchiL27, variables)}
}

// FullFactorialPolicy proposes the complete cartesian product once.
type FullFactorialPolicy struct{ *FixedDOEPolicy }

func NewFullFactorialPolicy(variables []Variable) *FullFactorialPolicy {
	return &FullFactorialPolicy{FixedDOEPolicy: NewFixedDOEPolicy(FullFactorial, variables)}
}

// RefinementPolicyOptions controls deterministic recentering. A shrink factor
// of 0.3 turns [20, 30, 40] centered at 30 into [27, 30, 33].
type RefinementPolicyOptions struct {
	Rounds               int
	ShrinkFactor         float64
	ClampToInitialBounds bool
}

// SuccessiveRefinementPolicy runs repeated three-level Taguchi rounds. After
// each completed round it derives the next levels by recentering and shrinking
// every variable around that round's best candidate. It is intentionally
// deterministic, inspectable, and restartable from StudyState.
type SuccessiveRefinementPolicy struct {
	Method               Method
	Initial              []Variable
	Rounds               int
	ShrinkFactor         float64
	ClampToInitialBounds bool
	Objectives           []Objective
	Constraints          []Constraint
}

func NewSuccessiveRefinementPolicy(spec StudySpec, options RefinementPolicyOptions) (*SuccessiveRefinementPolicy, error) {
	if spec.Method != TaguchiL9 && spec.Method != TaguchiL27 {
		return nil, fmt.Errorf("successive refinement requires a Taguchi method, got %q", spec.Method)
	}
	if err := validateVariables(spec.Method, spec.Variables); err != nil {
		return nil, err
	}
	if len(spec.Objectives) == 0 {
		return nil, fmt.Errorf("successive refinement requires at least one objective")
	}
	rounds := options.Rounds
	if rounds <= 0 {
		rounds = 2
	}
	shrink := options.ShrinkFactor
	if shrink == 0 {
		shrink = 0.3
	}
	if shrink <= 0 || shrink >= 1 {
		return nil, fmt.Errorf("refinement shrink factor must be > 0 and < 1")
	}
	if err := validateRefinementVariables(spec.Variables); err != nil {
		return nil, err
	}
	return &SuccessiveRefinementPolicy{
		Method:               spec.Method,
		Initial:              cloneVariables(spec.Variables),
		Rounds:               rounds,
		ShrinkFactor:         shrink,
		ClampToInitialBounds: options.ClampToInitialBounds,
		Objectives:           append([]Objective(nil), spec.Objectives...),
		Constraints:          append([]Constraint(nil), spec.Constraints...),
	}, nil
}

func (p *SuccessiveRefinementPolicy) Propose(ctx context.Context, state StudyState, _ int) ([]Candidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if state.Round >= p.Rounds {
		return nil, nil
	}
	current, err := p.variablesForNextRound(state)
	if err != nil {
		return nil, err
	}
	candidates, err := GenerateCandidates(p.Method, current)
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		candidates[index].ID = fmt.Sprintf("round-%02d-%s", state.Round+1, candidates[index].ID)
	}
	return cloneCandidates(candidates), nil
}

func (p *SuccessiveRefinementPolicy) variablesForNextRound(state StudyState) ([]Variable, error) {
	current := cloneVariables(p.Initial)
	for completedRound := 1; completedRound <= state.Round; completedRound++ {
		best := state.bestForRound(completedRound, p.Objectives)
		if best == nil {
			return nil, fmt.Errorf("refinement round %d has no observed best candidate in StudyState", completedRound)
		}
		var err error
		current, err = refineVariables(current, p.Initial, best.Candidate.Values, p.ShrinkFactor, p.ClampToInitialBounds)
		if err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (p *SuccessiveRefinementPolicy) Done(state StudyState) bool {
	return state.Round >= p.Rounds
}

func (state StudyState) bestForRound(number int, objectives []Objective) *CandidateResult {
	for _, round := range state.Rounds {
		if round.Number == number {
			return bestResult(round.Candidates, objectives)
		}
	}
	if number == state.Round {
		if state.RoundBest != nil {
			best := *state.RoundBest
			return &best
		}
		return bestResult(state.RoundResults, objectives)
	}
	return nil
}

func validateRefinementVariables(variables []Variable) error {
	for _, variable := range variables {
		if len(variable.Levels) != 3 {
			return fmt.Errorf("refinement variable %q must have exactly three levels", variable.Name)
		}
		levels := append([]float64(nil), variable.Levels...)
		sort.Float64s(levels)
		if levels[0] == levels[2] {
			return fmt.Errorf("refinement variable %q must have distinct levels", variable.Name)
		}
	}
	return nil
}

func refineVariables(current, initial []Variable, centers map[string]float64, shrink float64, clamp bool) ([]Variable, error) {
	initialBounds := make(map[string][2]float64, len(initial))
	for _, variable := range initial {
		levels := append([]float64(nil), variable.Levels...)
		sort.Float64s(levels)
		initialBounds[variable.Name] = [2]float64{levels[0], levels[len(levels)-1]}
	}
	refined := make([]Variable, len(current))
	for index, variable := range current {
		levels := append([]float64(nil), variable.Levels...)
		sort.Float64s(levels)
		center, ok := centers[variable.Name]
		if !ok {
			return nil, fmt.Errorf("best candidate has no value for refinement variable %q", variable.Name)
		}
		span := (levels[len(levels)-1] - levels[0]) * shrink
		if span <= 0 {
			return nil, fmt.Errorf("refinement variable %q has no positive span", variable.Name)
		}
		low := center - span/2
		high := center + span/2
		if clamp {
			bounds := initialBounds[variable.Name]
			if low < bounds[0] {
				high += bounds[0] - low
				low = bounds[0]
			}
			if high > bounds[1] {
				low -= high - bounds[1]
				high = bounds[1]
			}
			if low < bounds[0] {
				low = bounds[0]
			}
		}
		refined[index] = Variable{Name: variable.Name, Levels: []float64{low, (low + high) / 2, high}}
	}
	return refined, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneVariables(variables []Variable) []Variable {
	cloned := make([]Variable, len(variables))
	for index, variable := range variables {
		cloned[index] = Variable{Name: variable.Name, Levels: append([]float64(nil), variable.Levels...)}
	}
	return cloned
}

func cloneCandidates(candidates []Candidate) []Candidate {
	cloned := make([]Candidate, len(candidates))
	for index, candidate := range candidates {
		cloned[index] = Candidate{ID: candidate.ID, Index: candidate.Index, Values: make(map[string]float64, len(candidate.Values))}
		for name, value := range candidate.Values {
			cloned[index].Values[name] = value
		}
	}
	return cloned
}

func cloneCandidateResults(results []CandidateResult) []CandidateResult {
	cloned := make([]CandidateResult, len(results))
	for index, result := range results {
		cloned[index] = result
		cloned[index].Candidate = cloneCandidates([]Candidate{result.Candidate})[0]
		cloned[index].Violations = append([]string(nil), result.Violations...)
		cloned[index].Metrics = cloneFloatMap(result.Metrics)
		cloned[index].ObjectiveValues = cloneFloatMap(result.ObjectiveValues)
	}
	return cloned
}

func cloneAdaptiveRounds(rounds []AdaptiveRound) []AdaptiveRound {
	cloned := make([]AdaptiveRound, len(rounds))
	for index, round := range rounds {
		cloned[index] = AdaptiveRound{
			Number:     round.Number,
			Candidates: cloneCandidateResults(round.Candidates),
		}
	}
	return cloned
}

func cloneStudyState(state StudyState) StudyState {
	cloned := state
	cloned.Results = cloneCandidateResults(state.Results)
	cloned.RoundResults = cloneCandidateResults(state.RoundResults)
	cloned.Rounds = cloneAdaptiveRounds(state.Rounds)
	if state.RoundBest != nil {
		best := cloneCandidateResults([]CandidateResult{*state.RoundBest})[0]
		cloned.RoundBest = &best
	}
	if state.Best != nil {
		best := cloneCandidateResults([]CandidateResult{*state.Best})[0]
		cloned.Best = &best
	}
	return cloned
}

func cloneFloatMap(values map[string]float64) map[string]float64 {
	if values == nil {
		return nil
	}
	cloned := make(map[string]float64, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
}

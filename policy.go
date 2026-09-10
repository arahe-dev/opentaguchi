package opentaguchi

import (
	"context"
	"fmt"
	"sort"
)

// Policy chooses which candidates should be evaluated next. A policy may be
// completely fixed, as the one-shot DOE policies are, or may use observed
// results to propose a later round.
type Policy interface {
	Propose(context.Context, StudyState, int) ([]Candidate, error)
	Observe(context.Context, []CandidateResult) error
	Done(StudyState) bool
}

// StudyState is the serializable, solver-independent state of an adaptive
// study. Round is the number of completed rounds; a new study starts at zero.
type StudyState struct {
	StudyID      string            `json:"study_id"`
	Method       Method            `json:"method"`
	Round        int               `json:"round"`
	Results      []CandidateResult `json:"results"`
	RoundResults []CandidateResult `json:"round_results,omitempty"`
	RoundBest    *CandidateResult  `json:"round_best,omitempty"`
	Best         *CandidateResult  `json:"best,omitempty"`
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
type AdaptiveRunOptions struct {
	MaxConcurrent      int
	CandidatesPerRound int
	MaxRounds          int
}

// FixedDOEPolicy is a one-shot DOE policy. If CandidatesPerRound is smaller
// than the generated design, it emits deterministic chunks until exhausted.
type FixedDOEPolicy struct {
	Method    Method
	Variables []Variable

	candidates  []Candidate
	next        int
	initialized bool
	initErr     error
}

func NewFixedDOEPolicy(method Method, variables []Variable) *FixedDOEPolicy {
	return &FixedDOEPolicy{Method: method, Variables: cloneVariables(variables)}
}

func (p *FixedDOEPolicy) Propose(ctx context.Context, _ StudyState, budget int) ([]Candidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if !p.initialized {
		p.candidates, p.initErr = GenerateCandidates(p.Method, p.Variables)
		p.initialized = true
	}
	if p.initErr != nil {
		return nil, p.initErr
	}
	if p.next >= len(p.candidates) {
		return nil, nil
	}
	count := len(p.candidates) - p.next
	if budget > 0 && count > budget {
		count = budget
	}
	proposed := cloneCandidates(p.candidates[p.next : p.next+count])
	p.next += count
	return proposed, nil
}

func (p *FixedDOEPolicy) Observe(ctx context.Context, _ []CandidateResult) error {
	return contextError(ctx)
}

func (p *FixedDOEPolicy) Done(_ StudyState) bool {
	return p.initialized && p.next >= len(p.candidates)
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
// each observed round it recenters and shrinks every variable around that
// round's best candidate. It is intentionally deterministic and inspectable.
type SuccessiveRefinementPolicy struct {
	Method               Method
	Initial              []Variable
	Rounds               int
	ShrinkFactor         float64
	ClampToInitialBounds bool
	Objectives           []Objective
	Constraints          []Constraint

	current  []Variable
	observed []CandidateResult
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

func (p *SuccessiveRefinementPolicy) Propose(ctx context.Context, state StudyState, budget int) ([]Candidate, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if state.Round >= p.Rounds {
		return nil, nil
	}
	if state.Round == 0 {
		p.current = cloneVariables(p.Initial)
	} else {
		best := state.RoundBest
		if best == nil && len(p.observed) > 0 {
			best = bestResult(p.observed, p.Objectives)
		}
		if best == nil {
			return nil, fmt.Errorf("refinement round %d has no observed best candidate", state.Round)
		}
		var err error
		p.current, err = refineVariables(p.current, p.Initial, best.Candidate.Values, p.ShrinkFactor, p.ClampToInitialBounds)
		if err != nil {
			return nil, err
		}
	}
	candidates, err := GenerateCandidates(p.Method, p.current)
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		candidates[index].ID = fmt.Sprintf("round-%02d-%s", state.Round+1, candidates[index].ID)
	}
	if budget > 0 && budget < len(candidates) {
		candidates = candidates[:budget]
	}
	return cloneCandidates(candidates), nil
}

func (p *SuccessiveRefinementPolicy) Observe(ctx context.Context, results []CandidateResult) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	p.observed = cloneCandidateResults(results)
	return nil
}

func (p *SuccessiveRefinementPolicy) Done(state StudyState) bool {
	return state.Round >= p.Rounds
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

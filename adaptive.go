package opentaguchi

import (
	"context"
	"fmt"
	"sort"
)

// RunAdaptiveStudy executes proposal/observation rounds through the same
// workflow executor used by RunStudy. The policy owns what to propose next;
// this function owns validation, asynchronous execution, ranking, and state.
func RunAdaptiveStudy(ctx context.Context, spec StudySpec, policy Policy, executor WorkflowExecutor, options AdaptiveRunOptions) (AdaptiveStudyReport, error) {
	report := AdaptiveStudyReport{StudyID: spec.ID, Method: spec.Method}
	if err := spec.Validate(); err != nil {
		return report, err
	}
	if policy == nil {
		return report, fmt.Errorf("adaptive study policy is required")
	}
	if executor == nil {
		return report, fmt.Errorf("workflow executor is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state := cloneStudyState(options.InitialState)
	if state.StudyID == "" {
		state.StudyID = spec.ID
	}
	if state.StudyID != spec.ID {
		return report, fmt.Errorf("adaptive study state belongs to %q, not %q", state.StudyID, spec.ID)
	}
	if state.Method == "" {
		state.Method = spec.Method
	}
	if state.Method != spec.Method {
		return report, fmt.Errorf("adaptive study state uses method %q, not %q", state.Method, spec.Method)
	}
	if state.Round < 0 {
		return report, fmt.Errorf("adaptive study state has negative round %d", state.Round)
	}
	state = normalizeStudyState(state, spec.Objectives)
	report = adaptiveReportFromState(state, spec.Objectives)
	seenIDs := candidateIDs(state)
	for !policy.Done(state) {
		if options.MaxRounds > 0 && state.Round >= options.MaxRounds {
			return finalizeAdaptiveReport(report, state, spec.Objectives), fmt.Errorf("adaptive study reached max rounds %d before policy completed", options.MaxRounds)
		}
		candidates, err := policy.Propose(ctx, cloneStudyState(state), options.CandidatesPerRound)
		if err != nil {
			return finalizeAdaptiveReport(report, state, spec.Objectives), err
		}
		if len(candidates) == 0 {
			if policy.Done(state) {
				break
			}
			return finalizeAdaptiveReport(report, state, spec.Objectives), fmt.Errorf("adaptive policy proposed no candidates in round %d", state.Round+1)
		}
		if err := validateProposals(candidates, spec.Variables, seenIDs); err != nil {
			return finalizeAdaptiveReport(report, state, spec.Objectives), err
		}
		results := executeCandidates(ctx, spec, candidates, executor, options.MaxConcurrent)
		rankCandidateResults(results, spec.Objectives)
		state.RoundResults = cloneCandidateResults(results)
		state.RoundBest = bestResult(results, spec.Objectives)
		state.Results = append(state.Results, cloneCandidateResults(results)...)
		rankCandidateResults(state.Results, spec.Objectives)
		state.Best = bestResult(state.Results, spec.Objectives)
		state.Round++
		state.Rounds = append(state.Rounds, AdaptiveRound{
			Number:     state.Round,
			Candidates: cloneCandidateResults(results),
		})
		if err := contextError(ctx); err != nil {
			return finalizeAdaptiveReport(report, state, spec.Objectives), err
		}
	}
	return finalizeAdaptiveReport(report, state, spec.Objectives), nil
}

func validateProposals(candidates []Candidate, variables []Variable, seenIDs map[string]struct{}) error {
	for _, candidate := range candidates {
		if !validName(candidate.ID) {
			return fmt.Errorf("policy proposed invalid candidate id %q", candidate.ID)
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return fmt.Errorf("policy proposed duplicate candidate id %q", candidate.ID)
		}
		seenIDs[candidate.ID] = struct{}{}
		for _, variable := range variables {
			if _, exists := candidate.Values[variable.Name]; !exists {
				return fmt.Errorf("candidate %q has no value for variable %q", candidate.ID, variable.Name)
			}
		}
	}
	return nil
}

func rankCandidateResults(results []CandidateResult, objectives []Objective) {
	sort.SliceStable(results, func(i, j int) bool {
		return better(results[i], results[j], objectives)
	})
	for index := range results {
		results[index].Rank = index + 1
	}
}

func bestResult(results []CandidateResult, objectives []Objective) *CandidateResult {
	if len(results) == 0 {
		return nil
	}
	ranked := cloneCandidateResults(results)
	rankCandidateResults(ranked, objectives)
	for _, result := range ranked {
		if result.Feasible && result.Error == "" {
			best := result
			return &best
		}
	}
	return nil
}

func finalizeAdaptiveReport(report AdaptiveStudyReport, state StudyState, objectives []Objective) AdaptiveStudyReport {
	rankCandidateResults(state.Results, objectives)
	state.RoundResults = cloneCandidateResults(state.RoundResults)
	state.Best = bestResult(state.Results, objectives)
	report.State = state
	report.Rounds = cloneAdaptiveRounds(state.Rounds)
	report.Candidates = cloneCandidateResults(state.Results)
	report.Best = bestResult(report.Candidates, objectives)
	return report
}

func adaptiveReportFromState(state StudyState, objectives []Objective) AdaptiveStudyReport {
	return AdaptiveStudyReport{
		StudyID:    state.StudyID,
		Method:     state.Method,
		Rounds:     cloneAdaptiveRounds(state.Rounds),
		Candidates: cloneCandidateResults(state.Results),
		Best:       bestResult(state.Results, objectives),
		State:      cloneStudyState(state),
	}
}

func candidateIDs(state StudyState) map[string]struct{} {
	seen := make(map[string]struct{}, len(state.Results))
	for _, result := range state.Results {
		seen[result.Candidate.ID] = struct{}{}
	}
	for _, round := range state.Rounds {
		for _, result := range round.Candidates {
			seen[result.Candidate.ID] = struct{}{}
		}
	}
	return seen
}

func normalizeStudyState(state StudyState, objectives []Objective) StudyState {
	state = cloneStudyState(state)
	if state.Round == 0 && len(state.Rounds) > 0 {
		state.Round = state.Rounds[len(state.Rounds)-1].Number
	}
	if len(state.Results) == 0 && len(state.Rounds) > 0 {
		for _, round := range state.Rounds {
			state.Results = append(state.Results, cloneCandidateResults(round.Candidates)...)
		}
	}
	if len(state.Rounds) > 0 {
		latest := state.Rounds[len(state.Rounds)-1]
		state.RoundResults = cloneCandidateResults(latest.Candidates)
		state.RoundBest = bestResult(state.RoundResults, objectives)
	} else if len(state.RoundResults) > 0 {
		state.RoundBest = bestResult(state.RoundResults, objectives)
	}
	rankCandidateResults(state.Results, objectives)
	state.Best = bestResult(state.Results, objectives)
	return state
}

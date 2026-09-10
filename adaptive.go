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
	state := StudyState{StudyID: spec.ID, Method: spec.Method}
	report := AdaptiveStudyReport{StudyID: spec.ID, Method: spec.Method, State: state}
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
	seenIDs := make(map[string]struct{})
	for !policy.Done(state) {
		if options.MaxRounds > 0 && state.Round >= options.MaxRounds {
			return finalizeAdaptiveReport(report, state, spec.Objectives), fmt.Errorf("adaptive study reached max rounds %d before policy completed", options.MaxRounds)
		}
		candidates, err := policy.Propose(ctx, state, options.CandidatesPerRound)
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
		report.Rounds = append(report.Rounds, AdaptiveRound{
			Number:     state.Round,
			Candidates: cloneCandidateResults(results),
		})
		if err := policy.Observe(ctx, results); err != nil {
			return finalizeAdaptiveReport(report, state, spec.Objectives), err
		}
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
	report.Candidates = cloneCandidateResults(state.Results)
	report.Best = bestResult(report.Candidates, objectives)
	return report
}

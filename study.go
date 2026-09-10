package opentaguchi

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/arahe-dev/emanator"
)

// WorkflowTemplate compiles one DOE candidate into an Emanator workflow.
// The callback keeps the SDK solver- and domain-independent while preserving
// the full Emanator DAG contract.
type WorkflowTemplate struct {
	Build func(studyID string, candidate Candidate) (emanator.WorkflowSpec, error) `json:"-"`
}

type ObjectiveDirection string

const (
	Minimize          ObjectiveDirection = "minimize"
	Maximize          ObjectiveDirection = "maximize"
	ObjectiveMinimize                    = Minimize
	ObjectiveMaximize                    = Maximize
)

// Objective identifies a metric produced by one workflow task.
type Objective struct {
	Name      string             `json:"name"`
	TaskID    string             `json:"task_id"`
	Metric    string             `json:"metric"`
	Direction ObjectiveDirection `json:"direction"`
}

type ConstraintOperator string

const (
	LessEqual    ConstraintOperator = "<="
	GreaterEqual ConstraintOperator = ">="
	Less         ConstraintOperator = "<"
	Greater      ConstraintOperator = ">"
	Equal        ConstraintOperator = "="
)

// Constraint determines candidate feasibility from a task metric.
type Constraint struct {
	Name     string             `json:"name"`
	TaskID   string             `json:"task_id"`
	Metric   string             `json:"metric"`
	Operator ConstraintOperator `json:"operator"`
	Limit    float64            `json:"limit"`
}

// StudySpec is the complete declarative part of a parameter study.
type StudySpec struct {
	ID          string           `json:"id"`
	Method      Method           `json:"method"`
	Variables   []Variable       `json:"variables"`
	Workflow    WorkflowTemplate `json:"-"`
	Objectives  []Objective      `json:"objectives"`
	Constraints []Constraint     `json:"constraints,omitempty"`
}

// WorkflowExecutor is implemented by emanator.CoordinatorClient. Use
// CoordinatorExecutor when running directly against an in-process Coordinator.
type WorkflowExecutor interface {
	StartWorkflow(context.Context, emanator.WorkflowSpec) (emanator.WorkflowState, error)
	WaitWorkflow(context.Context, string) (emanator.WorkflowState, error)
}

type CoordinatorExecutor struct {
	Coordinator *emanator.Coordinator
}

func (e CoordinatorExecutor) StartWorkflow(ctx context.Context, spec emanator.WorkflowSpec) (emanator.WorkflowState, error) {
	if e.Coordinator == nil {
		return emanator.WorkflowState{}, fmt.Errorf("nil emanator coordinator")
	}
	select {
	case <-ctx.Done():
		return emanator.WorkflowState{}, ctx.Err()
	default:
	}
	return e.Coordinator.StartWorkflow(spec)
}

func (e CoordinatorExecutor) WaitWorkflow(ctx context.Context, id string) (emanator.WorkflowState, error) {
	if e.Coordinator == nil {
		return emanator.WorkflowState{}, fmt.Errorf("nil emanator coordinator")
	}
	return e.Coordinator.WaitWorkflow(ctx, id)
}

type RunOptions struct {
	// MaxConcurrent bounds the number of active candidate workflows. Zero uses
	// one slot per generated candidate.
	MaxConcurrent int
}

type CandidateResult struct {
	Candidate       Candidate               `json:"candidate"`
	Workflow        *emanator.WorkflowState `json:"workflow,omitempty"`
	Metrics         map[string]float64      `json:"metrics,omitempty"`
	ObjectiveValues map[string]float64      `json:"objective_values,omitempty"`
	Feasible        bool                    `json:"feasible"`
	Violations      []string                `json:"violations,omitempty"`
	Rank            int                     `json:"rank"`
	Error           string                  `json:"error,omitempty"`
}

type StudyReport struct {
	StudyID    string            `json:"study_id"`
	Method     Method            `json:"method"`
	Candidates []CandidateResult `json:"candidates"`
	Best       *CandidateResult  `json:"best,omitempty"`
}

func (s StudySpec) Validate() error {
	if !validName(s.ID) {
		return fmt.Errorf("study id %q must contain only letters, numbers, '-', '_' or '.'", s.ID)
	}
	if err := validateVariables(s.Method, s.Variables); err != nil {
		return err
	}
	if s.Workflow.Build == nil {
		return fmt.Errorf("study workflow builder is required")
	}
	if len(s.Objectives) == 0 {
		return fmt.Errorf("at least one objective is required")
	}
	objectiveNames := make(map[string]struct{}, len(s.Objectives))
	for _, objective := range s.Objectives {
		if strings.TrimSpace(objective.Name) == "" || strings.TrimSpace(objective.TaskID) == "" || strings.TrimSpace(objective.Metric) == "" {
			return fmt.Errorf("objective must have name, task id, and metric")
		}
		if _, exists := objectiveNames[objective.Name]; exists {
			return fmt.Errorf("objective %q is repeated", objective.Name)
		}
		objectiveNames[objective.Name] = struct{}{}
		if objective.Direction != Minimize && objective.Direction != Maximize {
			return fmt.Errorf("objective %q has unsupported direction %q", objective.Name, objective.Direction)
		}
	}
	constraintNames := make(map[string]struct{}, len(s.Constraints))
	for _, constraint := range s.Constraints {
		if strings.TrimSpace(constraint.Name) == "" || strings.TrimSpace(constraint.TaskID) == "" || strings.TrimSpace(constraint.Metric) == "" {
			return fmt.Errorf("constraint must have name, task id, and metric")
		}
		if _, exists := constraintNames[constraint.Name]; exists {
			return fmt.Errorf("constraint %q is repeated", constraint.Name)
		}
		constraintNames[constraint.Name] = struct{}{}
		switch constraint.Operator {
		case LessEqual, GreaterEqual, Less, Greater, Equal:
		default:
			return fmt.Errorf("constraint %q has unsupported operator %q", constraint.Name, constraint.Operator)
		}
	}
	return nil
}

func validName(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func RunStudy(ctx context.Context, spec StudySpec, executor WorkflowExecutor, options RunOptions) (StudyReport, error) {
	adaptive, err := RunAdaptiveStudy(ctx, spec, NewFixedDOEPolicy(spec.Method, spec.Variables), executor, AdaptiveRunOptions{MaxConcurrent: options.MaxConcurrent})
	return StudyReport{
		StudyID:    adaptive.StudyID,
		Method:     adaptive.Method,
		Candidates: adaptive.Candidates,
		Best:       adaptive.Best,
	}, err
}

func executeCandidates(ctx context.Context, spec StudySpec, candidates []Candidate, executor WorkflowExecutor, maxConcurrent int) []CandidateResult {
	if len(candidates) == 0 {
		return nil
	}
	limit := maxConcurrent
	if limit <= 0 || limit > len(candidates) {
		limit = len(candidates)
	}
	results := make(chan CandidateResult, len(candidates))
	semaphore := make(chan struct{}, limit)
	var waitGroup sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results <- CandidateResult{Candidate: candidate, Error: ctx.Err().Error()}
				return
			}
			defer func() { <-semaphore }()
			results <- runCandidate(ctx, spec, candidate, executor)
		}()
	}
	waitGroup.Wait()
	close(results)
	collected := make([]CandidateResult, 0, len(candidates))
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func runCandidate(ctx context.Context, spec StudySpec, candidate Candidate, executor WorkflowExecutor) CandidateResult {
	result := CandidateResult{Candidate: candidate}
	workflow, err := spec.Workflow.Build(spec.ID, candidate)
	if err != nil {
		result.Error = fmt.Sprintf("build workflow: %v", err)
		return result
	}
	started, err := executor.StartWorkflow(ctx, workflow)
	if err != nil {
		result.Error = fmt.Sprintf("start workflow: %v", err)
		return result
	}
	final, err := executor.WaitWorkflow(ctx, started.ID)
	if err != nil {
		result.Error = fmt.Sprintf("wait workflow: %v", err)
		return result
	}
	result.Workflow = &final
	if final.Status != emanator.WorkflowSucceeded {
		result.Error = fmt.Sprintf("workflow ended %s", final.Status)
		return result
	}
	result.Metrics = make(map[string]float64)
	for _, task := range final.Tasks {
		if task.Result == nil {
			continue
		}
		for name, value := range task.Result.Metrics {
			result.Metrics[task.ID+"."+name] = value
		}
	}
	result.ObjectiveValues = make(map[string]float64, len(spec.Objectives))
	for _, objective := range spec.Objectives {
		value, ok := result.Metrics[objective.TaskID+"."+objective.Metric]
		if !ok {
			result.Error = fmt.Sprintf("missing objective metric %s.%s", objective.TaskID, objective.Metric)
			return result
		}
		result.ObjectiveValues[objective.Name] = value
	}
	result.Feasible = true
	for _, constraint := range spec.Constraints {
		value, ok := result.Metrics[constraint.TaskID+"."+constraint.Metric]
		if !ok {
			result.Error = fmt.Sprintf("missing constraint metric %s.%s", constraint.TaskID, constraint.Metric)
			result.Feasible = false
			continue
		}
		if !satisfies(value, constraint.Operator, constraint.Limit) {
			result.Feasible = false
			result.Violations = append(result.Violations, fmt.Sprintf("%s: %.9g %s %.9g", constraint.Name, value, constraint.Operator, constraint.Limit))
		}
	}
	return result
}

func satisfies(value float64, operator ConstraintOperator, limit float64) bool {
	switch operator {
	case LessEqual:
		return value <= limit
	case GreaterEqual:
		return value >= limit
	case Less:
		return value < limit
	case Greater:
		return value > limit
	case Equal:
		return value == limit
	default:
		return false
	}
}

func better(left, right CandidateResult, objectives []Objective) bool {
	if left.Feasible != right.Feasible {
		return left.Feasible
	}
	if (left.Error == "") != (right.Error == "") {
		return left.Error == ""
	}
	if !left.Feasible && len(left.Violations) != len(right.Violations) {
		return len(left.Violations) < len(right.Violations)
	}
	for _, objective := range objectives {
		leftValue, leftOK := left.ObjectiveValues[objective.Name]
		rightValue, rightOK := right.ObjectiveValues[objective.Name]
		if leftOK != rightOK {
			return leftOK
		}
		if !leftOK {
			continue
		}
		if leftValue == rightValue {
			continue
		}
		if objective.Direction == Maximize {
			return leftValue > rightValue
		}
		return leftValue < rightValue
	}
	return left.Candidate.Index < right.Candidate.Index
}

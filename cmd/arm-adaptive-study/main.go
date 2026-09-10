package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/arahe-dev/emanator"
	"github.com/arahe-dev/opentaguchi"
)

func main() {
	coordinatorURL := flag.String("coordinator-url", "http://127.0.0.1:8080", "remote Emanator coordinator URL")
	timeout := flag.Duration("timeout", 60*time.Minute, "overall adaptive study timeout")
	maxConcurrent := flag.Int("max-concurrent", 0, "maximum candidate workflows in flight; zero means all candidates")
	maxDisplacement := flag.Float64("max-displacement-mm", 1.5, "maximum allowable displacement")
	rounds := flag.Int("rounds", 2, "number of L9 refinement rounds")
	shrinkFactor := flag.Float64("shrink-factor", 0.3, "per-round level-span shrink factor")
	reportPath := flag.String("report", filepath.Join("work", "arm-adaptive-report.json"), "JSON report path")
	flag.Parse()

	executor, err := emanator.NewCoordinatorClient(*coordinatorURL)
	if err != nil {
		log.Fatal(err)
	}
	study := opentaguchi.StudySpec{
		ID:     "arm-adaptive",
		Method: opentaguchi.TaguchiL9,
		Variables: []opentaguchi.Variable{
			{Name: "length_mm", Levels: []float64{180, 200, 220}},
			{Name: "width_mm", Levels: []float64{20, 30, 40}},
			{Name: "height_mm", Levels: []float64{10, 15, 20}},
			{Name: "mesh_size_mm", Levels: []float64{8, 10, 12}},
		},
		Workflow: opentaguchi.WorkflowTemplate{Build: armWorkflow},
		Objectives: []opentaguchi.Objective{{
			Name:      "mass",
			TaskID:    "solve",
			Metric:    "mass_kg",
			Direction: opentaguchi.Minimize,
		}},
		Constraints: []opentaguchi.Constraint{
			{Name: "factor_of_safety", TaskID: "solve", Metric: "factor_of_safety", Operator: opentaguchi.GreaterEqual, Limit: 2.5},
			{Name: "displacement", TaskID: "solve", Metric: "max_displacement_mm", Operator: opentaguchi.LessEqual, Limit: *maxDisplacement},
		},
	}
	policy, err := opentaguchi.NewSuccessiveRefinementPolicy(study, opentaguchi.RefinementPolicyOptions{
		Rounds:               *rounds,
		ShrinkFactor:         *shrinkFactor,
		ClampToInitialBounds: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, runErr := opentaguchi.RunAdaptiveStudy(ctx, study, policy, executor, opentaguchi.AdaptiveRunOptions{MaxConcurrent: *maxConcurrent})
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		log.Fatal(marshalErr)
	}
	reportFile, err := filepath.Abs(*reportPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(reportFile), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(reportFile, append(encoded, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("study=%s rounds=%d candidates=%d report=%s\n", report.StudyID, len(report.Rounds), len(report.Candidates), reportFile)
	for _, round := range report.Rounds {
		if len(round.Candidates) == 0 {
			continue
		}
		best := round.Candidates[0]
		fmt.Printf("round=%d best=%s mass_kg=%.9g fos=%.9g displacement_mm=%.9g feasible=%t\n", round.Number, best.Candidate.ID, best.ObjectiveValues["mass"], best.Metrics["solve.factor_of_safety"], best.Metrics["solve.max_displacement_mm"], best.Feasible)
	}
	if report.Best != nil {
		fmt.Printf("best=%s mass_kg=%.9g fos=%.9g displacement_mm=%.9g\n", report.Best.Candidate.ID, report.Best.ObjectiveValues["mass"], report.Best.Metrics["solve.factor_of_safety"], report.Best.Metrics["solve.max_displacement_mm"])
	}
	if runErr != nil {
		log.Printf("adaptive study completed with error: %v", runErr)
		os.Exit(1)
	}
}

func armWorkflow(studyID string, candidate opentaguchi.Candidate) (emanator.WorkflowSpec, error) {
	params := emanator.DefaultArmParameters()
	params.LengthMM = candidate.Values["length_mm"]
	params.WidthMM = candidate.Values["width_mm"]
	params.HeightMM = candidate.Values["height_mm"]
	params.MeshSizeMM = candidate.Values["mesh_size_mm"]
	encoded, err := json.Marshal(params)
	if err != nil {
		return emanator.WorkflowSpec{}, err
	}
	return emanator.WorkflowSpec{
		ID: fmt.Sprintf("%s-%s", studyID, candidate.ID),
		Tasks: []emanator.WorkflowTask{
			{ID: "geometry", Tool: "cadquery", Resources: emanator.ResourceRequest{CPU: 1, RAMMB: 2048}, Params: encoded},
			{ID: "mesh", Tool: "gmsh", Resources: emanator.ResourceRequest{CPU: 2, RAMMB: 4096}, Params: encoded, DependsOn: []string{"geometry"}, ArtifactInputs: []emanator.WorkflowArtifactInput{{Name: "geometry.step", FromTask: "geometry", ArtifactName: "geometry.step"}}},
			{ID: "solve", Tool: "calculix", Resources: emanator.ResourceRequest{CPU: 4, RAMMB: 8192}, Params: encoded, DependsOn: []string{"mesh"}, ArtifactInputs: []emanator.WorkflowArtifactInput{{Name: "mesh.msh", FromTask: "mesh", ArtifactName: "mesh.msh"}}},
		},
	}, nil
}

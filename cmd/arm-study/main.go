package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/arahe-dev/emanator"
	"github.com/arahe-dev/opentaguchi"
)

const defaultDisplacementLimitMM = 1.5

func main() {
	coordinatorURL := flag.String("coordinator-url", "http://127.0.0.1:8080", "remote Emanator coordinator URL")
	timeout := flag.Duration("timeout", 30*time.Minute, "overall study timeout")
	maxConcurrent := flag.Int("max-concurrent", 0, "maximum candidate workflows in flight; zero means all nine")
	displacementLimit := flag.Float64("max-displacement-mm", defaultDisplacementLimitMM, "maximum allowable displacement")
	flag.Parse()

	executor, err := emanator.NewCoordinatorClient(*coordinatorURL)
	if err != nil {
		log.Fatal(err)
	}
	study := opentaguchi.StudySpec{
		ID:     "arm-l9",
		Method: opentaguchi.TaguchiL9,
		Variables: []opentaguchi.Variable{
			{Name: "length_mm", Levels: []float64{180, 200, 220}},
			{Name: "width_mm", Levels: []float64{25, 30, 35}},
			{Name: "height_mm", Levels: []float64{10, 12, 14}},
			{Name: "mesh_size_mm", Levels: []float64{6, 8, 10}},
		},
		Workflow:   opentaguchi.WorkflowTemplate{Build: armWorkflow},
		Objectives: []opentaguchi.Objective{{Name: "mass", TaskID: "solve", Metric: "mass_kg", Direction: opentaguchi.Minimize}},
		Constraints: []opentaguchi.Constraint{
			{Name: "factor_of_safety", TaskID: "solve", Metric: "factor_of_safety", Operator: opentaguchi.GreaterEqual, Limit: 2.5},
			{Name: "displacement", TaskID: "solve", Metric: "max_displacement_mm", Operator: opentaguchi.LessEqual, Limit: *displacementLimit},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, err := opentaguchi.RunStudy(ctx, study, executor, opentaguchi.RunOptions{MaxConcurrent: *maxConcurrent})
	if err != nil {
		log.Printf("study completed with error: %v", err)
	}
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		log.Fatal(marshalErr)
	}
	fmt.Println(string(encoded))
	if report.Best != nil {
		fmt.Printf("best=%s mass_kg=%.9g\n", report.Best.Candidate.ID, report.Best.ObjectiveValues["mass"])
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
	workflowID := fmt.Sprintf("%s-%s", studyID, candidate.ID)
	return emanator.WorkflowSpec{
		ID: workflowID,
		Tasks: []emanator.WorkflowTask{
			{ID: "geometry", Tool: "cadquery", Resources: emanator.ResourceRequest{CPU: 1, RAMMB: 2048}, Params: encoded},
			{ID: "mesh", Tool: "gmsh", Resources: emanator.ResourceRequest{CPU: 2, RAMMB: 4096}, Params: encoded, DependsOn: []string{"geometry"}, ArtifactInputs: []emanator.WorkflowArtifactInput{{Name: "geometry.step", FromTask: "geometry", ArtifactName: "geometry.step"}}},
			{ID: "solve", Tool: "calculix", Resources: emanator.ResourceRequest{CPU: 4, RAMMB: 8192}, Params: encoded, DependsOn: []string{"mesh"}, ArtifactInputs: []emanator.WorkflowArtifactInput{{Name: "mesh.msh", FromTask: "mesh", ArtifactName: "mesh.msh"}}},
		},
	}, nil
}

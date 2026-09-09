package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/arahe-dev/emanator"
	"github.com/arahe-dev/opentaguchi"
)

func main() {
	su2Executable := flag.String("su2-exe", filepath.Join("work", "su2-dist", "package", "win64-omp", "bin", "SU2_CFD.exe"), "SU2_CFD executable")
	templatePath := flag.String("template", filepath.Join("examples", "su2", "naca0012", "inv_NACA0012.cfg"), "SU2 configuration template")
	meshPath := flag.String("mesh", filepath.Join("examples", "su2", "naca0012", "mesh_NACA0012_inv.su2"), "NACA0012 SU2 mesh")
	outputRoot := flag.String("output-root", filepath.Join("work", "su2-study"), "study output and artifact root")
	reportPath := flag.String("report", "", "JSON report path; defaults to <output-root>/report.json")
	iterations := flag.Int("iterations", 250, "SU2 iterations per candidate")
	minLift := flag.Float64("min-lift", 0.10, "minimum lift coefficient constraint")
	maxConcurrent := flag.Int("max-concurrent", 3, "maximum candidate workflows in flight")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall study timeout")
	flag.Parse()

	executable, err := filepath.Abs(*su2Executable)
	if err != nil {
		log.Fatal(err)
	}
	template, err := filepath.Abs(*templatePath)
	if err != nil {
		log.Fatal(err)
	}
	mesh, err := filepath.Abs(*meshPath)
	if err != nil {
		log.Fatal(err)
	}
	root, err := filepath.Abs(*outputRoot)
	if err != nil {
		log.Fatal(err)
	}
	if *reportPath == "" {
		*reportPath = filepath.Join(root, "report.json")
	} else if !filepath.IsAbs(*reportPath) {
		*reportPath, err = filepath.Abs(*reportPath)
		if err != nil {
			log.Fatal(err)
		}
	}

	runner := SU2Runner{
		Executable:   executable,
		TemplatePath: template,
		OutputRoot:   root,
		Iterations:   *iterations,
	}
	coordinator, cleanup, err := startWorkers(emanator.ToolRegistry{"su2_cfd": runner}, root)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	study := opentaguchi.StudySpec{
		ID:     "su2-naca0012-l9",
		Method: opentaguchi.TaguchiL9,
		Variables: []opentaguchi.Variable{
			{Name: "mach", Levels: []float64{0.70, 0.80, 0.90}},
			{Name: "aoa_deg", Levels: []float64{1.0, 2.0, 3.0}},
			{Name: "cfl_number", Levels: []float64{2.0, 5.0, 10.0}},
		},
		Workflow: opentaguchi.WorkflowTemplate{Build: func(studyID string, candidate opentaguchi.Candidate) (emanator.WorkflowSpec, error) {
			return su2Workflow(studyID, candidate, mesh)
		}},
		Objectives: []opentaguchi.Objective{{
			Name:      "lift_to_drag",
			TaskID:    "cfd",
			Metric:    "lift_to_drag",
			Direction: opentaguchi.Maximize,
		}},
		Constraints: []opentaguchi.Constraint{{
			Name:     "lift",
			TaskID:   "cfd",
			Metric:   "lift_coefficient",
			Operator: opentaguchi.GreaterEqual,
			Limit:    *minLift,
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, runErr := opentaguchi.RunStudy(ctx, study, opentaguchi.CoordinatorExecutor{Coordinator: coordinator}, opentaguchi.RunOptions{MaxConcurrent: *maxConcurrent})
	encoded, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		log.Fatal(marshalErr)
	}
	if err := os.MkdirAll(filepath.Dir(*reportPath), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*reportPath, append(encoded, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("study=%s method=%s candidates=%d report=%s\n", report.StudyID, report.Method, len(report.Candidates), *reportPath)
	for _, result := range report.Candidates {
		fmt.Printf("rank=%d id=%s mach=%.3g aoa_deg=%.3g cfl=%.3g lift_to_drag=%.9g drag=%.9g lift=%.9g feasible=%t", result.Rank, result.Candidate.ID, result.Candidate.Values["mach"], result.Candidate.Values["aoa_deg"], result.Candidate.Values["cfl_number"], result.ObjectiveValues["lift_to_drag"], result.Metrics["cfd.drag_coefficient"], result.Metrics["cfd.lift_coefficient"], result.Feasible)
		if result.Error != "" {
			fmt.Printf(" error=%q", result.Error)
		}
		fmt.Println()
	}
	if report.Best != nil {
		fmt.Printf("best=%s lift_to_drag=%.9g drag=%.9g lift=%.9g\n", report.Best.Candidate.ID, report.Best.ObjectiveValues["lift_to_drag"], report.Best.Metrics["cfd.drag_coefficient"], report.Best.Metrics["cfd.lift_coefficient"])
	}
	if runErr != nil {
		log.Printf("study completed with error: %v", runErr)
		os.Exit(1)
	}
}

func su2Workflow(studyID string, candidate opentaguchi.Candidate, meshPath string) (emanator.WorkflowSpec, error) {
	params, err := json.Marshal(map[string]float64{
		"mach":       candidate.Values["mach"],
		"aoa_deg":    candidate.Values["aoa_deg"],
		"cfl_number": candidate.Values["cfl_number"],
	})
	if err != nil {
		return emanator.WorkflowSpec{}, err
	}
	return emanator.WorkflowSpec{
		ID: fmt.Sprintf("%s-%s", studyID, candidate.ID),
		Tasks: []emanator.WorkflowTask{{
			ID:   "cfd",
			Tool: "su2_cfd",
			Resources: emanator.ResourceRequest{
				CPU:   1,
				RAMMB: 1024,
			},
			Params: params,
			Inputs: []emanator.ArtifactRef{{
				Name:      filepath.Base(meshPath),
				URI:       meshPath,
				MediaType: "model/mesh",
			}},
		}},
	}, nil
}

func startWorkers(registry emanator.ToolRegistry, outputRoot string) (*emanator.Coordinator, func(), error) {
	workers := make([]*emanator.Worker, 0, 3)
	servers := make([]*httptest.Server, 0, 3)
	for index := 1; index <= 3; index++ {
		id := fmt.Sprintf("su2-worker-%d", index)
		worker, err := emanator.NewWorker(emanator.WorkerConfig{
			ID:           id,
			CPU:          1,
			RAMMB:        4096,
			Registry:     registry,
			ArtifactRoot: filepath.Join(outputRoot, "artifacts", id),
		})
		if err != nil {
			return nil, func() {}, err
		}
		server := httptest.NewServer(worker)
		if err := worker.SetEndpoint(server.URL); err != nil {
			server.Close()
			return nil, func() {}, err
		}
		workers = append(workers, worker)
		servers = append(servers, server)
	}
	ads := make([]emanator.WorkerInfo, 0, len(workers))
	for _, worker := range workers {
		ads = append(ads, worker.Info())
	}
	coordinator := emanator.NewCoordinator(ads)
	cleanup := func() {
		for _, server := range servers {
			server.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, worker := range workers {
			if err := worker.Close(ctx); err != nil {
				log.Printf("close %s: %v", worker.Info().ID, err)
			}
		}
	}
	return coordinator, cleanup, nil
}

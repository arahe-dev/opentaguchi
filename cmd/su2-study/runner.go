package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/arahe-dev/emanator"
)

// SU2Runner adapts one SU2_CFD executable to Emanator's solver-independent
// Runner contract. It reports only final drag/lift/L-D metrics and small text
// artifacts; large restart/volume files stay in the worker.
type SU2Runner struct {
	Executable   string
	TemplatePath string
	OutputRoot   string
	Iterations   int
}

type su2Parameters struct {
	Mach      float64 `json:"mach"`
	AoADeg    float64 `json:"aoa_deg"`
	CFLNumber float64 `json:"cfl_number"`
}

func (r SU2Runner) Run(ctx context.Context, job emanator.JobSpec) (emanator.RunnerResult, error) {
	params := su2Parameters{}
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return emanator.RunnerResult{}, fmt.Errorf("decode SU2 params: %w", err)
	}
	if params.Mach <= 0 || params.CFLNumber <= 0 {
		return emanator.RunnerResult{}, errors.New("SU2 mach and cfl_number must be positive")
	}
	if strings.TrimSpace(r.Executable) == "" || strings.TrimSpace(r.TemplatePath) == "" {
		return emanator.RunnerResult{}, errors.New("SU2 executable and template are required")
	}
	meshPath, meshName, err := meshInput(job)
	if err != nil {
		return emanator.RunnerResult{}, err
	}
	workspace := filepath.Join(r.OutputRoot, safePathPart(job.ID))
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return emanator.RunnerResult{}, fmt.Errorf("create SU2 workspace: %w", err)
	}
	localMesh := filepath.Join(workspace, meshName)
	if err := copyFile(meshPath, localMesh); err != nil {
		return emanator.RunnerResult{}, fmt.Errorf("copy mesh: %w", err)
	}
	template, err := os.ReadFile(r.TemplatePath)
	if err != nil {
		return emanator.RunnerResult{}, fmt.Errorf("read SU2 template: %w", err)
	}
	config := string(template)
	config = setConfigValue(config, "MESH_FILENAME", meshName)
	config = setConfigValue(config, "MACH_NUMBER", formatFloat(params.Mach))
	config = setConfigValue(config, "AOA", formatFloat(params.AoADeg))
	config = setConfigValue(config, "CFL_NUMBER", formatFloat(params.CFLNumber))
	iterations := r.Iterations
	if iterations <= 0 {
		iterations = 60
	}
	config = setConfigValue(config, "ITER", strconv.Itoa(iterations))
	config = setConfigValue(config, "HISTORY_OUTPUT", "(ITER, RMS_RES, AERO_COEFF)")
	config = setConfigValue(config, "CONV_FILENAME", "history")
	config = setConfigValue(config, "RESTART_FILENAME", "restart")
	config = setConfigValue(config, "SOLUTION_FILENAME", "solution_flow")
	config = setConfigValue(config, "VOLUME_FILENAME", "flow")
	config = setConfigValue(config, "SURFACE_FILENAME", "surface_flow")
	config = setConfigValue(config, "OUTPUT_FILES", "(RESTART)")
	configPath := filepath.Join(workspace, "case.cfg")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		return emanator.RunnerResult{}, fmt.Errorf("write SU2 config: %w", err)
	}
	command := exec.CommandContext(ctx, r.Executable, filepath.Base(configPath))
	command.Dir = workspace
	command.Env = append(os.Environ(), "OMP_NUM_THREADS=1")
	output, runErr := command.CombinedOutput()
	logPath := filepath.Join(workspace, "solver.log")
	if err := os.WriteFile(logPath, output, 0o644); err != nil {
		return emanator.RunnerResult{}, fmt.Errorf("write SU2 log: %w", err)
	}
	if runErr != nil {
		return emanator.RunnerResult{}, fmt.Errorf("SU2_CFD failed: %w", runErr)
	}
	historyPath := filepath.Join(workspace, "history.csv")
	metrics, err := readAeroMetrics(historyPath)
	if err != nil {
		return emanator.RunnerResult{}, err
	}
	if metrics["drag_coefficient"] == 0 {
		return emanator.RunnerResult{}, errors.New("SU2 history has zero drag; cannot compute lift-to-drag")
	}
	metrics["lift_to_drag"] = metrics["lift_coefficient"] / metrics["drag_coefficient"]
	return emanator.RunnerResult{
		Metrics: metrics,
		Artifacts: []emanator.ArtifactRef{
			{Name: "su2-history.csv", URI: historyPath, MediaType: "text/csv"},
			{Name: "su2-config.cfg", URI: configPath, MediaType: "text/plain"},
			{Name: "su2-solver.log", URI: logPath, MediaType: "text/plain"},
		},
	}, nil
}

func meshInput(job emanator.JobSpec) (string, string, error) {
	for _, input := range job.Inputs {
		path := input.URI
		if strings.HasPrefix(path, "file://") {
			path = strings.TrimPrefix(path, "file://")
			path = strings.TrimPrefix(path, "/")
		}
		if strings.HasSuffix(strings.ToLower(path), ".su2") || strings.HasSuffix(strings.ToLower(input.Name), ".su2") {
			if path == "" {
				return "", "", fmt.Errorf("mesh input %q has empty uri", input.Name)
			}
			return path, filepath.Base(path), nil
		}
	}
	return "", "", errors.New("SU2 job has no .su2 mesh input")
}

func setConfigValue(config, key, value string) string {
	pattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=.*$`)
	line := key + "= " + value
	if pattern.MatchString(config) {
		return pattern.ReplaceAllString(config, line)
	}
	return strings.TrimRight(config, "\r\n") + "\n" + line + "\n"
}

func readAeroMetrics(path string) (map[string]float64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open SU2 history: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read SU2 history header: %w", err)
		}
		return nil, errors.New("SU2 history has no header")
	}
	header := strings.Split(scanner.Text(), ",")
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.ToUpper(strings.Trim(strings.TrimSpace(name), "\""))] = index
	}
	cdColumn, cdOK := columns["CD"]
	clColumn, clOK := columns["CL"]
	if !cdOK || !clOK {
		return nil, fmt.Errorf("SU2 history has no CD/CL columns")
	}
	var last []string
	for scanner.Scan() {
		record := strings.Split(scanner.Text(), ",")
		if len(record) > cdColumn && len(record) > clColumn {
			last = record
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SU2 history: %w", err)
	}
	if last == nil {
		return nil, errors.New("SU2 history has no data rows")
	}
	parse := func(column int) (float64, error) {
		value, parseErr := strconv.ParseFloat(strings.TrimSpace(last[column]), 64)
		if parseErr != nil {
			return 0, parseErr
		}
		return value, nil
	}
	drag, err := parse(cdColumn)
	if err != nil {
		return nil, fmt.Errorf("parse SU2 CD: %w", err)
	}
	lift, err := parse(clColumn)
	if err != nil {
		return nil, fmt.Errorf("parse SU2 CL: %w", err)
	}
	return map[string]float64{
		"drag_coefficient": drag,
		"lift_coefficient": lift,
	}, nil
}

func copyFile(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	return destination.Close()
}

func safePathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "job"
	}
	return strings.Map(func(char rune) rune {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			return char
		}
		return '_'
	}, value)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

# OpenTaguchi

OpenTaguchi is a small Go SDK for asynchronous design-of-experiments over
[Emanator](https://github.com/arahe-dev/emanator). It generates deterministic
Taguchi L9/L27 or full-factorial candidates, compiles each candidate into an
Emanator workflow, waits for distributed execution, evaluates objective and
constraint metrics, and returns a ranked report.

## First proof

`cmd/arm-study` runs a four-variable, three-level Taguchi L9 for the existing
CadQuery → Gmsh → CalculiX arm chain: nine candidates and 27 distributed jobs.
It minimizes `mass_kg`, subject to factor of safety ≥ 2.5 and displacement ≤
0.01 mm by default. Start an Emanator coordinator and workers, then run:

```text
go run ./cmd/arm-study -coordinator-url http://coordinator:8080
```

Use `-max-concurrent` to bound the number of candidate workflows submitted at
once. The workflow builder is deliberately a Go callback, so any solver DAG
can be used without making OpenTaguchi aware of domain-specific parameters.

## SDK shape

```go
report, err := opentaguchi.RunStudy(ctx, spec, executor, opentaguchi.RunOptions{})
```

An `emanator.CoordinatorClient` directly implements `WorkflowExecutor`.
`opentaguchi.CoordinatorExecutor` adapts an in-process coordinator.

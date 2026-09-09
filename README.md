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
1.5 mm by default. This is a physically meaningful demo threshold for the
current roughly 200 mm, 1 kN cantilever example; provide
`-max-displacement-mm` when a different design limit is appropriate. Start an
Emanator coordinator and workers, then run:

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

## Cross-domain proof

`cmd/su2-study` runs the same frozen core against a real SU2 NACA0012 Euler
case: nine L9 CFD workflows on three Emanator workers, maximizing lift-to-drag
subject to a lift constraint. See [`examples/su2`](examples/su2) for the
official solver download and run instructions.

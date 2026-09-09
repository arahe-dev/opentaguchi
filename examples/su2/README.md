# SU2 cross-domain proof

`cmd/su2-study` runs the frozen OpenTaguchi core against a real SU2 CFD
solver. It generates a deterministic Taguchi L9, compiles each candidate into
one Emanator workflow, executes the workflows on three local Emanator workers,
collects SU2 `CD`/`CL` history metrics, and ranks by maximum lift-to-drag
subject to a lift constraint.

The case is the official SU2 NACA0012 Euler Quick Start mesh/config. The
committed mesh is about 485 KB. The SU2 executable is intentionally kept out
of Git; download the official Windows binary into `work/su2-dist` first:

```powershell
gh release download v8.5.0 --repo su2code/SU2 `
  --pattern 'SU2-v8.5.0-win64-omp.zip' --dir work/su2-dist
Expand-Archive work/su2-dist/SU2-v8.5.0-win64-omp.zip -DestinationPath work/su2-dist/package
Expand-Archive work/su2-dist/package/win64-omp.zip -DestinationPath work/su2-dist/package/win64-omp
```

Run the nine real CFD jobs from the repository root:

```powershell
go run ./cmd/su2-study
```

The JSON report and per-candidate solver logs/history files are written under
`work/su2-study`. The three DOE variables are Mach number `[0.70, 0.80, 0.90]`,
angle of attack `[1, 2, 3]` degrees, and CFL number `[2, 5, 10]`.

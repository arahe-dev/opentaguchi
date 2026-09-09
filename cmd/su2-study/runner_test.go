package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAeroMetricsParsesSU2PaddedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.csv")
	content := `"Time_Iter","Outer_Iter","Inner_Iter",    "rms[Rho]"    ,    "RefForce"    ,       "CD"       ,       "CL"
          0,           0,           0,      -3.5,             0.343,       0.12,       0.20
          0,           0,           1,      -3.6,             0.343,       0.08,       0.24
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	metrics, err := readAeroMetrics(path)
	if err != nil {
		t.Fatal(err)
	}
	if metrics["drag_coefficient"] != 0.08 || metrics["lift_coefficient"] != 0.24 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestSetConfigValueDoesNotReplaceInnerIter(t *testing.T) {
	config := "INNER_ITER= 9999\nITER= 250\n"
	updated := setConfigValue(config, "ITER", "60")
	if updated != "INNER_ITER= 9999\nITER= 60\n" {
		t.Fatalf("unexpected config: %q", updated)
	}
}

package main

import "testing"

func TestDefaultDisplacementLimitIsDemoScale(t *testing.T) {
	if defaultDisplacementLimitMM != 1.5 {
		t.Fatalf("default displacement limit = %v, want 1.5 mm", defaultDisplacementLimitMM)
	}
}

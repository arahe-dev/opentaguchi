package opentaguchi

import (
	"fmt"
	"math"
	"strings"
)

// Method selects the design-of-experiments generator used by a study.
type Method string

const (
	TaguchiL9           Method = "taguchi_l9"
	TaguchiL27          Method = "taguchi_l27"
	FullFactorial       Method = "full_factorial"
	MethodTaguchiL9            = TaguchiL9
	MethodTaguchiL27           = TaguchiL27
	MethodFullFactorial        = FullFactorial
)

// Variable is one tunable parameter and its ordered candidate levels.
type Variable struct {
	Name   string    `json:"name"`
	Levels []float64 `json:"levels"`
}

// Candidate is one deterministic row of a generated design.
type Candidate struct {
	ID     string             `json:"id"`
	Index  int                `json:"index"`
	Values map[string]float64 `json:"values"`
}

// Value returns a named parameter value.
func (c Candidate) Value(name string) (float64, bool) {
	value, ok := c.Values[name]
	return value, ok
}

// GenerateCandidates expands variables into a deterministic DOE. Taguchi
// methods require three levels and use the standard L9 or L27 orthogonal
// arrays. FullFactorial enumerates the cartesian product in input order.
func GenerateCandidates(method Method, variables []Variable) ([]Candidate, error) {
	if err := validateVariables(method, variables); err != nil {
		return nil, err
	}
	var rows [][]int
	switch method {
	case MethodTaguchiL9:
		rows = l9Rows
	case MethodTaguchiL27:
		rows = l27Rows()
	case MethodFullFactorial:
		return fullFactorial(variables), nil
	default:
		return nil, fmt.Errorf("unsupported DOE method %q", method)
	}
	candidates := make([]Candidate, 0, len(rows))
	for index, row := range rows {
		values := make(map[string]float64, len(variables))
		for column, variable := range variables {
			values[variable.Name] = variable.Levels[row[column]]
		}
		candidates = append(candidates, Candidate{ID: candidateID(index), Index: index, Values: values})
	}
	return candidates, nil
}

func validateVariables(method Method, variables []Variable) error {
	if len(variables) == 0 {
		return fmt.Errorf("at least one DOE variable is required")
	}
	maxColumns := 0
	switch method {
	case MethodTaguchiL9:
		maxColumns = 4
	case MethodTaguchiL27:
		maxColumns = 13
	case MethodFullFactorial:
	default:
		return fmt.Errorf("unsupported DOE method %q", method)
	}
	if maxColumns > 0 && len(variables) > maxColumns {
		return fmt.Errorf("%s supports at most %d variables, got %d", method, maxColumns, len(variables))
	}
	seen := make(map[string]struct{}, len(variables))
	for _, variable := range variables {
		name := strings.TrimSpace(variable.Name)
		if name == "" {
			return fmt.Errorf("DOE variable name must not be empty")
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("DOE variable %q is repeated", name)
		}
		seen[name] = struct{}{}
		if len(variable.Levels) == 0 {
			return fmt.Errorf("DOE variable %q must have at least one level", name)
		}
		for _, level := range variable.Levels {
			if math.IsNaN(level) || math.IsInf(level, 0) {
				return fmt.Errorf("DOE variable %q contains a non-finite level", name)
			}
		}
		if (method == MethodTaguchiL9 || method == MethodTaguchiL27) && len(variable.Levels) != 3 {
			return fmt.Errorf("%s requires exactly three levels per variable; %q has %d", method, name, len(variable.Levels))
		}
	}
	return nil
}

func candidateID(index int) string {
	return fmt.Sprintf("candidate-%02d", index+1)
}

// The first four columns of the conventional 3-level L9 array.
var l9Rows = [][]int{
	{0, 0, 0, 0},
	{0, 1, 1, 1},
	{0, 2, 2, 2},
	{1, 0, 1, 2},
	{1, 1, 2, 0},
	{1, 2, 0, 1},
	{2, 0, 2, 1},
	{2, 1, 0, 2},
	{2, 2, 1, 0},
}

// l27Rows constructs the standard 3^13 array from the 13 projective lines
// in GF(3)^3. Any two distinct columns are orthogonal, and the construction
// is less error-prone than maintaining a 27-row literal table.
func l27Rows() [][]int {
	columns := [][3]int{
		{1, 0, 0}, {0, 1, 0}, {0, 0, 1},
		{1, 1, 0}, {1, 2, 0},
		{1, 0, 1}, {1, 0, 2},
		{0, 1, 1}, {0, 1, 2},
		{1, 1, 1}, {1, 1, 2}, {1, 2, 1}, {1, 2, 2},
	}
	rows := make([][]int, 0, 27)
	for a := 0; a < 3; a++ {
		for b := 0; b < 3; b++ {
			for c := 0; c < 3; c++ {
				row := make([]int, len(columns))
				for index, column := range columns {
					row[index] = (column[0]*a + column[1]*b + column[2]*c) % 3
				}
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func fullFactorial(variables []Variable) []Candidate {
	rows := make([]Candidate, 0)
	values := make(map[string]float64, len(variables))
	var visit func(int)
	visit = func(variableIndex int) {
		if variableIndex == len(variables) {
			copyValues := make(map[string]float64, len(values))
			for name, value := range values {
				copyValues[name] = value
			}
			rows = append(rows, Candidate{ID: candidateID(len(rows)), Index: len(rows), Values: copyValues})
			return
		}
		variable := variables[variableIndex]
		for _, level := range variable.Levels {
			values[variable.Name] = level
			visit(variableIndex + 1)
		}
		delete(values, variable.Name)
	}
	visit(0)
	return rows
}

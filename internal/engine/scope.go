package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// StepOutput represents the recorded outcome of a single step execution.
type StepOutput struct {
	Status string         `json:"status"`
	Output map[string]any `json:"output"`
}

// Scope is the data a running workflow can see. It is a value: the engine never
// mutates a caller's Scope, and every evaluation is a pure function of it.
//
// Env holds only what the caller explicitly places here. The engine must never
// read os.Environ() — purity_test.go cannot catch that, since os is stdlib.
type Scope struct {
	Trigger   map[string]any        `json:"trigger"`
	Steps     map[string]StepOutput `json:"steps"`
	Variables map[string]any        `json:"variables"`
	Env       map[string]string     `json:"env"`
}

// ExprEnv renders the Scope as a map expr-lang binds against.
// This ensures expressions read "trigger.name" or "steps.s1.output.id"
// without being forced to use Go struct field names like "Trigger.name".
func (s Scope) ExprEnv() map[string]any {
	stepsMap := make(map[string]any, len(s.Steps))
	for k, v := range s.Steps {
		outputMap := v.Output
		if outputMap == nil {
			outputMap = map[string]any{}
		}
		stepsMap[k] = map[string]any{
			"status": v.Status,
			"output": outputMap,
		}
	}

	triggerMap := s.Trigger
	if triggerMap == nil {
		triggerMap = map[string]any{}
	}

	variablesMap := s.Variables
	if variablesMap == nil {
		variablesMap = map[string]any{}
	}

	envMap := make(map[string]any, len(s.Env))
	for k, v := range s.Env {
		envMap[k] = v
	}

	return map[string]any{
		"trigger":   triggerMap,
		"steps":     stepsMap,
		"variables": variablesMap,
		"env":       envMap,
	}
}

var tokenPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_\.]+)\s*\}\}`)

// Interpolate substitutes {{path.to.value}} references in a template string.
// Unresolvable paths return an error, never a silent empty string.
func Interpolate(tmpl string, s Scope) (string, error) {
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}

	// Check for unbalanced braces
	openCount := strings.Count(tmpl, "{{")
	closeCount := strings.Count(tmpl, "}}")
	if openCount != closeCount {
		return "", fmt.Errorf("unbalanced template braces in %q", tmpl)
	}

	env := s.ExprEnv()
	var resolveErr error

	res := tokenPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		if resolveErr != nil {
			return match
		}
		submatches := tokenPattern.FindStringSubmatch(match)
		if len(submatches) < 2 {
			resolveErr = fmt.Errorf("invalid placeholder syntax %q", match)
			return match
		}
		path := submatches[1]

		val, err := resolvePath(env, path)
		if err != nil {
			resolveErr = err
			return match
		}
		return fmt.Sprintf("%v", val)
	})

	if resolveErr != nil {
		return "", resolveErr
	}

	return res, nil
}

func resolvePath(root map[string]any, path string) (any, error) {
	parts := strings.Split(path, ".")
	var current any = root

	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("cannot resolve field %q on non-object in path %q", part, path)
		}
		val, exists := m[part]
		if !exists {
			return nil, fmt.Errorf("unresolvable path %q: missing field %q", path, part)
		}
		current = val
	}

	return current, nil
}

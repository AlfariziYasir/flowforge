package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/expr-lang/expr"
)

// MaxExpressionNodes caps the compiled AST. This is the DoS guard: expr has no loop
// or recursion construct, so evaluation cost is bounded by AST size times data size.
const MaxExpressionNodes = 1000

// ErrExpressionFailed is returned when an expression fails compilation or execution.
var ErrExpressionFailed = errors.New("expression evaluation failed")

// ValidateExpression compiles an expression without running it.
// Useful at publish time to reject invalid syntax before execution.
func ValidateExpression(expression string, s Scope) error {
	_, err := expr.Compile(expression, expr.Env(s.ExprEnv()), expr.MaxNodes(MaxExpressionNodes))
	if err != nil {
		return fmt.Errorf("%w: syntax error: %v", ErrExpressionFailed, sanitizeErr(err))
	}
	return nil
}

// EvaluateCondition compiles and runs a CONDITION expression, asserting a boolean result.
// ctx is checked before evaluation; evaluation itself is in-memory and bounded by MaxExpressionNodes.
func EvaluateCondition(ctx context.Context, expression string, s Scope) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	program, err := expr.Compile(expression, expr.Env(s.ExprEnv()), expr.AsBool(), expr.MaxNodes(MaxExpressionNodes))
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrExpressionFailed, sanitizeErr(err))
	}

	if err := ctx.Err(); err != nil {
		return false, err
	}

	out, err := expr.Run(program, s.ExprEnv())
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrExpressionFailed, sanitizeErr(err))
	}

	res, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("%w: condition expression did not evaluate to boolean", ErrExpressionFailed)
	}

	return res, nil
}

// EvaluateTransform compiles and runs a TRANSFORM expression returning any result.
func EvaluateTransform(ctx context.Context, expression string, s Scope) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	program, err := expr.Compile(expression, expr.Env(s.ExprEnv()), expr.MaxNodes(MaxExpressionNodes))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExpressionFailed, sanitizeErr(err))
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out, err := expr.Run(program, s.ExprEnv())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExpressionFailed, sanitizeErr(err))
	}

	return out, nil
}

func sanitizeErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

package cmdsys

import "context"

// LoopRunner is the contract OnLoop expects from anything that can run
// closures on a game-loop goroutine. *engine.Engine satisfies this, as does
// the fake used in tests.
type LoopRunner interface {
	RunOnLoop(ctx context.Context, fn func() error) error
}

// OnLoop runs fn on the loop goroutine and returns its typed result. Replaces
// the boilerplate
//
//	var x R
//	if err := runner.RunOnLoop(ctx, func() error {
//	    x = ...
//	    return nil
//	}); err != nil {
//	    return zero, err
//	}
//	return x, nil
//
// with a single line:
//
//	return cmdsys.OnLoop[R](ctx, runner, func() (R, error) { ... })
//
// Use from cmdsys command handlers that need ECS access; the *engine.Engine
// stored on each *universe.Cell satisfies LoopRunner.
func OnLoop[R any](ctx context.Context, runner LoopRunner, fn func() (R, error)) (R, error) {
	var (
		result R
		inner  error
	)
	err := runner.RunOnLoop(ctx, func() error {
		result, inner = fn()
		return inner
	})
	if err != nil {
		var zero R
		return zero, err
	}
	return result, nil
}

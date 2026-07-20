package cascade

import (
	"fmt"
	"strings"
)

// rooted is implemented by cascade errors that can name the node where a
// failure originated, so that propagation can carry the root cause downstream.
type rooted[K comparable] interface {
	rootKey() K
}

// CycleError reports that a [Graph.SetFormula] call was rejected because the
// declared dependencies would close a cycle. The graph is left unchanged.
type CycleError[K comparable] struct {
	path []K
}

// Path returns the offending cycle, starting and ending at the same key —
// for example A, B, C, A means "A depends on B depends on C depends on A".
// The returned slice is a copy and may be modified freely.
func (e *CycleError[K]) Path() []K {
	out := make([]K, len(e.path))
	copy(out, e.path)
	return out
}

func (e *CycleError[K]) Error() string {
	parts := make([]string, len(e.path))
	for i, k := range e.path {
		parts[i] = fmt.Sprint(k)
	}
	return "cascade: cycle: " + strings.Join(parts, " -> ")
}

// UnresolvedError reports a reference to a key that is not defined in the
// graph. Missing is the undefined key; Key is the node that referenced it.
// When a key is looked up directly with [Graph.Get] and does not exist, Key
// and Missing are the same.
//
// Declaring a dependency on a key that does not exist yet is legal — forward
// references are supported — but evaluating such a node before the key is
// defined yields this error. Defining the key later marks dependents stale and
// they resolve normally.
type UnresolvedError[K comparable] struct {
	Key     K
	Missing K
}

func (e *UnresolvedError[K]) Error() string {
	if e.Key == e.Missing {
		return fmt.Sprintf("cascade: unresolved: key %v is not defined", e.Key)
	}
	return fmt.Sprintf("cascade: unresolved: %v references undefined key %v", e.Key, e.Missing)
}

func (e *UnresolvedError[K]) rootKey() K { return e.Key }

// EvalError reports that a node failed to compute. Key is the node holding
// this error; Cause is the node where the failure actually originated, which
// equals Key for a direct failure and names the upstream node for a
// propagated one — the spreadsheet #VALUE!/#REF! model.
//
// If the compute function panicked, Panic is true and PanicValue holds the
// recovered value. [EvalError.Unwrap] exposes the underlying error, so
// errors.Is and errors.As reach through a whole propagation chain to the
// original failure.
type EvalError[K comparable] struct {
	Key        K
	Cause      K
	Panic      bool
	PanicValue any
	Err        error
}

func (e *EvalError[K]) Unwrap() error { return e.Err }

func (e *EvalError[K]) Error() string {
	switch {
	case e.Panic:
		return fmt.Sprintf("cascade: eval %v: panic: %v", e.Key, e.PanicValue)
	case e.Cause == e.Key:
		return fmt.Sprintf("cascade: eval %v: %v", e.Key, e.Err)
	default:
		return fmt.Sprintf("cascade: eval %v: upstream %v failed: %v", e.Key, e.Cause, e.Err)
	}
}

func (e *EvalError[K]) rootKey() K { return e.Cause }

// UndeclaredError reports that a formula tried to read a key it did not
// declare as a dependency. Reads are restricted to declared dependencies so
// that the graph's edges always describe the real data flow.
type UndeclaredError[K comparable] struct {
	Key K
}

func (e *UndeclaredError[K]) Error() string {
	return fmt.Sprintf("cascade: undeclared dependency: %v", e.Key)
}

// Package cascade is a headless dependency-graph recalculation engine: the
// layer a spreadsheet has above its formula language, extracted and made
// generic.
//
// Expression libraries evaluate one expression safely and deliberately stop
// there. cascade owns everything above that — which cells depend on which,
// what order to recompute in, what is stale, and what happens when a formula
// fails — and evaluates nothing itself. You supply the compute function, so a
// formula can be an expr-lang program, a CEL program, or a plain Go closure,
// and cascade depends on none of them.
//
//	import "github.com/zkrebbekx/cascade"
//
// # Design
//
// A [Graph] holds keyed nodes. A node is either a value — a constant input you
// set — or a formula: a list of declared precedents plus a function that turns
// their values into one value.
//
//	g := cascade.New[string, float64]()
//	g.SetValue("qty", 3)
//	g.SetValue("unit", 19.99)
//	_ = g.SetFormula("net", []string{"qty", "unit"}, func(in cascade.Inputs[string, float64]) (float64, error) {
//		return in.MustGet("qty") * in.MustGet("unit"), nil
//	})
//	net, err := g.Get("net") // 59.97
//
// A formula reads its inputs through [Inputs], which only exposes the keys it
// declared. Reaching for an undeclared key fails with [UndeclaredError]
// instead of quietly succeeding, so the recorded edges stay an honest
// description of the data flow — the graph cannot drift away from the code.
//
// # Dirty propagation
//
// Mutating the graph computes nothing. [Graph.SetValue], [Graph.SetFormula]
// and [Graph.Delete] mark the affected node and all of its transitive
// dependents stale, and stop. Work happens on demand: [Graph.Get] pulls,
// computing the node and only the stale precedents it needs, while
// [Graph.Recalc] pushes, settling every stale node in one pass.
// [Graph.RecalcFrom] settles just the region reachable from some keys.
//
// Clean nodes are never recomputed, and a node reachable from a change by
// several paths is recomputed exactly once. In a diamond where B and C both
// read A and D reads both B and C, changing A recomputes B, C and D once each
// — D is not computed twice. [Graph.OnCompute] reports every node the engine
// actually computed, which makes that auditable rather than assumed.
//
// # Topological order
//
// Recalculation runs in dependency order, so a formula never observes a stale
// precedent. The order is deterministic for a given construction sequence: no
// map is iterated on any path that produces output, and both the sort and the
// dirty marking use explicit stacks rather than recursion, so a chain hundreds
// of thousands of nodes deep is bounded by the heap and cannot overflow the
// goroutine stack. [Graph.TopoOrder] exposes the same ordering for inspection.
//
// Keys are only constrained to be comparable, so cascade cannot sort them.
// Deterministic here means definition order, not sorted order; sort the result
// yourself if you need that.
//
// # Cycles
//
// [Graph.SetFormula] rejects any edge that would close a cycle and returns a
// [CycleError] carrying the path, so the graph can never enter a state that
// cannot be evaluated. A rejected call leaves the graph exactly as it was — no
// partial mutation, so error handling never has to unwind anything.
//
// # Error semantics
//
// Failure is a value, not an exception, and it flows along the edges like any
// other value. A formula returning an error leaves that error on its node, and
// every transitive dependent fails with an [EvalError] naming both itself and
// the node where the failure originated — the spreadsheet #VALUE!/#REF! model,
// where a broken cell is visible from downstream and you can still see which
// input actually broke. [EvalError.Unwrap] reaches through the whole chain, so
// errors.Is and errors.As find the original cause.
//
// Errors are cached exactly like values: a failed node is not retried until it
// or one of its precedents is marked stale again.
//
// Declaring a dependency on a key that does not exist yet is legal — forward
// references are how you build a graph in any order — but evaluating such a
// node before the key is defined yields an [UnresolvedError]. Defining the key
// later marks the dependents stale and they resolve normally. Deleting a node
// that has dependents puts them back into exactly that state rather than
// silently substituting a zero value.
//
// A compute function that panics is contained: the panic is recovered and
// becomes an [EvalError] with Panic set and the recovered value preserved.
// Formulas are frequently user-authored, and a bad one must fail its own node,
// not the process.
//
// # Concurrency
//
// A *Graph is not safe for concurrent use, and cascade deliberately ships no
// mutex-wrapped variant — locking belongs where the transaction boundaries
// are, which is your code, not here. Note that [Graph.Get] can compute and
// therefore writes, so concurrent Get calls race just as concurrent mutations
// do. Guard a shared Graph with your own lock, or give each goroutine its own
// via [Graph.Clone]. The test suite runs clean under the race detector.
package cascade

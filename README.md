# cascade

A headless dependency-graph recalculation engine for Go — the layer a
spreadsheet has above its formula language, extracted and made generic.

Go has excellent expression evaluators. `expr-lang/expr` and `cel-go` will run
one expression, sandboxed and fast, and they deliberately stop there. But an
application that needs *recalculation* — a pricing model, a metrics rollup, a
rules engine, a live dashboard — needs the layer above: which value depends on
which, what order to recompute in, what is stale, and what happens when one
formula fails. That layer is what gets rewritten badly in every project, and
it's what `cascade` is.

`cascade` evaluates nothing itself. You supply the compute function, so a
formula can be an expr program, a CEL program, or a plain Go closure — and
`cascade` depends on none of them.

- **Recomputes the minimum.** Mutating the graph marks the affected node and
  its transitive dependents stale and stops there. Clean nodes are never
  recomputed, and a node reachable from a change by several paths is computed
  **exactly once** — the diamond property that naive invalidation gets wrong.
- **Ordered and deterministic.** Recalculation runs in topological order, so a
  formula never observes a stale precedent, and the order is stable across
  runs for a given construction sequence.
- **Cycles are rejected, not discovered at runtime.** `SetFormula` refuses an
  edge that would close a loop and hands back the path — and leaves the graph
  exactly as it was, so error handling never has to unwind anything.
- **Failure is a value.** A failing formula holds its error, and dependents
  fail with a wrapped error naming the node that actually broke — `#VALUE!`
  semantics, not a panic. A formula that panics fails its own node, not your
  process.
- **Deep graphs are safe.** Marking, sorting and evaluation are all iterative.
  A chain of 100,000 nodes is a test case, not a stack overflow.
- **Zero dependencies.** Go 1.23+.

```go
import "github.com/zkrebbekx/cascade"
```

## Example

```go
g := cascade.New[string, float64]()

g.SetValue("qty", 3)
g.SetValue("unit", 19.99)
g.SetValue("taxRate", 0.2)

g.SetFormula("net", []string{"qty", "unit"}, func(in cascade.Inputs[string, float64]) (float64, error) {
    return in.MustGet("qty") * in.MustGet("unit"), nil
})
g.SetFormula("tax", []string{"net", "taxRate"}, func(in cascade.Inputs[string, float64]) (float64, error) {
    return in.MustGet("net") * in.MustGet("taxRate"), nil
})
g.SetFormula("total", []string{"net", "tax"}, func(in cascade.Inputs[string, float64]) (float64, error) {
    return in.MustGet("net") + in.MustGet("tax"), nil
})

total, _ := g.Get("total") // 71.964

g.SetValue("qty", 10)      // marks net, tax and total stale — computes nothing
total, _ = g.Get("total")  // 239.88, recomputing each of the three once
```

Nothing computes on mutation. `Get` pulls what it needs; `Recalc` settles the
whole graph; `RecalcFrom` settles one region.

## Using it with an expression evaluator

`cascade` owns the graph, your evaluator owns the expression. The compute
function is the seam, so wiring `expr` in is a few lines and adds no dependency
to `cascade` itself:

```go
// program is a compiled expr program; deps were extracted from its AST.
g.SetFormula(cell, deps, func(in cascade.Inputs[string, any]) (any, error) {
    env := make(map[string]any, in.Len())
    for _, k := range in.Keys() {
        env[k] = in.MustGet(k)
    }
    return expr.Run(program, env) // or cel Program.Eval, or plain Go
})
```

The environment handed to the evaluator contains exactly the declared
dependencies — nothing else is reachable, by construction.

## Honest edges

A formula reads its inputs through `Inputs`, which only exposes the keys it
declared. Reaching for anything else fails with `*UndeclaredError` instead of
quietly succeeding:

```go
g.SetFormula("net", []string{"qty"}, func(in cascade.Inputs[string, float64]) (float64, error) {
    return in.MustGet("unit"), nil // never declared — fails the node
})
```

This is the invariant that keeps the graph trustworthy. If undeclared reads
were allowed, an edge could be missing from the graph while the dependency was
real in the code, and the engine would silently serve stale values forever.

## Errors

Failure propagates along the edges like any other value.

```go
_, err := g.Get("total")

var ee *cascade.EvalError[string]
if errors.As(err, &ee) {
    fmt.Println(ee.Key)   // "total" — the node you asked for
    fmt.Println(ee.Cause) // "rate"  — the node that actually broke
}
errors.Is(err, io.ErrUnexpectedEOF) // reaches the original error
```

Errors are cached exactly like values: a failed node is not retried until it or
one of its precedents is marked stale again. A `*CycleError` carries the path;
an `*UnresolvedError` names a reference to a key that isn't defined — which is
also what a node's dependents get after that node is `Delete`d, rather than a
silent zero value.

Forward references are legal. Declare a dependency on a key that doesn't exist
yet, define it later, and the dependents go stale and resolve normally — so a
graph can be built in any order.

## Observability

`OnCompute` fires once for every node the engine actually computed, and stays
silent for nodes skipped as clean. That makes "did my change recompute what I
expected, and nothing more?" a question you can answer rather than assume.

```go
g.OnCompute(func(k string, err error) { log.Printf("computed %s: %v", k, err) })
```

## Concurrency

A `*Graph` is not safe for concurrent use, and `cascade` deliberately ships no
mutex-wrapped variant — locking belongs where your transaction boundaries are,
not here. Note that `Get` can compute and therefore writes, so concurrent
`Get` calls race just as concurrent mutations do. Guard a shared graph with
your own lock, or give each goroutine its own with `Clone`.

## Non-goals

- **Not an expression language.** `cascade` never parses or evaluates anything.
  Bring `expr`, `cel-go`, or a Go closure.
- **Not a spreadsheet.** No cell addressing, ranges, `A1` notation, formatting
  or file formats. Keys are whatever comparable type you choose.
- **Not persistent or distributed.** An in-memory graph in one process, with no
  storage layer, no snapshots and no cluster coordination.
- **Not concurrent.** No internal locking and no parallel evaluation of
  independent branches; the single-threaded semantics are the contract.
- **No automatic dependency discovery.** Dependencies are declared, not traced
  at runtime. That's the trade that makes the graph statically inspectable.

## License

MIT

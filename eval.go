package cascade

import "fmt"

// Get returns the current value of k, computing it and any stale precedents it
// needs, in dependency order, on demand. Nodes that are already clean are
// returned from cache without recomputing.
//
// A node that failed keeps its error: Get returns the cached failure rather
// than retrying, until the node or one of its precedents is marked stale
// again. Looking up a key that is not defined fails with *[UnresolvedError].
func (g *Graph[K, V]) Get(k K) (V, error) {
	g.mustBeIdle()
	n, ok := g.nodes[k]
	if !ok || n.kind == kindMissing {
		var zero V
		return zero, &UnresolvedError[K]{Key: k, Missing: k}
	}
	if n.dirty {
		list, set := g.staleClosure(k)
		// The node's own error is returned below; a failure elsewhere in the
		// closure is already recorded on its node and reachable via Get there.
		_ = g.evaluate(list, set)
	}
	return n.val, n.err
}

// Recalc computes every stale node in the graph, in dependency order, so no
// formula ever reads a stale precedent. Individual node failures are recorded
// on their nodes and propagated downstream; Recalc returns the first failure
// it saw in topological order, or nil if everything computed cleanly.
//
// Inspect [Graph.Get] per key, or install [Graph.OnCompute], when you need the
// full picture rather than the first error.
func (g *Graph[K, V]) Recalc() error {
	g.mustBeIdle()
	list := make([]K, 0, len(g.order))
	set := make(map[K]struct{}, len(g.order))
	for _, k := range g.order {
		if n := g.nodes[k]; n.kind == kindFormula && n.dirty {
			list = append(list, k)
			set[k] = struct{}{}
		}
	}
	return g.evaluate(list, set)
}

// RecalcFrom computes only the part of the graph reachable from the given
// keys: those keys and everything transitively downstream of them, restricted
// to what is actually stale. Stale precedents that those nodes need are pulled
// in as well, so the result is never computed from a stale input, but
// unrelated stale regions of the graph are left alone.
//
// It returns the first failure in topological order, or nil. Unknown keys are
// ignored.
func (g *Graph[K, V]) RecalcFrom(keys ...K) error {
	g.mustBeIdle()

	// Walk downstream from the seeds to find the affected region.
	region := make(map[K]struct{})
	stack := append(make([]K, 0, len(keys)), keys...)
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := region[k]; ok {
			continue
		}
		n, ok := g.nodes[k]
		if !ok {
			continue
		}
		region[k] = struct{}{}
		stack = append(stack, n.dependents...)
	}

	// Then take each stale node in that region together with the stale
	// precedents it depends on, wherever they live.
	list := make([]K, 0, len(region))
	set := make(map[K]struct{}, len(region))
	for _, k := range g.order {
		if _, ok := region[k]; !ok {
			continue
		}
		if n := g.nodes[k]; n.kind != kindFormula || !n.dirty {
			continue
		}
		g.collectStale(k, &list, set)
	}
	return g.evaluate(list, set)
}

// staleClosure gathers the stale nodes that must be computed before k can be
// resolved, k included. Traversal stops at clean nodes: the "dirty implies all
// dependents dirty" invariant guarantees a clean node has no stale precedents.
func (g *Graph[K, V]) staleClosure(k K) ([]K, map[K]struct{}) {
	list := make([]K, 0, 8)
	set := make(map[K]struct{}, 8)
	g.collectStale(k, &list, set)
	return list, set
}

// collectStale adds k and its stale precedent closure to list and set. It is
// an explicit-stack depth-first walk, so a chain of any depth is safe.
func (g *Graph[K, V]) collectStale(k K, list *[]K, set map[K]struct{}) {
	stack := []K{k}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := set[cur]; ok {
			continue
		}
		n, ok := g.nodes[cur]
		if !ok || n.kind != kindFormula || !n.dirty {
			continue
		}
		set[cur] = struct{}{}
		*list = append(*list, cur)
		stack = append(stack, n.deps...)
	}
}

// topoSubset orders the given subset so every node follows the precedents it
// has inside the subset. Kahn's algorithm with a FIFO queue seeded in the
// caller's order: no map is ever iterated, so the result is deterministic for
// a given construction sequence.
func (g *Graph[K, V]) topoSubset(subset []K, set map[K]struct{}) []K {
	indeg := make(map[K]int, len(subset))
	for _, k := range subset {
		c := 0
		for _, d := range g.nodes[k].deps {
			if _, ok := set[d]; ok {
				c++
			}
		}
		indeg[k] = c
	}

	queue := make([]K, 0, len(subset))
	for _, k := range subset {
		if indeg[k] == 0 {
			queue = append(queue, k)
		}
	}

	out := make([]K, 0, len(subset))
	for i := 0; i < len(queue); i++ {
		k := queue[i]
		out = append(out, k)
		for _, dep := range g.nodes[k].dependents {
			if _, ok := set[dep]; !ok {
				continue
			}
			indeg[dep]--
			if indeg[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	return out
}

// evaluate computes the given subset in dependency order and returns the first
// error encountered.
func (g *Graph[K, V]) evaluate(subset []K, set map[K]struct{}) error {
	if len(subset) == 0 {
		return nil
	}
	order := g.topoSubset(subset, set)

	g.busy = true
	defer func() { g.busy = false }()

	var first error
	for _, k := range order {
		err := g.compute(g.nodes[k])
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// compute evaluates a single formula node. Its precedents are guaranteed clean
// by the topological order, so their cached values and errors can be used
// directly: an undefined precedent yields *UnresolvedError, a failed one
// yields an *EvalError naming the original culprit, and neither calls fn.
func (g *Graph[K, V]) compute(n *node[K, V]) error {
	var zero V
	n.dirty = false

	vals := make(map[K]V, len(n.deps))
	for _, d := range n.deps {
		dn, ok := g.nodes[d]
		if !ok || dn.kind == kindMissing {
			n.val, n.err = zero, &UnresolvedError[K]{Key: n.key, Missing: d}
			return g.report(n)
		}
		if dn.err != nil {
			n.val = zero
			n.err = &EvalError[K]{Key: n.key, Cause: rootOf(dn), Err: dn.err}
			return g.report(n)
		}
		vals[d] = dn.val
	}

	v, err := g.call(n, vals)
	if err == nil {
		n.val, n.err = v, nil
		return g.report(n)
	}
	n.val = zero
	if _, ok := err.(*EvalError[K]); !ok {
		// A plain error from the formula: wrap it so the node is named.
		err = &EvalError[K]{Key: n.key, Cause: n.key, Err: err}
	}
	n.err = err
	return g.report(n)
}

// call invokes the user's compute function with panics contained. A panicking
// formula fails its own node as an *EvalError with Panic set, preserving the
// recovered value — user-authored formulas must not be able to take down the
// process.
func (g *Graph[K, V]) call(n *node[K, V], vals map[K]V) (v V, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		var zero V
		v = zero
		cause, ok := r.(error)
		if !ok {
			cause = fmt.Errorf("panic: %v", r)
		}
		err = &EvalError[K]{Key: n.key, Cause: n.key, Panic: true, PanicValue: r, Err: cause}
	}()
	return n.fn(Inputs[K, V]{keys: n.deps, vals: vals})
}

// report fires the OnCompute hook, if any, and returns the node's error.
func (g *Graph[K, V]) report(n *node[K, V]) error {
	if g.onCompute != nil {
		g.onCompute(n.key, n.err)
	}
	return n.err
}

// rootOf names the node where a failed node's error originated.
func rootOf[K comparable, V any](n *node[K, V]) K {
	if r, ok := n.err.(rooted[K]); ok {
		return r.rootKey()
	}
	return n.key
}

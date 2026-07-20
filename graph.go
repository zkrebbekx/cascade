package cascade

// kind distinguishes the three states a key can be in.
type kind uint8

const (
	// kindMissing is a placeholder: the key is referenced by at least one
	// formula but has never been defined, or was defined and then deleted.
	// It occupies a slot only so the reverse edges survive.
	kindMissing kind = iota
	// kindValue is a constant input, supplied by the caller.
	kindValue
	// kindFormula is computed from its declared precedents.
	kindFormula
)

// node is one slot in the graph.
type node[K comparable, V any] struct {
	key  K
	kind kind

	val V
	err error

	fn   func(Inputs[K, V]) (V, error)
	deps []K // declared precedents, deduplicated, in declaration order

	dependents []K // reverse edges, in the order the edges were created

	dirty bool
}

// Graph is a dependency graph of keyed values, where some values are constants
// supplied by the caller and others are computed from them by formulas.
//
// A Graph is lazy: mutating it never computes anything, it only marks the
// affected nodes stale. Values are produced on demand by [Graph.Get], or in
// bulk by [Graph.Recalc] and [Graph.RecalcFrom]. Clean nodes are never
// recomputed, and a node reachable by several paths from a change is
// recomputed exactly once.
//
// The zero Graph is not usable; call [New].
//
// A Graph is not safe for concurrent use. Mutating one from several goroutines,
// or calling Get concurrently (Get can compute, and therefore writes), is a
// data race. Guard it with your own lock if you need to share one.
type Graph[K comparable, V any] struct {
	nodes map[K]*node[K, V]
	order []K // insertion order of every slot, including placeholders

	onCompute func(K, error)

	busy bool // true while evaluating, to catch reentrant use from a formula
}

// New returns an empty Graph.
func New[K comparable, V any]() *Graph[K, V] {
	return &Graph[K, V]{nodes: make(map[K]*node[K, V])}
}

// OnCompute installs a hook called once for every node the engine actually
// computes, with the resulting error (nil on success). It is not called for
// nodes skipped as already clean, which makes it a direct audit of the work a
// change caused. Pass nil to remove the hook.
//
// The hook must not mutate the graph.
func (g *Graph[K, V]) OnCompute(fn func(k K, err error)) {
	g.onCompute = fn
}

// Len reports the number of defined keys. Placeholders for keys that are
// referenced but not defined are not counted.
func (g *Graph[K, V]) Len() int {
	n := 0
	for _, k := range g.order {
		if g.nodes[k].kind != kindMissing {
			n++
		}
	}
	return n
}

// Has reports whether k is defined, as either a value or a formula.
func (g *Graph[K, V]) Has(k K) bool {
	n, ok := g.nodes[k]
	return ok && n.kind != kindMissing
}

// Keys returns every defined key in the order the keys were first defined.
// The order is deterministic for a given construction sequence. Keys are only
// constrained to be comparable, so cascade cannot sort them; if you need a
// sorted view, sort the result yourself.
func (g *Graph[K, V]) Keys() []K {
	out := make([]K, 0, len(g.order))
	for _, k := range g.order {
		if g.nodes[k].kind != kindMissing {
			out = append(out, k)
		}
	}
	return out
}

// Stale returns the keys that are currently dirty and would be recomputed by
// the next [Graph.Recalc], in definition order.
func (g *Graph[K, V]) Stale() []K {
	out := make([]K, 0, len(g.order))
	for _, k := range g.order {
		if n := g.nodes[k]; n.kind == kindFormula && n.dirty {
			out = append(out, k)
		}
	}
	return out
}

// Precedents returns the direct dependencies k declared, in declaration order.
// It returns nil for values, placeholders and unknown keys. The returned slice
// is a copy.
func (g *Graph[K, V]) Precedents(k K) []K {
	n, ok := g.nodes[k]
	if !ok || len(n.deps) == 0 {
		return nil
	}
	out := make([]K, len(n.deps))
	copy(out, n.deps)
	return out
}

// Dependents returns the keys that directly declared k as a dependency, in the
// order those edges were created. It reports dependents of a referenced but
// undefined key too. The returned slice is a copy.
func (g *Graph[K, V]) Dependents(k K) []K {
	n, ok := g.nodes[k]
	if !ok || len(n.dependents) == 0 {
		return nil
	}
	out := make([]K, len(n.dependents))
	copy(out, n.dependents)
	return out
}

// SetValue defines or replaces k as a constant input. Any formula previously
// attached to k is discarded along with its edges. Every transitive dependent
// of k is marked stale; nothing is computed.
func (g *Graph[K, V]) SetValue(k K, v V) {
	g.mustBeIdle()
	n := g.slot(k)
	g.clearDeps(n)
	n.kind = kindValue
	n.val = v
	n.err = nil
	n.fn = nil
	n.dirty = false
	g.markStale(n.dependents)
}

// SetFormula defines or replaces k as computed from deps by fn. Duplicate deps
// are collapsed, and declaring a dep on a key that does not exist yet is legal:
// the node evaluates to *[UnresolvedError] until the key is defined, then
// resolves normally.
//
// If the edges would close a cycle, SetFormula returns *[CycleError] and the
// graph is left exactly as it was — no partial mutation. fn must not be nil.
//
// k and its transitive dependents are marked stale; nothing is computed.
func (g *Graph[K, V]) SetFormula(k K, deps []K, fn func(in Inputs[K, V]) (V, error)) error {
	g.mustBeIdle()
	if fn == nil {
		panic("cascade: SetFormula: nil compute function")
	}

	unique := dedup(deps)

	// Validate before mutating anything, so a rejected call is a no-op.
	if path, bad := g.wouldCycle(k, unique); bad {
		return &CycleError[K]{path: path}
	}

	n := g.slot(k)
	g.clearDeps(n)
	n.kind = kindFormula
	n.fn = fn
	n.deps = unique
	n.err = nil
	n.dirty = true
	var zero V
	n.val = zero
	for _, d := range unique {
		dn := g.slot(d)
		dn.dependents = append(dn.dependents, k)
	}
	g.markStale(n.dependents)
	return nil
}

// Delete removes k. Its dependents are not removed and are not zeroed: they
// become stale and will fail with *[UnresolvedError] until k is defined again.
// Deleting an unknown key is a no-op.
func (g *Graph[K, V]) Delete(k K) {
	g.mustBeIdle()
	n, ok := g.nodes[k]
	if !ok || n.kind == kindMissing {
		return
	}
	g.clearDeps(n)
	var zero V
	n.val = zero
	n.err = nil
	n.fn = nil
	n.kind = kindMissing
	n.dirty = true
	g.markStale(n.dependents)

	// A placeholder only exists to hold reverse edges; drop it if it has none.
	if len(n.dependents) == 0 {
		delete(g.nodes, k)
		for i, key := range g.order {
			if key == k {
				g.order = append(g.order[:i], g.order[i+1:]...)
				break
			}
		}
	}
}

// Clone returns a deep copy of the graph: structure, cached values, cached
// errors and stale flags. The copy shares the caller's compute functions —
// they are values, not state — but no other memory, so mutating either graph
// leaves the other untouched. The OnCompute hook is carried over.
func (g *Graph[K, V]) Clone() *Graph[K, V] {
	out := &Graph[K, V]{
		nodes:     make(map[K]*node[K, V], len(g.nodes)),
		order:     append([]K(nil), g.order...),
		onCompute: g.onCompute,
	}
	for k, n := range g.nodes {
		cp := *n
		cp.deps = append([]K(nil), n.deps...)
		cp.dependents = append([]K(nil), n.dependents...)
		out.nodes[k] = &cp
	}
	return out
}

// TopoOrder returns every defined key in dependency order: a key always
// appears after all of its precedents. The order is deterministic for a given
// construction sequence.
//
// Since [Graph.SetFormula] rejects cycles, a well-formed graph never fails
// here; the error exists so the invariant is checked rather than assumed.
func (g *Graph[K, V]) TopoOrder() ([]K, error) {
	all := make([]K, 0, len(g.order))
	set := make(map[K]struct{}, len(g.order))
	for _, k := range g.order {
		if g.nodes[k].kind == kindMissing {
			continue
		}
		all = append(all, k)
		set[k] = struct{}{}
	}
	out := g.topoSubset(all, set)
	if len(out) != len(all) {
		return nil, &CycleError[K]{path: unplaced(out, all)}
	}
	return out, nil
}

// unplaced names the keys a topological sort could not place, which is exactly
// the set caught in a cycle. Reachable only if the no-cycles invariant is ever
// broken; it exists so TopoOrder checks that invariant instead of assuming it.
func unplaced[K comparable](placed, all []K) []K {
	done := make(map[K]struct{}, len(placed))
	for _, k := range placed {
		done[k] = struct{}{}
	}
	rest := make([]K, 0, len(all)-len(placed))
	for _, k := range all {
		if _, ok := done[k]; !ok {
			rest = append(rest, k)
		}
	}
	return append(rest, rest[0])
}

// slot returns the node for k, creating a placeholder if the key is unknown.
func (g *Graph[K, V]) slot(k K) *node[K, V] {
	if n, ok := g.nodes[k]; ok {
		return n
	}
	n := &node[K, V]{key: k, kind: kindMissing, dirty: true}
	g.nodes[k] = n
	g.order = append(g.order, k)
	return n
}

// clearDeps detaches n from each of its precedents and forgets its formula.
func (g *Graph[K, V]) clearDeps(n *node[K, V]) {
	for _, d := range n.deps {
		dn, ok := g.nodes[d]
		if !ok {
			continue
		}
		for i, k := range dn.dependents {
			if k == n.key {
				dn.dependents = append(dn.dependents[:i], dn.dependents[i+1:]...)
				break
			}
		}
		// Drop a placeholder that nothing references any more.
		if dn.kind == kindMissing && len(dn.dependents) == 0 {
			delete(g.nodes, d)
			for i, k := range g.order {
				if k == d {
					g.order = append(g.order[:i], g.order[i+1:]...)
					break
				}
			}
		}
	}
	n.deps = nil
}

// markStale dirties the given keys and everything transitively downstream of
// them. It is iterative, so depth is bounded by the heap and not the goroutine
// stack, and it stops descending at nodes that are already dirty — the
// invariant "dirty implies all dependents dirty" makes that safe and keeps the
// cost proportional to the newly dirtied region.
func (g *Graph[K, V]) markStale(seeds []K) {
	if len(seeds) == 0 {
		return
	}
	stack := append(make([]K, 0, len(seeds)), seeds...)
	for len(stack) > 0 {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n, ok := g.nodes[k]
		if !ok {
			continue
		}
		if n.kind == kindFormula {
			if n.dirty {
				continue
			}
			n.dirty = true
			n.err = nil
		}
		stack = append(stack, n.dependents...)
	}
}

// wouldCycle reports whether making k depend on deps closes a cycle, and if so
// returns the path k -> ... -> k.
//
// The search runs downstream from k rather than upstream from each dep: a new
// edge can only close a loop if one of the declared deps already depends on k,
// so one walk over k's dependents settles it. That walk is empty for a node
// nothing depends on yet, which makes the common append-only construction O(1)
// per edge instead of a full ancestor scan.
func (g *Graph[K, V]) wouldCycle(k K, deps []K) ([]K, bool) {
	if len(deps) == 0 {
		return nil, false
	}
	want := make(map[K]struct{}, len(deps))
	for _, d := range deps {
		if d == k {
			return []K{k, k}, true
		}
		want[d] = struct{}{}
	}

	n, ok := g.nodes[k]
	if !ok || len(n.dependents) == 0 {
		return nil, false
	}

	seen := map[K]struct{}{k: {}}
	stack := append(make([]K, 0, len(n.dependents)), n.dependents...)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := seen[cur]; ok {
			continue
		}
		seen[cur] = struct{}{}
		if _, ok := want[cur]; ok {
			// cur is a declared dep that already depends on k: report
			// k -> cur -> ... -> k.
			path, _ := g.pathTo(cur, k)
			return append([]K{k}, path...), true
		}
		if cn, ok := g.nodes[cur]; ok {
			stack = append(stack, cn.dependents...)
		}
	}
	return nil, false
}

// pathTo finds a route from src to dst along precedent edges, returned as
// src, ..., dst. It is an explicit-stack depth-first search, used only once a
// cycle has already been confirmed, to name it for the error message.
func (g *Graph[K, V]) pathTo(src, dst K) ([]K, bool) {
	type frame struct {
		key K
		i   int
	}
	seen := map[K]struct{}{src: {}}
	stack := []frame{{key: src}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		n := g.nodes[top.key]
		if n == nil || top.i >= len(n.deps) {
			stack = stack[:len(stack)-1]
			continue
		}
		d := n.deps[top.i]
		top.i++
		if d == dst {
			path := make([]K, 0, len(stack)+1)
			for _, f := range stack {
				path = append(path, f.key)
			}
			return append(path, dst), true
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		stack = append(stack, frame{key: d})
	}
	return nil, false
}

// mustBeIdle rejects reentrant use: calling back into the graph from inside a
// compute function would corrupt the in-flight evaluation. The panic is
// recovered by the engine and reported as an *EvalError against the offending
// node, so a misbehaving formula fails itself rather than the process.
func (g *Graph[K, V]) mustBeIdle() {
	if g.busy {
		panic("cascade: reentrant use of Graph from inside a compute function")
	}
}

// dedup returns keys with duplicates removed, preserving first-seen order.
func dedup[K comparable](keys []K) []K {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[K]struct{}, len(keys))
	out := make([]K, 0, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

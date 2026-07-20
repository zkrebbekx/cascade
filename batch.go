package cascade

// Batch groups a set of edits so they settle in a single recalculation pass.
// It is handed to the function passed to [Graph.Batch] and is only valid for
// the duration of that call; using it afterwards panics.
type Batch[K comparable, V any] struct {
	g *Graph[K, V]
}

// Graph is lazy by default, so edits never compute on their own — Batch exists
// to make "many edits, then settle once" explicit at the call site, and to give
// that intent a scope. It is sugar, not a different execution mode.
func (b *Batch[K, V]) graph() *Graph[K, V] {
	if b.g == nil {
		panic("cascade: Batch used after its Batch call returned")
	}
	return b.g
}

// SetValue defines or replaces k as a constant input. See [Graph.SetValue].
func (b *Batch[K, V]) SetValue(k K, v V) { b.graph().SetValue(k, v) }

// SetFormula defines or replaces k as computed from deps. See
// [Graph.SetFormula].
func (b *Batch[K, V]) SetFormula(k K, deps []K, fn func(in Inputs[K, V]) (V, error)) error {
	return b.graph().SetFormula(k, deps, fn)
}

// Delete removes k. See [Graph.Delete].
func (b *Batch[K, V]) Delete(k K) { b.graph().Delete(k) }

// Batch runs fn against the graph and then performs exactly one [Graph.Recalc],
// returning its error. Because a Graph never computes on mutation, this is a
// readability and scoping device: it states that the edits inside belong
// together and that the graph should settle once when they are all applied.
//
// If fn panics, the graph is left in whatever state the applied edits produced
// — consistently stale, never half-computed — and the panic propagates.
func (g *Graph[K, V]) Batch(fn func(b *Batch[K, V]) error) error {
	g.mustBeIdle()
	b := &Batch[K, V]{g: g}
	defer func() { b.g = nil }()
	if err := fn(b); err != nil {
		return err
	}
	return g.Recalc()
}

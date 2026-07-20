package cascade

// Inputs is the read-only handle a formula uses to reach its declared
// dependencies. It is valid only for the duration of the call that received
// it; retaining it and reading later yields the values captured at call time.
//
// Reads are restricted to the keys the formula declared. Reaching for anything
// else fails with [UndeclaredError] rather than silently succeeding, which is
// what keeps the recorded edges an honest description of the data flow.
type Inputs[K comparable, V any] struct {
	keys []K
	vals map[K]V
}

// Get returns the value of a declared dependency. It fails with
// *[UndeclaredError] if k is not one of the formula's declared dependencies.
func (in Inputs[K, V]) Get(k K) (V, error) {
	v, ok := in.vals[k]
	if !ok {
		var zero V
		return zero, &UndeclaredError[K]{Key: k}
	}
	return v, nil
}

// MustGet returns the value of a declared dependency and panics if k was not
// declared. The panic is contained by the engine and surfaces as an
// *[EvalError] with Panic set, so it fails the node rather than the process.
func (in Inputs[K, V]) MustGet(k K) V {
	v, err := in.Get(k)
	if err != nil {
		panic(err)
	}
	return v
}

// Len reports how many dependencies the formula declared.
func (in Inputs[K, V]) Len() int { return len(in.keys) }

// Keys returns the declared dependencies in declaration order. The returned
// slice is a copy and may be modified freely.
func (in Inputs[K, V]) Keys() []K {
	out := make([]K, len(in.keys))
	copy(out, in.keys)
	return out
}

package cascade_test

import (
	"errors"
	"fmt"

	"github.com/zkrebbekx/cascade"
)

// Example builds a small invoice model and changes one input.
func Example() {
	g := cascade.New[string, float64]()

	g.SetValue("qty", 3)
	g.SetValue("unit", 20)
	g.SetValue("taxRate", 0.2)

	_ = g.SetFormula("net", []string{"qty", "unit"}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet("qty") * in.MustGet("unit"), nil
	})
	_ = g.SetFormula("tax", []string{"net", "taxRate"}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet("net") * in.MustGet("taxRate"), nil
	})
	_ = g.SetFormula("total", []string{"net", "tax"}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet("net") + in.MustGet("tax"), nil
	})

	total, _ := g.Get("total")
	fmt.Printf("total: %.2f\n", total)

	g.SetValue("qty", 10)
	total, _ = g.Get("total")
	fmt.Printf("total: %.2f\n", total)

	// Output:
	// total: 72.00
	// total: 240.00
}

// Example_minimalRecompute shows that a change recomputes each affected node
// exactly once, even where two paths reconverge.
func Example_minimalRecompute() {
	g := cascade.New[string, int]()
	g.SetValue("a", 1)

	double := func(k string) func(cascade.Inputs[string, int]) (int, error) {
		return func(in cascade.Inputs[string, int]) (int, error) {
			return in.MustGet(k) * 2, nil
		}
	}
	_ = g.SetFormula("b", []string{"a"}, double("a"))
	_ = g.SetFormula("c", []string{"a"}, double("a"))
	_ = g.SetFormula("d", []string{"b", "c"}, func(in cascade.Inputs[string, int]) (int, error) {
		return in.MustGet("b") + in.MustGet("c"), nil
	})
	_ = g.Recalc()

	// Only report work caused by the change below.
	g.OnCompute(func(k string, _ error) { fmt.Println("computed", k) })
	g.SetValue("a", 5)
	_ = g.Recalc()

	d, _ := g.Get("d")
	fmt.Println("d =", d)

	// Output:
	// computed b
	// computed c
	// computed d
	// d = 20
}

// Example_errorPropagation shows a failure travelling downstream while still
// naming the node that actually broke.
func Example_errorPropagation() {
	errNoRate := errors.New("no rate available")

	g := cascade.New[string, float64]()
	g.SetValue("amount", 100)
	_ = g.SetFormula("rate", nil, func(cascade.Inputs[string, float64]) (float64, error) {
		return 0, errNoRate
	})
	_ = g.SetFormula("converted", []string{"amount", "rate"}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet("amount") * in.MustGet("rate"), nil
	})

	_, err := g.Get("converted")

	var ee *cascade.EvalError[string]
	if errors.As(err, &ee) {
		fmt.Println("failed node:", ee.Key)
		fmt.Println("root cause: ", ee.Cause)
	}
	fmt.Println("is errNoRate:", errors.Is(err, errNoRate))

	// Output:
	// failed node: converted
	// root cause:  rate
	// is errNoRate: true
}

// Example_cycle shows that a cyclic definition is rejected up front, with the
// offending path, and leaves the graph untouched.
func Example_cycle() {
	g := cascade.New[string, int]()
	g.SetValue("a", 1)
	pass := func(k string) func(cascade.Inputs[string, int]) (int, error) {
		return func(in cascade.Inputs[string, int]) (int, error) { return in.MustGet(k), nil }
	}
	_ = g.SetFormula("b", []string{"a"}, pass("a"))
	_ = g.SetFormula("c", []string{"b"}, pass("b"))

	err := g.SetFormula("a", []string{"c"}, pass("c"))

	var ce *cascade.CycleError[string]
	if errors.As(err, &ce) {
		fmt.Println(ce)
		fmt.Println("path:", ce.Path())
	}

	// The rejected call changed nothing.
	a, _ := g.Get("a")
	fmt.Println("a is still", a)

	// Output:
	// cascade: cycle: a -> c -> b -> a
	// path: [a c b a]
	// a is still 1
}

// Example_forwardReference shows that a dependency may be declared before it
// exists; the dependent resolves once the key is defined.
func Example_forwardReference() {
	g := cascade.New[string, int]()
	_ = g.SetFormula("greeting", []string{"name"}, func(in cascade.Inputs[string, int]) (int, error) {
		return in.MustGet("name"), nil
	})

	_, err := g.Get("greeting")
	fmt.Println(err)

	g.SetValue("name", 7)
	v, _ := g.Get("greeting")
	fmt.Println("resolved to", v)

	// Output:
	// cascade: unresolved: greeting references undefined key name
	// resolved to 7
}

// Example_panicContainment shows a misbehaving formula failing its own node
// instead of the process.
func Example_panicContainment() {
	g := cascade.New[string, int]()
	g.SetValue("n", 0)
	_ = g.SetFormula("recip", []string{"n"}, func(in cascade.Inputs[string, int]) (int, error) {
		return 100 / in.MustGet("n"), nil // divides by zero
	})

	_, err := g.Get("recip")

	var ee *cascade.EvalError[string]
	if errors.As(err, &ee) {
		fmt.Println("panicked:", ee.Panic)
		fmt.Println("node:", ee.Key)
	}
	fmt.Println("graph still usable:", g.Has("n"))

	// Output:
	// panicked: true
	// node: recip
	// graph still usable: true
}

// Example_batch groups several edits so the graph settles in one pass.
func Example_batch() {
	g := cascade.New[string, int]()
	g.SetValue("a", 1)
	g.SetValue("b", 2)

	var computes int
	_ = g.SetFormula("sum", []string{"a", "b"}, func(in cascade.Inputs[string, int]) (int, error) {
		computes++
		return in.MustGet("a") + in.MustGet("b"), nil
	})

	_ = g.Batch(func(b *cascade.Batch[string, int]) error {
		b.SetValue("a", 10)
		b.SetValue("b", 20)
		return nil
	})

	sum, _ := g.Get("sum")
	fmt.Println("sum:", sum, "computes:", computes)
	// Output: sum: 30 computes: 1
}

// Example_topoOrder inspects the evaluation order the engine will use.
func Example_topoOrder() {
	g := cascade.New[string, int]()
	g.SetValue("width", 3)
	g.SetValue("height", 4)
	_ = g.SetFormula("area", []string{"width", "height"}, func(in cascade.Inputs[string, int]) (int, error) {
		return in.MustGet("width") * in.MustGet("height"), nil
	})
	_ = g.SetFormula("volume", []string{"area"}, func(in cascade.Inputs[string, int]) (int, error) {
		return in.MustGet("area") * 10, nil
	})

	order, _ := g.TopoOrder()
	fmt.Println(order)
	fmt.Println("volume depends directly on", g.Precedents("volume"))
	fmt.Println("area feeds", g.Dependents("area"))

	// Output:
	// [width height area volume]
	// volume depends directly on [area]
	// area feeds [volume]
}

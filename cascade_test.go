package cascade_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zkrebbekx/cascade"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type counter struct{ hits map[string]int }

func newCounter() *counter { return &counter{hits: map[string]int{}} }

func (c *counter) count(k string) { c.hits[k]++ }

// sum builds a formula that adds every declared dependency and records a hit.
func sum(c *counter, key string) func(cascade.Inputs[string, int]) (int, error) {
	return func(in cascade.Inputs[string, int]) (int, error) {
		if c != nil {
			c.count(key)
		}
		total := 0
		for _, k := range in.Keys() {
			total += in.MustGet(k)
		}
		return total, nil
	}
}

func mustSetFormula(t *testing.T, g *cascade.Graph[string, int], k string, deps []string, fn func(cascade.Inputs[string, int]) (int, error)) {
	t.Helper()
	if err := g.SetFormula(k, deps, fn); err != nil {
		t.Fatalf("SetFormula(%q): unexpected error: %v", k, err)
	}
}

func mustGet(t *testing.T, g *cascade.Graph[string, int], k string) int {
	t.Helper()
	v, err := g.Get(k)
	if err != nil {
		t.Fatalf("Get(%q): unexpected error: %v", k, err)
	}
	return v
}

// ---------------------------------------------------------------------------
// dirty propagation
// ---------------------------------------------------------------------------

func TestDirtyPropagation(t *testing.T) {
	t.Run("Given a chain A->B->C", func(t *testing.T) {
		build := func(c *counter) *cascade.Graph[string, int] {
			g := cascade.New[string, int]()
			g.SetValue("A", 1)
			mustSetFormula(t, g, "B", []string{"A"}, sum(c, "B"))
			mustSetFormula(t, g, "C", []string{"B"}, sum(c, "C"))
			return g
		}

		t.Run("When A changes", func(t *testing.T) {
			c := newCounter()
			g := build(c)
			if got := mustGet(t, g, "C"); got != 1 {
				t.Fatalf("C = %d, want 1", got)
			}
			c.hits = map[string]int{}
			g.SetValue("A", 10)

			t.Run("Then B and C are recomputed exactly once", func(t *testing.T) {
				if got := mustGet(t, g, "C"); got != 10 {
					t.Errorf("C = %d, want 10", got)
				}
				if c.hits["B"] != 1 || c.hits["C"] != 1 {
					t.Errorf("recompute counts = %v, want B:1 C:1", c.hits)
				}
			})
		})

		t.Run("When nothing changes", func(t *testing.T) {
			c := newCounter()
			g := build(c)
			mustGet(t, g, "C")
			c.hits = map[string]int{}

			t.Run("Then repeated reads recompute nothing", func(t *testing.T) {
				mustGet(t, g, "C")
				mustGet(t, g, "C")
				if err := g.Recalc(); err != nil {
					t.Fatalf("Recalc: %v", err)
				}
				if len(c.hits) != 0 {
					t.Errorf("recompute counts = %v, want none", c.hits)
				}
			})
		})

		t.Run("When only a leaf is read", func(t *testing.T) {
			c := newCounter()
			g := build(c)

			t.Run("Then unrelated stale nodes stay uncomputed", func(t *testing.T) {
				g.SetValue("X", 5)
				mustSetFormula(t, g, "Y", []string{"X"}, sum(c, "Y"))
				if got := mustGet(t, g, "B"); got != 1 {
					t.Errorf("B = %d, want 1", got)
				}
				if c.hits["Y"] != 0 {
					t.Errorf("Y computed %d times, want 0", c.hits["Y"])
				}
				if got := g.Stale(); !slices.Contains(got, "Y") {
					t.Errorf("Stale() = %v, want it to contain Y", got)
				}
			})
		})
	})

	t.Run("Given a diamond A->{B,C}->D", func(t *testing.T) {
		c := newCounter()
		g := cascade.New[string, int]()
		g.SetValue("A", 2)
		mustSetFormula(t, g, "B", []string{"A"}, sum(c, "B"))
		mustSetFormula(t, g, "C", []string{"A"}, sum(c, "C"))
		mustSetFormula(t, g, "D", []string{"B", "C"}, sum(c, "D"))

		t.Run("When A changes", func(t *testing.T) {
			mustGet(t, g, "D")
			c.hits = map[string]int{}
			g.SetValue("A", 7)

			t.Run("Then B, C and D are each recomputed exactly once", func(t *testing.T) {
				if got := mustGet(t, g, "D"); got != 14 {
					t.Errorf("D = %d, want 14", got)
				}
				want := map[string]int{"B": 1, "C": 1, "D": 1}
				if !reflect.DeepEqual(c.hits, want) {
					t.Errorf("recompute counts = %v, want %v", c.hits, want)
				}
			})
		})

		t.Run("When A changes and Recalc is used instead of Get", func(t *testing.T) {
			c.hits = map[string]int{}
			g.SetValue("A", 3)

			t.Run("Then D is still computed exactly once", func(t *testing.T) {
				if err := g.Recalc(); err != nil {
					t.Fatalf("Recalc: %v", err)
				}
				want := map[string]int{"B": 1, "C": 1, "D": 1}
				if !reflect.DeepEqual(c.hits, want) {
					t.Errorf("recompute counts = %v, want %v", c.hits, want)
				}
				if got := mustGet(t, g, "D"); got != 6 {
					t.Errorf("D = %d, want 6", got)
				}
			})
		})
	})

	t.Run("Given a wide fan-in over a shared root", func(t *testing.T) {
		const n = 50
		c := newCounter()
		g := cascade.New[string, int]()
		g.SetValue("root", 1)
		mids := make([]string, n)
		for i := range n {
			k := fmt.Sprintf("m%02d", i)
			mids[i] = k
			mustSetFormula(t, g, k, []string{"root"}, sum(c, k))
		}
		mustSetFormula(t, g, "top", mids, sum(c, "top"))

		t.Run("When the root changes", func(t *testing.T) {
			mustGet(t, g, "top")
			c.hits = map[string]int{}
			g.SetValue("root", 2)

			t.Run("Then every node is recomputed exactly once", func(t *testing.T) {
				if got := mustGet(t, g, "top"); got != 2*n {
					t.Errorf("top = %d, want %d", got, 2*n)
				}
				if len(c.hits) != n+1 {
					t.Fatalf("computed %d nodes, want %d", len(c.hits), n+1)
				}
				for k, v := range c.hits {
					if v != 1 {
						t.Errorf("%s computed %d times, want 1", k, v)
					}
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// topological order
// ---------------------------------------------------------------------------

func TestTopoOrder(t *testing.T) {
	build := func() *cascade.Graph[string, int] {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		g.SetValue("Z", 1)
		mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
		mustSetFormula(t, g, "C", []string{"A", "Z"}, sum(nil, "C"))
		mustSetFormula(t, g, "D", []string{"B", "C"}, sum(nil, "D"))
		mustSetFormula(t, g, "E", []string{"D"}, sum(nil, "E"))
		return g
	}

	t.Run("Given a graph with several independent branches", func(t *testing.T) {
		t.Run("When TopoOrder is taken", func(t *testing.T) {
			order, err := build().TopoOrder()
			if err != nil {
				t.Fatalf("TopoOrder: %v", err)
			}

			t.Run("Then every key follows all of its precedents", func(t *testing.T) {
				pos := map[string]int{}
				for i, k := range order {
					pos[k] = i
				}
				if len(order) != 6 {
					t.Fatalf("order = %v, want 6 keys", order)
				}
				for _, pair := range [][2]string{
					{"A", "B"}, {"A", "C"}, {"Z", "C"},
					{"B", "D"}, {"C", "D"}, {"D", "E"},
				} {
					if pos[pair[0]] >= pos[pair[1]] {
						t.Errorf("%s must come before %s in %v", pair[0], pair[1], order)
					}
				}
			})
		})

		t.Run("When the same construction sequence is repeated many times", func(t *testing.T) {
			t.Run("Then the order is identical every run", func(t *testing.T) {
				first, err := build().TopoOrder()
				if err != nil {
					t.Fatalf("TopoOrder: %v", err)
				}
				for i := range 200 {
					got, err := build().TopoOrder()
					if err != nil {
						t.Fatalf("TopoOrder: %v", err)
					}
					if !slices.Equal(got, first) {
						t.Fatalf("run %d: order = %v, want %v", i, got, first)
					}
				}
			})
		})

		t.Run("When Recalc runs repeatedly", func(t *testing.T) {
			t.Run("Then the compute order is identical every run", func(t *testing.T) {
				var first []string
				for i := range 200 {
					g := build()
					var seen []string
					g.OnCompute(func(k string, _ error) { seen = append(seen, k) })
					if err := g.Recalc(); err != nil {
						t.Fatalf("Recalc: %v", err)
					}
					if first == nil {
						first = seen
						continue
					}
					if !slices.Equal(seen, first) {
						t.Fatalf("run %d: compute order = %v, want %v", i, seen, first)
					}
				}
			})
		})
	})

	t.Run("Given a graph containing only values", func(t *testing.T) {
		t.Run("When TopoOrder is taken", func(t *testing.T) {
			g := cascade.New[string, int]()
			g.SetValue("a", 1)
			g.SetValue("b", 2)

			t.Run("Then it returns them in definition order", func(t *testing.T) {
				order, err := g.TopoOrder()
				if err != nil {
					t.Fatalf("TopoOrder: %v", err)
				}
				if !slices.Equal(order, []string{"a", "b"}) {
					t.Errorf("order = %v, want [a b]", order)
				}
			})
		})
	})

	t.Run("Given a graph with an undefined forward reference", func(t *testing.T) {
		t.Run("When TopoOrder is taken", func(t *testing.T) {
			g := cascade.New[string, int]()
			mustSetFormula(t, g, "B", []string{"ghost"}, sum(nil, "B"))

			t.Run("Then the placeholder is not listed", func(t *testing.T) {
				order, err := g.TopoOrder()
				if err != nil {
					t.Fatalf("TopoOrder: %v", err)
				}
				if !slices.Equal(order, []string{"B"}) {
					t.Errorf("order = %v, want [B]", order)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// cycles
// ---------------------------------------------------------------------------

func TestCycles(t *testing.T) {
	t.Run("Given a chain A->B->C", func(t *testing.T) {
		build := func() *cascade.Graph[string, int] {
			g := cascade.New[string, int]()
			g.SetValue("A", 1)
			mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
			mustSetFormula(t, g, "C", []string{"B"}, sum(nil, "C"))
			return g
		}

		t.Run("When a formula would close the loop", func(t *testing.T) {
			g := build()
			before := g.Keys()
			err := g.SetFormula("A", []string{"C"}, sum(nil, "A"))

			t.Run("Then it is rejected with a CycleError naming the path", func(t *testing.T) {
				var ce *cascade.CycleError[string]
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v (%T), want *CycleError", err, err)
				}
				want := []string{"A", "C", "B", "A"}
				if !slices.Equal(ce.Path(), want) {
					t.Errorf("Path() = %v, want %v", ce.Path(), want)
				}
				if got := ce.Error(); got != "cascade: cycle: A -> C -> B -> A" {
					t.Errorf("Error() = %q", got)
				}
			})

			t.Run("Then the returned path is a copy the caller may mutate", func(t *testing.T) {
				var ce *cascade.CycleError[string]
				if !errors.As(err, &ce) {
					t.Fatal("want *CycleError")
				}
				p := ce.Path()
				p[0] = "clobbered"
				if ce.Path()[0] != "A" {
					t.Error("Path() exposed internal state")
				}
			})

			t.Run("Then the graph is left completely unchanged", func(t *testing.T) {
				if !slices.Equal(g.Keys(), before) {
					t.Errorf("Keys() = %v, want %v", g.Keys(), before)
				}
				if got := g.Precedents("A"); got != nil {
					t.Errorf("A gained precedents %v", got)
				}
				if got := g.Dependents("C"); got != nil {
					t.Errorf("C gained dependents %v", got)
				}
				if got := mustGet(t, g, "A"); got != 1 {
					t.Errorf("A = %d, want its original value 1", got)
				}
				if got := mustGet(t, g, "C"); got != 1 {
					t.Errorf("C = %d, want 1", got)
				}
			})
		})

		t.Run("When a node is made to depend on itself", func(t *testing.T) {
			g := build()
			err := g.SetFormula("B", []string{"A", "B"}, sum(nil, "B"))

			t.Run("Then the self-cycle is rejected and B keeps its old formula", func(t *testing.T) {
				var ce *cascade.CycleError[string]
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want *CycleError", err)
				}
				if !slices.Equal(ce.Path(), []string{"B", "B"}) {
					t.Errorf("Path() = %v, want [B B]", ce.Path())
				}
				if got := g.Precedents("B"); !slices.Equal(got, []string{"A"}) {
					t.Errorf("Precedents(B) = %v, want [A]", got)
				}
			})
		})

		t.Run("When a brand-new key would close a cycle", func(t *testing.T) {
			g := build()
			mustSetFormula(t, g, "D", []string{"C"}, sum(nil, "D"))
			err := g.SetFormula("B", []string{"D"}, sum(nil, "B"))

			t.Run("Then no placeholder or edge is left behind", func(t *testing.T) {
				if err == nil {
					t.Fatal("want *CycleError")
				}
				if got := g.Precedents("B"); !slices.Equal(got, []string{"A"}) {
					t.Errorf("Precedents(B) = %v, want [A]", got)
				}
				if got := g.Dependents("D"); got != nil {
					t.Errorf("Dependents(D) = %v, want none", got)
				}
			})
		})

		t.Run("When a formula depends on a key that does not exist", func(t *testing.T) {
			g := build()

			t.Run("Then no cycle is reported", func(t *testing.T) {
				if err := g.SetFormula("F", []string{"nope"}, sum(nil, "F")); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			})
		})
	})

	t.Run("Given a cycle reachable only through a dead-end branch", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		g.SetValue("X", 1)
		mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
		mustSetFormula(t, g, "C", []string{"X", "B"}, sum(nil, "C"))

		t.Run("When the loop is closed", func(t *testing.T) {
			err := g.SetFormula("A", []string{"C"}, sum(nil, "A"))

			t.Run("Then the reported path skips the dead end", func(t *testing.T) {
				var ce *cascade.CycleError[string]
				if !errors.As(err, &ce) {
					t.Fatalf("err = %v, want *CycleError", err)
				}
				want := []string{"A", "C", "B", "A"}
				if !slices.Equal(ce.Path(), want) {
					t.Errorf("Path() = %v, want %v", ce.Path(), want)
				}
			})
		})
	})

	t.Run("Given a node whose dependents form a diamond", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		g.SetValue("other", 2)
		mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
		mustSetFormula(t, g, "C", []string{"A"}, sum(nil, "C"))
		mustSetFormula(t, g, "D", []string{"B", "C"}, sum(nil, "D"))

		t.Run("When that node is rewired to an unrelated key", func(t *testing.T) {
			err := g.SetFormula("A", []string{"other"}, sum(nil, "A"))

			t.Run("Then the cycle search visits the shared node once and accepts", func(t *testing.T) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got := mustGet(t, g, "D"); got != 4 {
					t.Errorf("D = %d, want 4", got)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// errors
// ---------------------------------------------------------------------------

var errBoom = errors.New("boom")

func TestErrorPropagation(t *testing.T) {
	t.Run("Given a chain where the middle node fails", func(t *testing.T) {
		c := newCounter()
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		mustSetFormula(t, g, "B", []string{"A"}, func(cascade.Inputs[string, int]) (int, error) {
			c.count("B")
			return 0, errBoom
		})
		mustSetFormula(t, g, "C", []string{"B"}, sum(c, "C"))
		mustSetFormula(t, g, "D", []string{"C"}, sum(c, "D"))

		t.Run("When a downstream node is read", func(t *testing.T) {
			_, err := g.Get("D")

			t.Run("Then it fails with an EvalError naming the original culprit", func(t *testing.T) {
				var ee *cascade.EvalError[string]
				if !errors.As(err, &ee) {
					t.Fatalf("err = %v (%T), want *EvalError", err, err)
				}
				if ee.Key != "D" {
					t.Errorf("Key = %q, want D", ee.Key)
				}
				if ee.Cause != "B" {
					t.Errorf("Cause = %q, want B", ee.Cause)
				}
				if ee.Panic {
					t.Error("Panic = true, want false")
				}
			})

			t.Run("Then the original error is reachable with errors.Is", func(t *testing.T) {
				if !errors.Is(err, errBoom) {
					t.Errorf("errors.Is(err, errBoom) = false; err = %v", err)
				}
			})

			t.Run("Then the message names both the node and the upstream cause", func(t *testing.T) {
				got := err.Error()
				if !strings.Contains(got, "eval D") || !strings.Contains(got, "upstream B") {
					t.Errorf("Error() = %q", got)
				}
			})

			t.Run("Then the failing node itself reports a direct failure", func(t *testing.T) {
				_, bErr := g.Get("B")
				var ee *cascade.EvalError[string]
				if !errors.As(bErr, &ee) {
					t.Fatalf("err = %v, want *EvalError", bErr)
				}
				if ee.Key != "B" || ee.Cause != "B" {
					t.Errorf("Key/Cause = %q/%q, want B/B", ee.Key, ee.Cause)
				}
				if got := bErr.Error(); got != "cascade: eval B: boom" {
					t.Errorf("Error() = %q", got)
				}
			})

			t.Run("Then downstream nodes hold the zero value", func(t *testing.T) {
				v, _ := g.Get("D")
				if v != 0 {
					t.Errorf("D = %d, want 0", v)
				}
			})
		})

		t.Run("When the failing node is read repeatedly", func(t *testing.T) {
			c.hits = map[string]int{}

			t.Run("Then the error is cached and the formula is not retried", func(t *testing.T) {
				for range 3 {
					if _, err := g.Get("D"); err == nil {
						t.Fatal("want an error")
					}
				}
				if len(c.hits) != 0 {
					t.Errorf("recompute counts = %v, want none", c.hits)
				}
			})
		})

		t.Run("When a precedent is marked stale again", func(t *testing.T) {
			c.hits = map[string]int{}
			g.SetValue("A", 2)

			t.Run("Then the cached error is discarded and the formula retried", func(t *testing.T) {
				if _, err := g.Get("D"); err == nil {
					t.Fatal("want an error")
				}
				if c.hits["B"] != 1 {
					t.Errorf("B computed %d times, want 1", c.hits["B"])
				}
			})
		})

		t.Run("When the failing formula is replaced with a working one", func(t *testing.T) {
			mustSetFormula(t, g, "B", []string{"A"}, sum(c, "B"))

			t.Run("Then the whole chain recovers", func(t *testing.T) {
				if got := mustGet(t, g, "D"); got != 2 {
					t.Errorf("D = %d, want 2", got)
				}
			})
		})
	})

	t.Run("Given a node whose two precedents both fail", func(t *testing.T) {
		g := cascade.New[string, int]()
		fail := func(name string) func(cascade.Inputs[string, int]) (int, error) {
			return func(cascade.Inputs[string, int]) (int, error) {
				return 0, fmt.Errorf("%s failed", name)
			}
		}
		mustSetFormula(t, g, "X", nil, fail("X"))
		mustSetFormula(t, g, "Y", nil, fail("Y"))
		mustSetFormula(t, g, "Z", []string{"X", "Y"}, sum(nil, "Z"))

		t.Run("When Z is read", func(t *testing.T) {
			_, err := g.Get("Z")

			t.Run("Then it reports the first declared failing precedent", func(t *testing.T) {
				var ee *cascade.EvalError[string]
				if !errors.As(err, &ee) {
					t.Fatalf("err = %v, want *EvalError", err)
				}
				if ee.Cause != "X" {
					t.Errorf("Cause = %q, want X", ee.Cause)
				}
			})
		})
	})

	t.Run("Given a graph with several failures", func(t *testing.T) {
		g := cascade.New[string, int]()
		mustSetFormula(t, g, "a", nil, func(cascade.Inputs[string, int]) (int, error) {
			return 0, errBoom
		})
		mustSetFormula(t, g, "b", nil, sum(nil, "b"))

		t.Run("When Recalc runs", func(t *testing.T) {
			err := g.Recalc()

			t.Run("Then it returns an error but still computes the healthy nodes", func(t *testing.T) {
				if !errors.Is(err, errBoom) {
					t.Errorf("Recalc err = %v, want errBoom", err)
				}
				if _, bErr := g.Get("b"); bErr != nil {
					t.Errorf("b = %v, want it to have computed cleanly", bErr)
				}
				if len(g.Stale()) != 0 {
					t.Errorf("Stale() = %v, want empty", g.Stale())
				}
			})
		})
	})
}

func TestPanicContainment(t *testing.T) {
	t.Run("Given a formula that panics", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		mustSetFormula(t, g, "B", []string{"A"}, func(cascade.Inputs[string, int]) (int, error) {
			panic("kaboom")
		})
		mustSetFormula(t, g, "C", []string{"B"}, sum(nil, "C"))

		t.Run("When the node is evaluated", func(t *testing.T) {
			_, err := g.Get("B")

			t.Run("Then the panic is contained as an EvalError, not a crash", func(t *testing.T) {
				var ee *cascade.EvalError[string]
				if !errors.As(err, &ee) {
					t.Fatalf("err = %v (%T), want *EvalError", err, err)
				}
				if !ee.Panic {
					t.Error("Panic = false, want true")
				}
				if ee.PanicValue != "kaboom" {
					t.Errorf("PanicValue = %v, want kaboom", ee.PanicValue)
				}
				if got := err.Error(); got != "cascade: eval B: panic: kaboom" {
					t.Errorf("Error() = %q", got)
				}
			})

			t.Run("Then dependents fail with the panicking node as the cause", func(t *testing.T) {
				_, cErr := g.Get("C")
				var ee *cascade.EvalError[string]
				if !errors.As(cErr, &ee) {
					t.Fatalf("err = %v, want *EvalError", cErr)
				}
				if ee.Cause != "B" {
					t.Errorf("Cause = %q, want B", ee.Cause)
				}
			})
		})
	})

	t.Run("Given a formula that panics with an error value", func(t *testing.T) {
		g := cascade.New[string, int]()
		mustSetFormula(t, g, "P", nil, func(cascade.Inputs[string, int]) (int, error) {
			panic(errBoom)
		})

		t.Run("When it is evaluated", func(t *testing.T) {
			_, err := g.Get("P")

			t.Run("Then the panicked error stays unwrappable", func(t *testing.T) {
				if !errors.Is(err, errBoom) {
					t.Errorf("errors.Is(err, errBoom) = false; err = %v", err)
				}
			})
		})
	})

	t.Run("Given a formula that reenters the graph", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		mustSetFormula(t, g, "B", []string{"A"}, func(cascade.Inputs[string, int]) (int, error) {
			g.SetValue("A", 99)
			return 0, nil
		})

		t.Run("When it is evaluated", func(t *testing.T) {
			_, err := g.Get("B")

			t.Run("Then the reentrancy is reported against that node only", func(t *testing.T) {
				var ee *cascade.EvalError[string]
				if !errors.As(err, &ee) {
					t.Fatalf("err = %v, want *EvalError", err)
				}
				if !ee.Panic {
					t.Error("Panic = false, want true")
				}
				if !strings.Contains(err.Error(), "reentrant") {
					t.Errorf("Error() = %q, want it to mention reentrancy", err.Error())
				}
			})

			t.Run("Then the graph is usable again afterwards", func(t *testing.T) {
				if got := mustGet(t, g, "A"); got != 1 {
					t.Errorf("A = %d, want 1", got)
				}
			})
		})
	})
}

func TestUnresolvedReferences(t *testing.T) {
	t.Run("Given a formula declaring a dependency that does not exist yet", func(t *testing.T) {
		c := newCounter()
		g := cascade.New[string, int]()
		mustSetFormula(t, g, "total", []string{"later"}, sum(c, "total"))

		t.Run("When the formula is defined", func(t *testing.T) {
			t.Run("Then the forward reference is accepted", func(t *testing.T) {
				if !g.Has("total") {
					t.Error("Has(total) = false")
				}
				if g.Has("later") {
					t.Error("Has(later) = true, want false for an undefined key")
				}
				if g.Len() != 1 {
					t.Errorf("Len() = %d, want 1 (placeholders excluded)", g.Len())
				}
				if got := g.Keys(); !slices.Equal(got, []string{"total"}) {
					t.Errorf("Keys() = %v, want [total]", got)
				}
			})

			t.Run("Then the placeholder still records the reverse edge", func(t *testing.T) {
				if got := g.Dependents("later"); !slices.Equal(got, []string{"total"}) {
					t.Errorf("Dependents(later) = %v, want [total]", got)
				}
			})
		})

		t.Run("When it is evaluated before the key exists", func(t *testing.T) {
			_, err := g.Get("total")

			t.Run("Then it fails with an UnresolvedError naming the missing key", func(t *testing.T) {
				var ue *cascade.UnresolvedError[string]
				if !errors.As(err, &ue) {
					t.Fatalf("err = %v (%T), want *UnresolvedError", err, err)
				}
				if ue.Key != "total" || ue.Missing != "later" {
					t.Errorf("Key/Missing = %q/%q, want total/later", ue.Key, ue.Missing)
				}
				if got := err.Error(); got != "cascade: unresolved: total references undefined key later" {
					t.Errorf("Error() = %q", got)
				}
			})

			t.Run("Then the formula was never invoked", func(t *testing.T) {
				if c.hits["total"] != 0 {
					t.Errorf("total computed %d times, want 0", c.hits["total"])
				}
			})
		})

		t.Run("When the missing key is finally defined", func(t *testing.T) {
			g.SetValue("later", 42)

			t.Run("Then the dependent becomes stale and resolves normally", func(t *testing.T) {
				if got := g.Stale(); !slices.Equal(got, []string{"total"}) {
					t.Errorf("Stale() = %v, want [total]", got)
				}
				if got := mustGet(t, g, "total"); got != 42 {
					t.Errorf("total = %d, want 42", got)
				}
				if c.hits["total"] != 1 {
					t.Errorf("total computed %d times, want 1", c.hits["total"])
				}
			})
		})
	})

	t.Run("Given an empty graph", func(t *testing.T) {
		g := cascade.New[string, int]()

		t.Run("When an unknown key is read", func(t *testing.T) {
			_, err := g.Get("nope")

			t.Run("Then it fails with an UnresolvedError for that key itself", func(t *testing.T) {
				var ue *cascade.UnresolvedError[string]
				if !errors.As(err, &ue) {
					t.Fatalf("err = %v, want *UnresolvedError", err)
				}
				if ue.Key != "nope" || ue.Missing != "nope" {
					t.Errorf("Key/Missing = %q/%q, want nope/nope", ue.Key, ue.Missing)
				}
				if got := err.Error(); got != "cascade: unresolved: key nope is not defined" {
					t.Errorf("Error() = %q", got)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// declared-dependency discipline
// ---------------------------------------------------------------------------

func TestUndeclaredReads(t *testing.T) {
	t.Run("Given a formula that reads a key it did not declare", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("declared", 1)
		g.SetValue("secret", 99)

		t.Run("When it uses Inputs.Get", func(t *testing.T) {
			mustSetFormula(t, g, "F", []string{"declared"}, func(in cascade.Inputs[string, int]) (int, error) {
				v, err := in.Get("secret")
				if err != nil {
					return 0, err
				}
				return v, nil
			})
			_, err := g.Get("F")

			t.Run("Then the read fails with UndeclaredError rather than succeeding", func(t *testing.T) {
				var ue *cascade.UndeclaredError[string]
				if !errors.As(err, &ue) {
					t.Fatalf("err = %v (%T), want *UndeclaredError", err, err)
				}
				if ue.Key != "secret" {
					t.Errorf("Key = %q, want secret", ue.Key)
				}
				if got := ue.Error(); got != "cascade: undeclared dependency: secret" {
					t.Errorf("Error() = %q", got)
				}
			})
		})

		t.Run("When it uses Inputs.MustGet", func(t *testing.T) {
			mustSetFormula(t, g, "M", []string{"declared"}, func(in cascade.Inputs[string, int]) (int, error) {
				return in.MustGet("secret"), nil
			})
			_, err := g.Get("M")

			t.Run("Then the panic is contained and reported as undeclared", func(t *testing.T) {
				var ee *cascade.EvalError[string]
				if !errors.As(err, &ee) || !ee.Panic {
					t.Fatalf("err = %v, want a panic *EvalError", err)
				}
				var ue *cascade.UndeclaredError[string]
				if !errors.As(err, &ue) {
					t.Fatalf("err = %v, want it to unwrap to *UndeclaredError", err)
				}
			})
		})
	})

	t.Run("Given a formula with duplicate and ordered declarations", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("x", 1)
		g.SetValue("y", 2)
		var seen []string
		var n int
		mustSetFormula(t, g, "F", []string{"y", "x", "y"}, func(in cascade.Inputs[string, int]) (int, error) {
			seen = in.Keys()
			n = in.Len()
			return in.MustGet("x") + in.MustGet("y"), nil
		})

		t.Run("When it is evaluated", func(t *testing.T) {
			got := mustGet(t, g, "F")

			t.Run("Then duplicates are collapsed and declaration order is kept", func(t *testing.T) {
				if got != 3 {
					t.Errorf("F = %d, want 3", got)
				}
				if !slices.Equal(seen, []string{"y", "x"}) {
					t.Errorf("Inputs.Keys() = %v, want [y x]", seen)
				}
				if n != 2 {
					t.Errorf("Inputs.Len() = %d, want 2", n)
				}
				if p := g.Precedents("F"); !slices.Equal(p, []string{"y", "x"}) {
					t.Errorf("Precedents(F) = %v, want [y x]", p)
				}
			})

			t.Run("Then Inputs.Keys returns a copy", func(t *testing.T) {
				var a, b []string
				mustSetFormula(t, g, "G", []string{"x"}, func(in cascade.Inputs[string, int]) (int, error) {
					a = in.Keys()
					a[0] = "clobbered"
					b = in.Keys()
					return 0, nil
				})
				mustGet(t, g, "G")
				if b[0] != "x" {
					t.Errorf("Keys() = %v, want an unclobbered copy", b)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// mutation
// ---------------------------------------------------------------------------

func TestMutation(t *testing.T) {
	t.Run("Given a node with dependents", func(t *testing.T) {
		build := func() *cascade.Graph[string, int] {
			g := cascade.New[string, int]()
			g.SetValue("A", 5)
			mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
			mustSetFormula(t, g, "C", []string{"B"}, sum(nil, "C"))
			return g
		}

		t.Run("When it is deleted", func(t *testing.T) {
			g := build()
			mustGet(t, g, "C")
			g.Delete("A")

			t.Run("Then dependents become unresolved, not silently zero", func(t *testing.T) {
				if g.Has("A") {
					t.Error("Has(A) = true after Delete")
				}
				_, err := g.Get("C")
				var ue *cascade.UnresolvedError[string]
				if !errors.As(err, &ue) {
					t.Fatalf("err = %v (%T), want *UnresolvedError", err, err)
				}
				if ue.Key != "B" || ue.Missing != "A" {
					t.Errorf("Key/Missing = %q/%q, want B/A", ue.Key, ue.Missing)
				}
			})

			t.Run("Then the placeholder survives to hold the reverse edge", func(t *testing.T) {
				if got := g.Dependents("A"); !slices.Equal(got, []string{"B"}) {
					t.Errorf("Dependents(A) = %v, want [B]", got)
				}
				if got := g.Keys(); !slices.Equal(got, []string{"B", "C"}) {
					t.Errorf("Keys() = %v, want [B C]", got)
				}
				if g.Len() != 2 {
					t.Errorf("Len() = %d, want 2", g.Len())
				}
			})

			t.Run("Then redefining the key restores the chain", func(t *testing.T) {
				g.SetValue("A", 8)
				if got := mustGet(t, g, "C"); got != 8 {
					t.Errorf("C = %d, want 8", got)
				}
			})
		})

		t.Run("When a leaf with no dependents is deleted", func(t *testing.T) {
			g := build()
			g.Delete("C")

			t.Run("Then it disappears entirely", func(t *testing.T) {
				if got := g.Keys(); !slices.Equal(got, []string{"A", "B"}) {
					t.Errorf("Keys() = %v, want [A B]", got)
				}
				if got := g.Dependents("B"); got != nil {
					t.Errorf("Dependents(B) = %v, want none", got)
				}
			})
		})

		t.Run("When an unknown key is deleted", func(t *testing.T) {
			g := build()
			before := g.Keys()
			g.Delete("ghost")

			t.Run("Then it is a no-op", func(t *testing.T) {
				if !slices.Equal(g.Keys(), before) {
					t.Errorf("Keys() = %v, want %v", g.Keys(), before)
				}
			})
		})

		t.Run("When a formula is replaced by a value", func(t *testing.T) {
			g := build()
			mustGet(t, g, "C")
			g.SetValue("B", 100)

			t.Run("Then its old edges are dropped and dependents recompute", func(t *testing.T) {
				if got := g.Dependents("A"); got != nil {
					t.Errorf("Dependents(A) = %v, want none", got)
				}
				if got := g.Precedents("B"); got != nil {
					t.Errorf("Precedents(B) = %v, want none", got)
				}
				if got := mustGet(t, g, "C"); got != 100 {
					t.Errorf("C = %d, want 100", got)
				}
			})

			t.Run("Then changing the orphaned value has no effect downstream", func(t *testing.T) {
				g.SetValue("A", 999)
				if got := mustGet(t, g, "C"); got != 100 {
					t.Errorf("C = %d, want 100", got)
				}
			})
		})

		t.Run("When a formula's dependencies are rewired", func(t *testing.T) {
			g := build()
			g.SetValue("Q", 3)
			mustSetFormula(t, g, "B", []string{"Q"}, sum(nil, "B"))

			t.Run("Then only the new precedent is tracked", func(t *testing.T) {
				if got := g.Dependents("A"); got != nil {
					t.Errorf("Dependents(A) = %v, want none", got)
				}
				if got := g.Dependents("Q"); !slices.Equal(got, []string{"B"}) {
					t.Errorf("Dependents(Q) = %v, want [B]", got)
				}
				if got := mustGet(t, g, "C"); got != 3 {
					t.Errorf("C = %d, want 3", got)
				}
			})
		})

		t.Run("When the last reference to a placeholder is rewired away", func(t *testing.T) {
			g := cascade.New[string, int]()
			g.SetValue("real", 1)
			mustSetFormula(t, g, "F", []string{"ghost"}, sum(nil, "F"))
			mustSetFormula(t, g, "F", []string{"real"}, sum(nil, "F"))

			t.Run("Then the placeholder is garbage collected", func(t *testing.T) {
				if got := g.Dependents("ghost"); got != nil {
					t.Errorf("Dependents(ghost) = %v, want none", got)
				}
				if got := mustGet(t, g, "F"); got != 1 {
					t.Errorf("F = %d, want 1", got)
				}
			})
		})
	})

	t.Run("Given a query for a key that was never defined", func(t *testing.T) {
		g := cascade.New[string, int]()

		t.Run("When accessors are called", func(t *testing.T) {
			t.Run("Then they report emptiness rather than panicking", func(t *testing.T) {
				if g.Has("x") {
					t.Error("Has = true")
				}
				if g.Len() != 0 {
					t.Error("Len != 0")
				}
				if got := g.Precedents("x"); got != nil {
					t.Errorf("Precedents = %v", got)
				}
				if got := g.Dependents("x"); got != nil {
					t.Errorf("Dependents = %v", got)
				}
				if got := g.Stale(); len(got) != 0 {
					t.Errorf("Stale = %v", got)
				}
				if err := g.Recalc(); err != nil {
					t.Errorf("Recalc = %v", err)
				}
				if err := g.RecalcFrom("x"); err != nil {
					t.Errorf("RecalcFrom = %v", err)
				}
			})
		})
	})

	t.Run("Given a SetFormula call with a nil function", func(t *testing.T) {
		g := cascade.New[string, int]()

		t.Run("When it is made", func(t *testing.T) {
			t.Run("Then it panics as a programming error", func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Error("want a panic")
					}
				}()
				_ = g.SetFormula("k", nil, nil)
			})
		})
	})

	t.Run("Given accessors that return slices", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))

		t.Run("When the caller mutates a returned slice", func(t *testing.T) {
			p := g.Precedents("B")
			p[0] = "clobbered"
			d := g.Dependents("A")
			d[0] = "clobbered"

			t.Run("Then the graph is unaffected", func(t *testing.T) {
				if got := g.Precedents("B"); !slices.Equal(got, []string{"A"}) {
					t.Errorf("Precedents(B) = %v, want [A]", got)
				}
				if got := g.Dependents("A"); !slices.Equal(got, []string{"B"}) {
					t.Errorf("Dependents(A) = %v, want [B]", got)
				}
			})
		})
	})

	t.Run("Given a formula whose declared deps slice is reused by the caller", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		deps := []string{"A"}
		mustSetFormula(t, g, "B", deps, sum(nil, "B"))

		t.Run("When the caller rewrites that slice", func(t *testing.T) {
			deps[0] = "clobbered"

			t.Run("Then the graph kept its own copy", func(t *testing.T) {
				if got := g.Precedents("B"); !slices.Equal(got, []string{"A"}) {
					t.Errorf("Precedents(B) = %v, want [A]", got)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// RecalcFrom, Batch, OnCompute
// ---------------------------------------------------------------------------

func TestRecalcFrom(t *testing.T) {
	t.Run("Given two independent stale branches", func(t *testing.T) {
		c := newCounter()
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		g.SetValue("X", 1)
		mustSetFormula(t, g, "B", []string{"A"}, sum(c, "B"))
		mustSetFormula(t, g, "C", []string{"B"}, sum(c, "C"))
		mustSetFormula(t, g, "Y", []string{"X"}, sum(c, "Y"))

		t.Run("When RecalcFrom is given one branch's root", func(t *testing.T) {
			if err := g.RecalcFrom("A"); err != nil {
				t.Fatalf("RecalcFrom: %v", err)
			}

			t.Run("Then only that branch is computed", func(t *testing.T) {
				want := map[string]int{"B": 1, "C": 1}
				if !reflect.DeepEqual(c.hits, want) {
					t.Errorf("recompute counts = %v, want %v", c.hits, want)
				}
				if got := g.Stale(); !slices.Equal(got, []string{"Y"}) {
					t.Errorf("Stale() = %v, want [Y]", got)
				}
			})
		})

		t.Run("When RecalcFrom is given a mid-chain node with a stale precedent", func(t *testing.T) {
			c.hits = map[string]int{}
			g.SetValue("A", 4)
			if err := g.RecalcFrom("C"); err != nil {
				t.Fatalf("RecalcFrom: %v", err)
			}

			t.Run("Then the stale precedent is pulled in so the result is correct", func(t *testing.T) {
				if got := mustGet(t, g, "C"); got != 4 {
					t.Errorf("C = %d, want 4", got)
				}
				want := map[string]int{"B": 1, "C": 1}
				if !reflect.DeepEqual(c.hits, want) {
					t.Errorf("recompute counts = %v, want %v", c.hits, want)
				}
			})
		})

		t.Run("When RecalcFrom is given overlapping seeds", func(t *testing.T) {
			c.hits = map[string]int{}
			g.SetValue("A", 9)
			if err := g.RecalcFrom("A", "B", "A"); err != nil {
				t.Fatalf("RecalcFrom: %v", err)
			}

			t.Run("Then the overlap is not computed twice", func(t *testing.T) {
				want := map[string]int{"B": 1, "C": 1}
				if !reflect.DeepEqual(c.hits, want) {
					t.Errorf("recompute counts = %v, want %v", c.hits, want)
				}
			})
		})

		t.Run("When RecalcFrom is given an unknown key", func(t *testing.T) {
			c.hits = map[string]int{}

			t.Run("Then it is ignored", func(t *testing.T) {
				if err := g.RecalcFrom("ghost"); err != nil {
					t.Errorf("RecalcFrom: %v", err)
				}
				if len(c.hits) != 0 {
					t.Errorf("recompute counts = %v, want none", c.hits)
				}
			})
		})

		t.Run("When RecalcFrom hits a failing branch", func(t *testing.T) {
			mustSetFormula(t, g, "B", []string{"A"}, func(cascade.Inputs[string, int]) (int, error) {
				return 0, errBoom
			})

			t.Run("Then it returns the failure", func(t *testing.T) {
				if err := g.RecalcFrom("A"); !errors.Is(err, errBoom) {
					t.Errorf("RecalcFrom = %v, want errBoom", err)
				}
			})
		})
	})
}

func TestBatch(t *testing.T) {
	t.Run("Given a graph settled once per batch", func(t *testing.T) {
		c := newCounter()
		g := cascade.New[string, int]()
		g.SetValue("a", 1)
		g.SetValue("b", 1)
		mustSetFormula(t, g, "total", []string{"a", "b"}, sum(c, "total"))
		mustGet(t, g, "total")

		t.Run("When several values change inside one Batch", func(t *testing.T) {
			c.hits = map[string]int{}
			err := g.Batch(func(b *cascade.Batch[string, int]) error {
				b.SetValue("a", 10)
				b.SetValue("b", 20)
				b.SetValue("c", 30)
				b.Delete("c")
				return nil
			})

			t.Run("Then they coalesce into a single recomputation", func(t *testing.T) {
				if err != nil {
					t.Fatalf("Batch: %v", err)
				}
				if c.hits["total"] != 1 {
					t.Errorf("total computed %d times, want 1", c.hits["total"])
				}
				if got := mustGet(t, g, "total"); got != 30 {
					t.Errorf("total = %d, want 30", got)
				}
			})
		})

		t.Run("When the batch function returns an error", func(t *testing.T) {
			c.hits = map[string]int{}
			err := g.Batch(func(b *cascade.Batch[string, int]) error {
				b.SetValue("a", 0)
				return errBoom
			})

			t.Run("Then the error is returned and no recalculation happens", func(t *testing.T) {
				if !errors.Is(err, errBoom) {
					t.Errorf("Batch = %v, want errBoom", err)
				}
				if len(c.hits) != 0 {
					t.Errorf("recompute counts = %v, want none", c.hits)
				}
				if got := g.Stale(); !slices.Equal(got, []string{"total"}) {
					t.Errorf("Stale() = %v, want [total]", got)
				}
			})
		})

		t.Run("When a batch defines a cyclic formula", func(t *testing.T) {
			err := g.Batch(func(b *cascade.Batch[string, int]) error {
				return b.SetFormula("a", []string{"total"}, sum(nil, "a"))
			})

			t.Run("Then the CycleError surfaces from Batch", func(t *testing.T) {
				var ce *cascade.CycleError[string]
				if !errors.As(err, &ce) {
					t.Errorf("Batch = %v, want *CycleError", err)
				}
			})
		})

		t.Run("When a batch's formula fails", func(t *testing.T) {
			err := g.Batch(func(b *cascade.Batch[string, int]) error {
				return b.SetFormula("bad", nil, func(cascade.Inputs[string, int]) (int, error) {
					return 0, errBoom
				})
			})

			t.Run("Then Batch reports the recalculation error", func(t *testing.T) {
				if !errors.Is(err, errBoom) {
					t.Errorf("Batch = %v, want errBoom", err)
				}
			})
		})

		t.Run("When the batch handle escapes its call", func(t *testing.T) {
			var escaped *cascade.Batch[string, int]
			_ = g.Batch(func(b *cascade.Batch[string, int]) error {
				escaped = b
				return nil
			})

			t.Run("Then using it afterwards panics", func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Error("want a panic")
					}
				}()
				escaped.SetValue("a", 1)
			})
		})
	})
}

func TestOnCompute(t *testing.T) {
	t.Run("Given a hook installed on a chain", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
		mustSetFormula(t, g, "C", []string{"B"}, func(cascade.Inputs[string, int]) (int, error) {
			return 0, errBoom
		})

		type record struct {
			key string
			ok  bool
		}
		var seen []record
		g.OnCompute(func(k string, err error) { seen = append(seen, record{k, err == nil}) })

		t.Run("When Recalc runs", func(t *testing.T) {
			_ = g.Recalc()

			t.Run("Then every computed node is reported with its outcome", func(t *testing.T) {
				want := []record{{"B", true}, {"C", false}}
				if !reflect.DeepEqual(seen, want) {
					t.Errorf("hook saw %v, want %v", seen, want)
				}
			})
		})

		t.Run("When nothing is stale", func(t *testing.T) {
			seen = nil
			_ = g.Recalc()

			t.Run("Then the hook is silent, so skipped work is visible", func(t *testing.T) {
				if len(seen) != 0 {
					t.Errorf("hook saw %v, want nothing", seen)
				}
			})
		})

		t.Run("When the hook is removed", func(t *testing.T) {
			g.OnCompute(nil)
			seen = nil
			g.SetValue("A", 2)
			_ = g.Recalc()

			t.Run("Then it stops firing", func(t *testing.T) {
				if len(seen) != 0 {
					t.Errorf("hook saw %v, want nothing", seen)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// Clone
// ---------------------------------------------------------------------------

func TestClone(t *testing.T) {
	t.Run("Given a computed graph", func(t *testing.T) {
		g := cascade.New[string, int]()
		g.SetValue("A", 1)
		mustSetFormula(t, g, "B", []string{"A"}, sum(nil, "B"))
		mustSetFormula(t, g, "C", []string{"B", "ghost"}, sum(nil, "C"))
		mustGet(t, g, "B")

		t.Run("When it is cloned", func(t *testing.T) {
			cp := g.Clone()

			t.Run("Then the copy carries structure, values and stale flags", func(t *testing.T) {
				if !slices.Equal(cp.Keys(), g.Keys()) {
					t.Errorf("Keys() = %v, want %v", cp.Keys(), g.Keys())
				}
				if !slices.Equal(cp.Stale(), g.Stale()) {
					t.Errorf("Stale() = %v, want %v", cp.Stale(), g.Stale())
				}
				if got := mustGet(t, cp, "B"); got != 1 {
					t.Errorf("B = %d, want 1", got)
				}
				if !slices.Equal(cp.Precedents("C"), []string{"B", "ghost"}) {
					t.Errorf("Precedents(C) = %v", cp.Precedents("C"))
				}
			})

			t.Run("Then mutating the clone leaves the original untouched", func(t *testing.T) {
				cp.SetValue("A", 100)
				cp.SetValue("ghost", 1)
				cp.Delete("B")
				if got := mustGet(t, g, "A"); got != 1 {
					t.Errorf("original A = %d, want 1", got)
				}
				if !g.Has("B") {
					t.Error("original lost B")
				}
				if got := mustGet(t, g, "B"); got != 1 {
					t.Errorf("original B = %d, want 1", got)
				}
				if g.Has("ghost") {
					t.Error("original gained ghost")
				}
			})

			t.Run("Then mutating the original leaves the clone untouched", func(t *testing.T) {
				fresh := g.Clone()
				g.SetValue("A", 55)
				mustSetFormula(t, g, "D", []string{"A"}, sum(nil, "D"))
				if fresh.Has("D") {
					t.Error("clone gained D")
				}
				if got := mustGet(t, fresh, "A"); got != 1 {
					t.Errorf("clone A = %d, want 1", got)
				}
				if got := fresh.Dependents("A"); !slices.Equal(got, []string{"B"}) {
					t.Errorf("clone Dependents(A) = %v, want [B]", got)
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// scale
// ---------------------------------------------------------------------------

const deepN = 100_000

func buildDeep(t testing.TB, n int) *cascade.Graph[int, int] {
	t.Helper()
	g := cascade.New[int, int]()
	g.SetValue(0, 1)
	add1 := func(in cascade.Inputs[int, int]) (int, error) {
		return in.MustGet(in.Keys()[0]) + 1, nil
	}
	for i := 1; i <= n; i++ {
		if err := g.SetFormula(i, []int{i - 1}, add1); err != nil {
			t.Fatalf("SetFormula(%d): %v", i, err)
		}
	}
	return g
}

func TestDeepChain(t *testing.T) {
	t.Run("Given a chain of 100,000 nodes", func(t *testing.T) {
		g := buildDeep(t, deepN)

		t.Run("When it is recalculated", func(t *testing.T) {
			if err := g.Recalc(); err != nil {
				t.Fatalf("Recalc: %v", err)
			}

			t.Run("Then it completes without overflowing the stack", func(t *testing.T) {
				got, err := g.Get(deepN)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if want := deepN + 1; got != want {
					t.Errorf("tail = %d, want %d", got, want)
				}
			})
		})

		t.Run("When the head changes", func(t *testing.T) {
			g.SetValue(0, 2)

			t.Run("Then dirty marking and the pull are also iterative", func(t *testing.T) {
				if n := len(g.Stale()); n != deepN {
					t.Fatalf("stale = %d, want %d", n, deepN)
				}
				got, err := g.Get(deepN)
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if want := deepN + 2; got != want {
					t.Errorf("tail = %d, want %d", got, want)
				}
			})
		})

		t.Run("When a deep chain is cloned and torn down", func(t *testing.T) {
			t.Run("Then Clone and TopoOrder stay iterative too", func(t *testing.T) {
				cp := g.Clone()
				order, err := cp.TopoOrder()
				if err != nil {
					t.Fatalf("TopoOrder: %v", err)
				}
				if len(order) != deepN+1 || order[0] != 0 || order[deepN] != deepN {
					t.Errorf("order length = %d, ends = %v/%v", len(order), order[0], order[len(order)-1])
				}
			})
		})
	})
}

// ---------------------------------------------------------------------------
// benchmarks
// ---------------------------------------------------------------------------

func buildWide(b testing.TB, n int) *cascade.Graph[int, int] {
	b.Helper()
	g := cascade.New[int, int]()
	g.SetValue(-1, 1)
	leaves := make([]int, n)
	for i := range n {
		leaves[i] = i
		if err := g.SetFormula(i, []int{-1}, func(in cascade.Inputs[int, int]) (int, error) {
			return in.MustGet(-1) * 2, nil
		}); err != nil {
			b.Fatalf("SetFormula: %v", err)
		}
	}
	if err := g.SetFormula(-2, leaves, func(in cascade.Inputs[int, int]) (int, error) {
		total := 0
		for _, k := range in.Keys() {
			total += in.MustGet(k)
		}
		return total, nil
	}); err != nil {
		b.Fatalf("SetFormula: %v", err)
	}
	return g
}

func BenchmarkRecalcWide(b *testing.B) {
	g := buildWide(b, 10_000)
	if err := g.Recalc(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.SetValue(-1, i)
		if err := g.Recalc(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecalcDeep(b *testing.B) {
	g := buildDeep(b, 10_000)
	if err := g.Recalc(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.SetValue(0, i)
		if err := g.Recalc(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIncrementalChange measures the case cascade exists for: one input
// changes in a large graph and only the affected sliver is recomputed.
func BenchmarkIncrementalChange(b *testing.B) {
	const n = 10_000
	g := cascade.New[int, int]()
	// n independent pairs: changing one input touches exactly one formula.
	for i := range n {
		g.SetValue(i, i)
		if err := g.SetFormula(n+i, []int{i}, func(in cascade.Inputs[int, int]) (int, error) {
			return in.MustGet(in.Keys()[0]) * 2, nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	if err := g.Recalc(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := i % n
		g.SetValue(k, i)
		if _, err := g.Get(n + k); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildDeep(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = i
		buildDeep(b, 10_000)
	}
}

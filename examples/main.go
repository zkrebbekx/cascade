// Command examples models a SaaS invoice as a dependency graph and watches it
// recalculate: change one input, and only the values that actually depend on it
// are recomputed — in the right order, exactly once each.
package main

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/zkrebbekx/cascade"
)

// cell names, kept as constants so a typo is a compile error rather than a
// silently unresolved reference.
const (
	seats        = "seats"
	pricePerSeat = "price_per_seat"
	overageGB    = "overage_gb"
	overageRate  = "overage_rate"
	discountPct  = "discount_pct"
	taxRate      = "tax_rate"

	seatCost    = "seat_cost"
	overageCost = "overage_cost"
	subtotal    = "subtotal"
	discount    = "discount"
	taxable     = "taxable"
	tax         = "tax"
	total       = "total"
)

var errNegativeSeats = errors.New("seat count cannot be negative")

func main() {
	g := build()

	// Report every node the engine actually computes, so the cost of a change
	// is visible rather than assumed.
	var computed []string
	g.OnCompute(func(k string, err error) {
		if err != nil {
			computed = append(computed, k+" (failed)")
			return
		}
		computed = append(computed, k)
	})

	fmt.Println("== initial calculation ==")
	if err := g.Recalc(); err != nil {
		fmt.Println("error:", err)
	}
	fmt.Printf("computed %d nodes: %s\n", len(computed), strings.Join(computed, " "))
	printInvoice(g)

	// A change to a leaf input touches only its descendants. price_per_seat
	// feeds seat_cost, so subtotal, discount, taxable, tax and total all move —
	// but overage_cost does not, and is not recomputed.
	fmt.Println("\n== customer moves to a cheaper plan ==")
	computed = nil
	g.SetValue(pricePerSeat, 12.00)
	if err := g.Recalc(); err != nil {
		fmt.Println("error:", err)
	}
	fmt.Printf("computed %d nodes: %s\n", len(computed), strings.Join(computed, " "))
	printInvoice(g)

	// Several edits that belong together settle in one pass instead of leaving
	// the graph half-recalculated between them.
	fmt.Println("\n== renewal: more seats, bigger discount, new tax region ==")
	computed = nil
	err := g.Batch(func(b *cascade.Batch[string, float64]) error {
		b.SetValue(seats, 40)
		b.SetValue(discountPct, 0.15)
		b.SetValue(taxRate, 0.0825)
		return nil
	})
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Printf("computed %d nodes: %s\n", len(computed), strings.Join(computed, " "))
	printInvoice(g)

	// A bad input fails one node, and every value downstream of it reports the
	// failure while still naming the node that actually broke.
	fmt.Println("\n== a bad input arrives ==")
	computed = nil
	g.SetValue(seats, -5)
	_ = g.Recalc()
	fmt.Printf("computed %d nodes: %s\n", len(computed), strings.Join(computed, " "))

	if _, err := g.Get(total); err != nil {
		var ee *cascade.EvalError[string]
		if errors.As(err, &ee) {
			fmt.Printf("%s is unavailable because %s failed\n", ee.Key, ee.Cause)
		}
		fmt.Println("underlying:", errors.Is(err, errNegativeSeats))
	}
	// overage_cost does not depend on seats, so it still has a good value.
	fmt.Printf("%-12s %8.2f (unaffected)\n", overageCost, must(g.Get(overageCost)))

	fmt.Println("\n== corrected ==")
	g.SetValue(seats, 40)
	_ = g.Recalc()
	printInvoice(g)

	// A late-arriving input: declare the formula now, supply the value later.
	fmt.Println("\n== adding a credit line that isn't priced yet ==")
	_ = g.SetFormula("balance_due", []string{total, "credit"},
		func(in cascade.Inputs[string, float64]) (float64, error) {
			return in.MustGet(total) - in.MustGet("credit"), nil
		})
	if _, err := g.Get("balance_due"); err != nil {
		fmt.Println("not ready:", err)
	}
	g.SetValue("credit", 100)
	fmt.Printf("balance_due   %8.2f\n", must(g.Get("balance_due")))

	fmt.Println("\n== evaluation order ==")
	order, err := g.TopoOrder()
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(strings.Join(order, " -> "))
}

// build wires the invoice model. Every formula declares exactly the cells it
// reads; cascade rejects any read outside that list.
func build() *cascade.Graph[string, float64] {
	g := cascade.New[string, float64]()

	g.SetValue(seats, 25)
	g.SetValue(pricePerSeat, 15.00)
	g.SetValue(overageGB, 320)
	g.SetValue(overageRate, 0.08)
	g.SetValue(discountPct, 0.10)
	g.SetValue(taxRate, 0.20)

	set(g, seatCost, []string{seats, pricePerSeat}, func(in cascade.Inputs[string, float64]) (float64, error) {
		n := in.MustGet(seats)
		if n < 0 {
			return 0, fmt.Errorf("%w: got %.0f", errNegativeSeats, n)
		}
		return n * in.MustGet(pricePerSeat), nil
	})

	set(g, overageCost, []string{overageGB, overageRate}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet(overageGB) * in.MustGet(overageRate), nil
	})

	set(g, subtotal, []string{seatCost, overageCost}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet(seatCost) + in.MustGet(overageCost), nil
	})

	set(g, discount, []string{subtotal, discountPct}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return round2(in.MustGet(subtotal) * in.MustGet(discountPct)), nil
	})

	set(g, taxable, []string{subtotal, discount}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet(subtotal) - in.MustGet(discount), nil
	})

	set(g, tax, []string{taxable, taxRate}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return round2(in.MustGet(taxable) * in.MustGet(taxRate)), nil
	})

	set(g, total, []string{taxable, tax}, func(in cascade.Inputs[string, float64]) (float64, error) {
		return in.MustGet(taxable) + in.MustGet(tax), nil
	})

	return g
}

// set panics on a cycle: the model is a literal in this file, so a cycle here
// is a bug in the program rather than a runtime condition to handle.
func set(g *cascade.Graph[string, float64], k string, deps []string, fn func(cascade.Inputs[string, float64]) (float64, error)) {
	if err := g.SetFormula(k, deps, fn); err != nil {
		panic(err)
	}
}

func printInvoice(g *cascade.Graph[string, float64]) {
	for _, k := range []string{seatCost, overageCost, subtotal, discount, taxable, tax, total} {
		v, err := g.Get(k)
		if err != nil {
			fmt.Printf("%-12s %8s\n", k, "#ERR")
			continue
		}
		fmt.Printf("%-12s %8.2f\n", k, v)
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func must(v float64, err error) float64 {
	if err != nil {
		panic(err)
	}
	return v
}

package billing

import "testing"

func TestCompute(t *testing.T) {
	inv := Compute("inv1", "acme", []Line{{SKU: "a", Quantity: 2, UnitCents: 150}, {SKU: "b", Quantity: 1, UnitCents: 99}})
	if inv.TotalCents != 399 {
		t.Errorf("Compute total = %d, want 399", inv.TotalCents)
	}
}

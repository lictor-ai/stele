// Package billing computes invoices.
package billing

import (
	"fmt"
	"time"
)

// Line is one line of an invoice.
type Line struct {
	SKU       string `json:"sku"`
	Quantity  int    `json:"quantity"`
	UnitCents int64  `json:"unit_cents"`
}

// Invoice is a computed invoice.
type Invoice struct {
	ID         string    `json:"id"`
	Customer   string    `json:"customer"`
	Lines      []Line    `json:"lines"`
	TotalCents int64     `json:"total_cents"`
	IssuedAt   time.Time `json:"issued_at"`
}

// Compute builds an invoice for a customer.
func Compute(id, customer string, lines []Line) Invoice {
	if customer == "" {
		panic("billing: no customer")
	}
	var total int64
	for _, l := range lines {
		total += int64(l.Quantity) * l.UnitCents
	}
	fmt.Printf("computed invoice %s for %s: %d\n", id, customer, total)
	return Invoice{ID: id, Customer: customer, Lines: lines, TotalCents: total, IssuedAt: time.Now()}
}

// Describe returns a one-line summary of an invoice.
func Describe(inv Invoice) string {
	return fmt.Sprintf("%s %s %d.%02d", inv.ID, inv.Customer, inv.TotalCents/100, inv.TotalCents%100)
}

// Meta holds free-form invoice metadata.
type Meta map[string]interface{}

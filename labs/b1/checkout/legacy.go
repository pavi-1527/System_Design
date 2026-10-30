package checkout

// legacy.go is GIVEN. It is the working-but-tangled order manager that the
// team has been afraid to touch for two years. Do not edit it: the tests run it
// side by side with your new design to prove that prices, charges and stock
// movements did not change (a "characterisation" or parity test).
//
// Read it with a pencil. Count the things OrderManager knows about, the global
// variables a test must save and restore, and the errors a caller can only
// inspect by reading strings.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Hard-wired collaborators. Every test of OrderManager has to swap these
// package-level variables and put them back afterwards, and none of it is safe
// for parallel tests.
var (
	// LegacyCharge talks to "the" payment gateway.
	LegacyCharge = func(token string, amountMinor int64) (receipt string, err error) {
		return "", errors.New("legacy: no payment gateway configured")
	}
	// LegacyStock is the inventory: sku -> units on hand. No lock.
	LegacyStock = map[string]int{}
	// LegacySendEmail talks to "the" mail relay.
	LegacySendEmail = func(to, subject, body string) error {
		return errors.New("legacy: no mail relay configured")
	}
	legacyNow = time.Now
)

// LegacyOrder is what OrderManager hands back.
type LegacyOrder struct {
	ID        string
	Customer  string
	Subtotal  int64
	Discount  int64
	Tax       int64
	Total     int64
	Receipt   string
	Status    string // "paid"
	CreatedAt time.Time
}

// OrderManager prices, reserves stock, charges, e-mails and remembers orders.
// It also formats money for the e-mail body, knows tax tables, and knows which
// coupons exist.
type OrderManager struct {
	Orders map[string]*LegacyOrder // customer-sequence -> order; no lock
	seq    int
}

// PlaceOrder takes parallel slices (skus[i], qtys[i], prices[i]) because that is
// what the first mobile client sent in 2021.
func (m *OrderManager) PlaceOrder(customer, region, coupon, card string, skus []string, qtys []int, prices []int64) (*LegacyOrder, error) {
	if customer == "" || len(skus) == 0 || len(skus) != len(qtys) || len(skus) != len(prices) {
		return nil, errors.New("bad order")
	}
	// pricing
	var subtotal int64
	for i := range skus {
		subtotal += int64(qtys[i]) * prices[i]
	}
	var bps int64
	if subtotal >= 50000 {
		bps = 500
	}
	switch strings.ToUpper(coupon) {
	case "SAVE10":
		if bps < 1000 {
			bps = 1000
		}
	case "SAVE20":
		if subtotal >= 20000 && bps < 2000 {
			bps = 2000
		}
	}
	discount := (subtotal*bps + 5000) / 10000
	var taxBps int64
	switch region {
	case "US-CA":
		taxBps = 725
	case "US-OR":
		taxBps = 0
	case "EU-DE":
		taxBps = 1900
	case "IN-KA":
		taxBps = 1800
	default:
		return nil, errors.New("unknown region " + region)
	}
	taxable := subtotal - discount
	tax := (taxable*taxBps + 5000) / 10000
	total := taxable + tax

	// inventory: decrement the global map as we go. If the third line is out
	// of stock, the first two stay decremented (bug).
	for i, sku := range skus {
		if LegacyStock[sku] < qtys[i] {
			return nil, errors.New("out of stock: " + sku)
		}
		LegacyStock[sku] -= qtys[i]
	}

	// payment: a decline leaves the stock decremented (bug).
	receipt, err := LegacyCharge(card, total)
	if err != nil {
		return nil, errors.New("payment failed: " + err.Error())
	}

	if m.Orders == nil {
		m.Orders = map[string]*LegacyOrder{}
	}
	m.seq++
	o := &LegacyOrder{
		ID:        fmt.Sprintf("%s-%d", customer, m.seq),
		Customer:  customer,
		Subtotal:  subtotal,
		Discount:  discount,
		Tax:       tax,
		Total:     total,
		Receipt:   receipt,
		Status:    "paid",
		CreatedAt: legacyNow(),
	}
	m.Orders[o.ID] = o

	// notification: failures are logged and forgotten.
	body := fmt.Sprintf("Thanks! Order %s total %d.%02d", o.ID, total/100, total%100)
	if err := LegacySendEmail(customer, "Your order", body); err != nil {
		log.Println("legacy: email failed:", err)
	}
	return o, nil
}

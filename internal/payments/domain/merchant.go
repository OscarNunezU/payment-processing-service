package domain

import (
	"errors"
	"fmt"
)

// Currency is an ISO 4217 code. Amounts are integers in its minor unit: CLP
// has no decimals and USD is in cents.
type Currency string

const (
	CLP Currency = "CLP"
	USD Currency = "USD"
)

// ErrUnsupportedCurrency means the service does not take payments in that
// currency.
var ErrUnsupportedCurrency = errors.New("the currency is not supported")

// ParseCurrency accepts the currencies the service supports.
func ParseCurrency(s string) (Currency, error) {
	switch c := Currency(s); c {
	case CLP, USD:
		return c, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnsupportedCurrency, s)
}

// MerchantStatus is where a merchant is in its lifecycle.
type MerchantStatus string

const (
	MerchantActive    MerchantStatus = "ACTIVE"
	MerchantSuspended MerchantStatus = "SUSPENDED"
)

// ErrMerchantSuspended means the merchant may query its payments but not
// create new ones (FR03).
var ErrMerchantSuspended = errors.New("the merchant is suspended")

// Merchant is a business that takes payments through the service.
type Merchant struct {
	ID        string
	LegalName string
	RUT       RUT
	Status    MerchantStatus
	// MaxAmount is the largest payment the merchant may take in each
	// currency. A currency without a limit is not accepted.
	MaxAmount map[Currency]int64
}

// CanCreatePayments reports whether the merchant may create a payment now.
func (m Merchant) CanCreatePayments() error {
	if m.Status != MerchantActive {
		return ErrMerchantSuspended
	}
	return nil
}

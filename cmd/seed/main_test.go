package main

import (
	"testing"

	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

func TestTheSeedDataIsValid(t *testing.T) {
	ids, ruts := map[string]bool{}, map[domain.RUT]bool{}
	for _, lm := range localMerchants {
		m := lm.merchant
		if rut, err := domain.ParseRUT(string(m.RUT)); err != nil || rut != m.RUT {
			t.Errorf("%s: RUT %s is not valid and canonical", m.LegalName, m.RUT)
		}
		if _, ok := domain.HashAPIKey(lm.key); !ok {
			t.Errorf("%s: key %q is not shaped like a key", m.LegalName, lm.key)
		}
		if ids[m.ID] || ruts[m.RUT] {
			t.Errorf("%s: its id or RUT is repeated", m.LegalName)
		}
		ids[m.ID], ruts[m.RUT] = true, true
	}
}

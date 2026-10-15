package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type Service struct {
	repo   Repository
	vault  CardVault
	limits Limits
	now    func() time.Time
}

func NewService(repo Repository, vault CardVault, limits Limits) *Service {
	return &Service{repo: repo, vault: vault, limits: limits, now: time.Now}
}

// Create validates the payment, applies the business rules and stores it as
// PENDING with the card sealed. The acquirer is called later, by the worker.
func (s *Service) Create(ctx context.Context, p NewPayment) (Transaction, error) {
	if err := p.Validate(); err != nil {
		return Transaction{}, err
	}
	if err := CheckRules(p, s.limits, s.now()); err != nil {
		return Transaction{}, err
	}

	sealed, err := s.sealCard(p.Card)
	if err != nil {
		return Transaction{}, err
	}

	return s.repo.Create(ctx, Transaction{
		MerchantID: p.MerchantID,
		Amount:     p.Amount,
		Currency:   p.Currency,
		Status:     StatusPending,
		CardLast4:  p.Card.Last4(),
		CardData:   sealed,
	})
}

// cardSecret is how a card is serialized inside the vault. It is a separate
// type so that a new field in Card never changes the stored format by accident.
type cardSecret struct {
	Number      string `json:"number"`
	ExpiryMonth int    `json:"expiry_month"`
	ExpiryYear  int    `json:"expiry_year"`
	CVV         string `json:"cvv"`
}

// sealCard is the only place where the full number and the CVV are written
// anywhere, and they leave it encrypted.
func (s *Service) sealCard(c Card) ([]byte, error) {
	plain, err := json.Marshal(cardSecret{
		Number:      c.Number,
		ExpiryMonth: c.ExpiryMonth,
		ExpiryYear:  c.ExpiryYear,
		CVV:         c.CVV,
	})
	if err != nil {
		return nil, fmt.Errorf("encode card: %w", err)
	}
	sealed, err := s.vault.Seal(plain)
	if err != nil {
		return nil, fmt.Errorf("seal card: %w", err)
	}
	return sealed, nil
}

package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 100
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

// Create stores the payment as PENDING with the card sealed, once per
// idempotency key: a retry gets the same payment back and the bool reports it
// was replayed; a different payment with a used key gets
// ErrIdempotencyKeyReused. The acquirer is called later, by the worker.
func (s *Service) Create(ctx context.Context, p NewPayment) (Transaction, bool, error) {
	if err := p.Validate(); err != nil {
		return Transaction{}, false, err
	}
	fp := fingerprint(p)

	if ruleErr := CheckRules(p, s.limits, s.now()); ruleErr != nil {
		// A retry of a payment already accepted still gets it back, even if a
		// limit changed or the card expired in between.
		existing, found, err := s.repo.FindByIdempotencyKey(ctx, p.MerchantID, p.IdempotencyKey)
		if err != nil {
			return Transaction{}, false, err
		}
		if !found {
			return Transaction{}, false, ruleErr
		}
		return replay(existing, fp)
	}
	sealed, err := s.sealCard(p.Card)
	if err != nil {
		return Transaction{}, false, err
	}

	stored, created, err := s.repo.Create(ctx, Transaction{
		MerchantID:         p.MerchantID,
		IdempotencyKey:     p.IdempotencyKey,
		RequestFingerprint: fp,
		Amount:             p.Amount,
		Currency:           p.Currency,
		Status:             StatusPending,
		CardLast4:          p.Card.Last4(),
		CardData:           sealed,
	})
	if err != nil {
		return Transaction{}, false, err
	}
	if !created {
		return replay(stored, fp)
	}
	stored.CardData = nil
	return stored, false, nil
}

func (s *Service) Get(ctx context.Context, id string) (Transaction, error) {
	return s.repo.Get(ctx, id)
}

// List returns the transactions that match the filter, newest first. Without a
// limit it returns DefaultListLimit, and it never returns more than MaxListLimit.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]Transaction, error) {
	if filter.Limit <= 0 {
		filter.Limit = DefaultListLimit
	}
	filter.Limit = min(filter.Limit, MaxListLimit)
	return s.repo.List(ctx, filter)
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

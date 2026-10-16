package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DefaultLease is how long a worker keeps a payment before another worker may
// take it over. It must be longer than processing a payment takes.
const DefaultLease = 30 * time.Second

// Reasons the service itself gives when it ends a payment as FAILED. Decline
// reasons, instead, come from the acquirer.
const (
	ReasonCardUnreadable = "card_unreadable"
	ReasonAcquirerError  = "acquirer_error"
)

// Processor is the worker's use case: it takes waiting payments to their final
// state. It is apart from Service because it needs the acquirer, and the API
// never does.
type Processor struct {
	repo     Repository
	vault    CardVault
	acquirer Acquirer
	lease    time.Duration
}

func NewProcessor(repo Repository, vault CardVault, acquirer Acquirer) *Processor {
	return &Processor{repo: repo, vault: vault, acquirer: acquirer, lease: DefaultLease}
}

// ProcessNext claims the payment that has waited the longest, authorizes it and
// records how it ended. The returned transaction never carries card data.
//
//   - processed is false when nothing was waiting, or the claim itself failed.
//   - An error with processed true means no outcome was recorded: the
//     processing was interrupted or the outcome could not be stored. The
//     payment stays PROCESSING and is taken again when its lease expires.
func (p *Processor) ProcessNext(ctx context.Context) (txn Transaction, processed bool, err error) {
	var ok bool
	txn, ok, err = p.repo.ClaimNext(ctx, p.lease)
	if err != nil || !ok {
		return Transaction{}, false, err
	}
	sealed := txn.CardData
	txn.CardData = nil

	outcome, err := p.authorize(ctx, txn, sealed)
	if err != nil {
		return txn, true, fmt.Errorf("process %s: %w", txn.ID, err)
	}
	if !txn.Status.CanTransitionTo(outcome.Status) {
		return txn, true, fmt.Errorf("process %s: %s to %s is not allowed", txn.ID, txn.Status, outcome.Status)
	}
	if err := p.repo.Finish(ctx, txn.ID, outcome); err != nil {
		return txn, true, fmt.Errorf("finish %s: %w", txn.ID, err)
	}

	txn.Status, txn.Reason, txn.AuthorizationCode = outcome.Status, outcome.Reason, outcome.AuthorizationCode
	return txn, true, nil
}

// authorize turns the acquirer's answer, or the lack of one, into an outcome.
// A decline is an answer and ends DECLINED; no answer ends FAILED. The
// exception is when our own context ended (the worker is stopping): the
// acquirer may still have approved, so nothing is decided and an error is
// returned instead.
func (p *Processor) authorize(ctx context.Context, txn Transaction, sealed []byte) (Outcome, error) {
	card, err := p.openCard(sealed)
	if err != nil {
		return Outcome{Status: StatusFailed, Reason: ReasonCardUnreadable}, nil
	}

	result, err := p.acquirer.Authorize(ctx, AuthorizationRequest{
		TransactionID: txn.ID,
		Amount:        txn.Amount,
		Currency:      txn.Currency,
		Card:          card,
	})
	switch {
	case err != nil && ctx.Err() != nil:
		return Outcome{}, fmt.Errorf("interrupted while waiting for the acquirer: %w", ctx.Err())
	case err != nil:
		return Outcome{Status: StatusFailed, Reason: ReasonAcquirerError}, nil
	case result.Approved:
		return Outcome{Status: StatusApproved, AuthorizationCode: result.AuthorizationCode}, nil
	default:
		return Outcome{Status: StatusDeclined, Reason: result.DeclineReason}, nil
	}
}

func (p *Processor) openCard(sealed []byte) (Card, error) {
	if sealed == nil {
		return Card{}, errors.New("no card data")
	}
	plain, err := p.vault.Open(sealed)
	if err != nil {
		return Card{}, err
	}
	var secret cardSecret
	if err := json.Unmarshal(plain, &secret); err != nil {
		return Card{}, err
	}
	return Card{
		Number:      secret.Number,
		ExpiryMonth: secret.ExpiryMonth,
		ExpiryYear:  secret.ExpiryYear,
		CVV:         secret.CVV,
	}, nil
}

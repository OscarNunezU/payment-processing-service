package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// DefaultLease is how long a worker keeps a payment before another worker may
// take it over. It must be longer than processing a payment takes.
const DefaultLease = 30 * time.Second

// Retries for a payment the acquirer gives no answer to: three attempts, with
// waits of 1-2s and then 2-4s, ride out a failure of a few seconds without
// holding the payment for long.
const (
	DefaultMaxAttempts    = 3
	DefaultRetryBaseDelay = 2 * time.Second
)

// Reasons the service itself gives when it ends a payment as FAILED, or when
// an attempt fails and is retried. Decline reasons, instead, come from the
// acquirer.
const (
	ReasonCardUnreadable      = "card_unreadable"
	ReasonAcquirerRejected    = "acquirer_rejected"
	ReasonAcquirerUnavailable = "acquirer_unavailable"
)

// errNoAnswer marks an attempt the acquirer gave no answer to: a temporary
// failure, so the attempt can be made again.
var errNoAnswer = errors.New("no answer from the acquirer")

// Processor is the worker's use case: it takes waiting payments to their final
// state. It is apart from Service because it needs the acquirer, and the API
// never does.
type Processor struct {
	repo        Repository
	vault       CardVault
	acquirer    Acquirer
	lease       time.Duration
	maxAttempts int
	retryBase   time.Duration
}

func NewProcessor(repo Repository, vault CardVault, acquirer Acquirer) *Processor {
	return &Processor{
		repo:        repo,
		vault:       vault,
		acquirer:    acquirer,
		lease:       DefaultLease,
		maxAttempts: DefaultMaxAttempts,
		retryBase:   DefaultRetryBaseDelay,
	}
}

// ProcessNext claims the payment that has waited the longest, makes one attempt
// to authorize it and records how it went. The returned transaction never
// carries card data.
//
//   - processed is false when nothing was waiting, or the claim itself failed.
//   - ErrRetryScheduled means the acquirer gave no answer and another attempt
//     was scheduled; the last attempt without an answer ends FAILED instead.
//   - Any other error with processed true means nothing was recorded: the
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
	switch {
	case errors.Is(err, errNoAnswer) && txn.Attempts < p.maxAttempts:
		delay := retryDelay(p.retryBase, txn.Attempts)
		if err := p.repo.Retry(ctx, txn.ID, ReasonAcquirerUnavailable, delay); err != nil {
			return txn, true, fmt.Errorf("schedule a retry of %s: %w", txn.ID, err)
		}
		return txn, true, fmt.Errorf("process %s: %w in %s: %w", txn.ID, ErrRetryScheduled, delay.Round(time.Millisecond), err)
	case errors.Is(err, errNoAnswer):
		outcome = Outcome{Status: StatusFailed, Reason: ReasonAcquirerUnavailable}
	case err != nil:
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
// A decline is an answer and ends DECLINED, and a rejected request ends FAILED.
// No answer is errNoAnswer, so the attempt can be made again. When our own
// context ended (the payment ran out of time), the acquirer may still have
// approved, so nothing is decided and an error is returned instead.
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
	case errors.Is(err, ErrAcquirerRejected):
		return Outcome{Status: StatusFailed, Reason: ReasonAcquirerRejected}, nil
	case err != nil:
		return Outcome{}, fmt.Errorf("%w: %w", errNoAnswer, err)
	case result.Approved:
		return Outcome{Status: StatusApproved, AuthorizationCode: result.AuthorizationCode}, nil
	default:
		return Outcome{Status: StatusDeclined, Reason: result.DeclineReason}, nil
	}
}

// retryDelay is the wait after a failed attempt: it doubles with each attempt
// (base, then twice base) and half of it is random, so payments that failed
// together do not all come back at the same instant.
func retryDelay(base time.Duration, attempt int) time.Duration {
	d := base << (attempt - 1)
	return d/2 + rand.N(d/2+1)
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

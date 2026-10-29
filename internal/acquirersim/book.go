// Package acquirersim simulates the acquirer that payments-worker calls: card
// tokenization, authorizations by idempotency key, reversals that leave a
// tombstone, test cards and injected faults (RFC 0001, section 10). It stands
// for another company, so it shares no code with the payment service.
package acquirersim

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errReversed    = errors.New("the key was reversed, and can never be authorized")
	errKeyReused   = errors.New("the key was already used for a different request")
	errUnavailable = errors.New("the acquirer is unavailable")
	errRejected    = errors.New("the acquirer rejected the request")
)

// Outcome is what the acquirer decided for a key.
type Outcome string

const (
	Approved Outcome = "approved"
	Declined Outcome = "declined"
	// None is a key that was reversed before any authorization reached it.
	None Outcome = "none"
)

// Entry is one key in the book.
type Entry struct {
	Key               string     `json:"key"`
	Outcome           Outcome    `json:"outcome"`
	Reference         string     `json:"reference,omitempty"`
	Amount            int64      `json:"amount,omitempty"`
	Currency          string     `json:"currency,omitempty"`
	AuthorizationCode string     `json:"authorization_code,omitempty"`
	DeclineCode       string     `json:"decline_code,omitempty"`
	Brand             string     `json:"brand,omitempty"`
	Last4             string     `json:"last4,omitempty"`
	Reversed          bool       `json:"reversed"`
	CreatedAt         time.Time  `json:"created_at"`
	ReversedAt        *time.Time `json:"reversed_at,omitempty"`

	hash     [sha256.Size]byte
	behavior behavior
}

// Live reports whether the entry holds an authorization that was not reversed:
// money held on the cardholder's card.
func (e Entry) Live() bool { return e.Outcome == Approved && !e.Reversed }

// AuthorizeRequest is an authorization, as the acquirer receives it.
type AuthorizeRequest struct {
	Key       string
	Token     string
	Amount    int64
	Currency  string
	Reference string
}

func (r AuthorizeRequest) hash() [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		r.Token, strconv.FormatInt(r.Amount, 10), r.Currency, r.Reference,
	}, "\x00")))
}

// authorization is the result of Book.Authorize.
type authorization struct {
	entry Entry
	// replayed is true when the key already had an answer, which is returned
	// as it was given the first time.
	replayed bool
}

// Book records every key the acquirer has seen, and makes each authorization
// idempotent: a key is decided once, and every later call with it gets the
// same answer. A reversed key keeps a tombstone forever, so a request delayed
// in the network cannot authorize it afterwards.
type Book struct {
	mu      sync.Mutex
	entries map[string]*Entry
	// refused counts the 503 answers given to each key, for unavailableOnce.
	refused map[string]int
	tokens  *tokens
	now     func() time.Time
}

func newBook(t *tokens, now func() time.Time) *Book {
	return &Book{entries: make(map[string]*Entry), refused: make(map[string]int), tokens: t, now: now}
}

// Authorize decides r, or returns the answer already given to its key. The
// decision is recorded before anything is answered, so a client that times
// out still finds it on its retry.
func (b *Book) Authorize(r AuthorizeRequest) (authorization, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	hash := r.hash()
	if e, ok := b.entries[r.Key]; ok {
		switch {
		case e.Reversed:
			return authorization{}, errReversed
		case e.hash != hash:
			return authorization{}, errKeyReused
		}
		return authorization{entry: *e, replayed: true}, nil
	}

	c, err := b.tokens.use(r.Token, r.Key)
	if err != nil {
		return authorization{}, err
	}
	switch c.behavior() {
	case unavailable:
		return authorization{}, errUnavailable
	case unavailableOnce:
		if b.refused[r.Key] == 0 {
			b.refused[r.Key]++
			return authorization{}, errUnavailable
		}
	case rejectRequest:
		return authorization{}, errRejected
	}

	e := &Entry{
		Key:       r.Key,
		Outcome:   Approved,
		Reference: r.Reference,
		Amount:    r.Amount,
		Currency:  r.Currency,
		Brand:     c.brand,
		Last4:     c.last4(),
		CreatedAt: b.now(),
		hash:      hash,
		behavior:  c.behavior(),
	}
	switch c.behavior() {
	case declineCard:
		e.Outcome, e.DeclineCode = Declined, "card_declined"
	case declineFunds:
		e.Outcome, e.DeclineCode = Declined, "insufficient_funds"
	default:
		e.AuthorizationCode = rand.Text()[:6]
	}
	b.entries[r.Key] = e
	return authorization{entry: *e}, nil
}

// Reverse releases the authorization of key, if there is one, and leaves a
// tombstone that rejects any later authorization with it. It works for a key
// the book never saw, and reversing twice changes nothing.
func (b *Book) Reverse(key string) Entry {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	e, ok := b.entries[key]
	if !ok {
		e = &Entry{Key: key, Outcome: None, CreatedAt: now}
		b.entries[key] = e
	}
	if !e.Reversed {
		e.Reversed, e.ReversedAt = true, &now
	}
	return *e
}

// Entries returns every key, oldest first.
func (b *Book) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, 0, len(b.entries))
	for _, e := range b.entries {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b Entry) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.Key, b.Key))
	})
	return out
}

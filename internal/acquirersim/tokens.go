package acquirersim

import (
	"crypto/rand"
	"errors"
	"sync"
	"time"
)

var (
	errTokenUnknown = errors.New("the token does not exist")
	errTokenExpired = errors.New("the token expired")
	errTokenUsed    = errors.New("the token already paid for another authorization")
)

// tokens keeps the cards behind their tokens. A token pays for a single
// authorization: once a key uses it, only that key may use it again, for its
// retries.
type tokens struct {
	mu        sync.Mutex
	ttl       time.Duration
	now       func() time.Time
	byID      map[string]*token
	lastPurge time.Time
}

type token struct {
	card    card
	expires time.Time
	usedBy  string
}

func newTokens(ttl time.Duration, now func() time.Time) *tokens {
	return &tokens{ttl: ttl, now: now, byID: make(map[string]*token)}
}

// create stores c and returns its token and expiry.
func (t *tokens) create(c card) (string, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.purge(now)
	id := "tok_" + rand.Text()
	expires := now.Add(t.ttl)
	t.byID[id] = &token{card: c, expires: expires}
	return id, expires
}

// use returns the card behind id for the authorization with key.
func (t *tokens) use(id, key string) (card, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tok, ok := t.byID[id]
	switch {
	case !ok:
		return card{}, errTokenUnknown
	case tok.usedBy == key:
		// A retry of the same authorization: the token was valid when it began.
		return tok.card, nil
	case tok.usedBy != "":
		return card{}, errTokenUsed
	case !t.now().Before(tok.expires):
		return card{}, errTokenExpired
	}
	tok.usedBy = key
	return tok.card, nil
}

// purge drops the tokens that expired without being used, once a minute at
// most. A used token stays, so its authorization can still be retried.
func (t *tokens) purge(now time.Time) {
	if now.Sub(t.lastPurge) < time.Minute {
		return
	}
	t.lastPurge = now
	for id, tok := range t.byID {
		if tok.usedBy == "" && !now.Before(tok.expires) {
			delete(t.byID, id)
		}
	}
}

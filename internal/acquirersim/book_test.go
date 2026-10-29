package acquirersim

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// clock is a time source that tests move by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestBook(t *testing.T) (*Book, *tokens, *clock) {
	t.Helper()
	c := &clock{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	tk := newTokens(time.Hour, c.Now)
	return newBook(tk, c.Now), tk, c
}

// request tokenizes number and returns an authorization for key that pays
// 15,000 CLP with it.
func request(tk *tokens, key, number string) AuthorizeRequest {
	id, _ := tk.create(card{number: number, brand: brandOf(number), expMonth: 12, expYear: 2030})
	return AuthorizeRequest{Key: key, Token: id, Amount: 15000, Currency: "CLP", Reference: "order-1"}
}

func TestAKeyIsDecidedOnceAndReplayedAfterwards(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4111111111111111")

	first, err := b.Authorize(req)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if first.replayed || first.entry.Outcome != Approved || len(first.entry.AuthorizationCode) != 6 || !first.entry.Live() {
		t.Fatalf("got %+v, want a live approval with a 6-character code", first)
	}
	if first.entry.Brand != "visa" || first.entry.Last4 != "1111" {
		t.Errorf("got card %s %s, want visa 1111", first.entry.Brand, first.entry.Last4)
	}

	again, err := b.Authorize(req)
	if err != nil {
		t.Fatalf("authorize again: %v", err)
	}
	if !again.replayed || again.entry.AuthorizationCode != first.entry.AuthorizationCode {
		t.Errorf("got %+v, want the first answer replayed", again)
	}
	if n := len(b.Entries()); n != 1 {
		t.Errorf("the book has %d entries, want 1", n)
	}
}

func TestTheSameKeyWithAnotherRequestIsRejected(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4111111111111111")
	if _, err := b.Authorize(req); err != nil {
		t.Fatal(err)
	}
	req.Amount++
	if _, err := b.Authorize(req); !errors.Is(err, errKeyReused) {
		t.Errorf("got %v, want errKeyReused", err)
	}
}

func TestDeclineCards(t *testing.T) {
	tests := map[string]string{
		"4000000000000002": "card_declined",
		"4000000000009995": "insufficient_funds",
	}
	for number, code := range tests {
		b, tk, _ := newTestBook(t)
		auth, err := b.Authorize(request(tk, "k1", number))
		if err != nil {
			t.Fatalf("%s: %v", number, err)
		}
		if auth.entry.Outcome != Declined || auth.entry.DeclineCode != code || auth.entry.AuthorizationCode != "" || auth.entry.Live() {
			t.Errorf("%s: got %+v, want declined with %s", number, auth.entry, code)
		}
	}
}

func TestAnUnavailableAcquirerRecordsNothing(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4000000000000119")
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := b.Authorize(req); !errors.Is(err, errUnavailable) {
			t.Fatalf("attempt %d: got %v, want errUnavailable", attempt, err)
		}
	}
	if n := len(b.Entries()); n != 0 {
		t.Errorf("the book has %d entries, want none", n)
	}
}

func TestUnavailableOnlyTheFirstTime(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4000000000000127")
	if _, err := b.Authorize(req); !errors.Is(err, errUnavailable) {
		t.Fatalf("first attempt: got %v, want errUnavailable", err)
	}
	auth, err := b.Authorize(req)
	if err != nil || auth.entry.Outcome != Approved {
		t.Errorf("second attempt: got %+v, %v, want approved", auth.entry, err)
	}
}

func TestARetryAfterARefusalMustBeTheSameRequest(t *testing.T) {
	for _, number := range []string{"4000000000000127", "4000000000000119", "4000000000000143"} {
		b, tk, _ := newTestBook(t)
		req := request(tk, "k1", number)
		if _, err := b.Authorize(req); err == nil {
			t.Fatalf("%s: the first attempt was accepted, want it refused or rejected", number)
		}
		changed := req
		changed.Amount++
		if _, err := b.Authorize(changed); !errors.Is(err, errKeyReused) {
			t.Errorf("%s: a retry with another amount got %v, want errKeyReused", number, err)
		}
	}
}

func TestAnInvalidTokenDoesNotBindTheKey(t *testing.T) {
	b, tk, _ := newTestBook(t)
	bad := AuthorizeRequest{Key: "k1", Token: "tok_nope", Amount: 15000, Currency: "CLP", Reference: "order-1"}
	if _, err := b.Authorize(bad); !errors.Is(err, errTokenUnknown) {
		t.Fatalf("got %v, want errTokenUnknown", err)
	}
	if _, err := b.Authorize(request(tk, "k1", "4111111111111111")); err != nil {
		t.Errorf("the corrected request with the same key: got %v, want it accepted", err)
	}
}

func TestARejectedRequestRecordsNothing(t *testing.T) {
	b, tk, _ := newTestBook(t)
	if _, err := b.Authorize(request(tk, "k1", "4000000000000143")); !errors.Is(err, errRejected) {
		t.Fatalf("got %v, want errRejected", err)
	}
	if n := len(b.Entries()); n != 0 {
		t.Errorf("the book has %d entries, want none", n)
	}
}

func TestATokenPaysForOneKey(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4000000000000127")
	// The first attempt is refused, but it already ties the token to k1.
	if _, err := b.Authorize(req); !errors.Is(err, errUnavailable) {
		t.Fatalf("got %v, want errUnavailable", err)
	}
	other := req
	other.Key = "k2"
	if _, err := b.Authorize(other); !errors.Is(err, errTokenUsed) {
		t.Errorf("another key: got %v, want errTokenUsed", err)
	}
	if _, err := b.Authorize(req); err != nil {
		t.Errorf("the retry of k1: got %v, want it accepted", err)
	}
}

func TestUnknownAndExpiredTokens(t *testing.T) {
	b, tk, c := newTestBook(t)
	if _, err := b.Authorize(AuthorizeRequest{Key: "k1", Token: "tok_nope", Amount: 1, Currency: "CLP"}); !errors.Is(err, errTokenUnknown) {
		t.Errorf("unknown token: got %v, want errTokenUnknown", err)
	}

	req := request(tk, "k2", "4111111111111111")
	c.advance(time.Hour)
	if _, err := b.Authorize(req); !errors.Is(err, errTokenExpired) {
		t.Errorf("token after its hour: got %v, want errTokenExpired", err)
	}
}

func TestAUsedTokenStillServesItsRetriesAfterItExpires(t *testing.T) {
	b, tk, c := newTestBook(t)
	req := request(tk, "k1", "4000000000000127")
	if _, err := b.Authorize(req); !errors.Is(err, errUnavailable) {
		t.Fatalf("got %v, want errUnavailable", err)
	}
	c.advance(2 * time.Hour)
	tk.create(card{number: "4111111111111111"}) // runs the purge
	if _, err := b.Authorize(req); err != nil {
		t.Errorf("the retry: got %v, want it accepted", err)
	}
}

func TestAReversalReleasesTheAuthorizationAndBlocksTheKey(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4111111111111111")
	if _, err := b.Authorize(req); err != nil {
		t.Fatal(err)
	}

	e := b.Reverse("k1")
	if e.Outcome != Approved || !e.Reversed || e.ReversedAt == nil || e.Live() {
		t.Errorf("got %+v, want an approval that is reversed and no longer live", e)
	}
	if _, err := b.Authorize(req); !errors.Is(err, errReversed) {
		t.Errorf("authorize after the reversal: got %v, want errReversed", err)
	}
}

func TestAReversalOfAnUnknownKeyLeavesATombstone(t *testing.T) {
	b, tk, _ := newTestBook(t)
	e := b.Reverse("k1")
	if e.Outcome != None || !e.Reversed {
		t.Fatalf("got %+v, want a tombstone", e)
	}
	// The authorization was delayed in the network and arrives afterwards.
	if _, err := b.Authorize(request(tk, "k1", "4111111111111111")); !errors.Is(err, errReversed) {
		t.Errorf("got %v, want errReversed", err)
	}
}

func TestReversingTwiceChangesNothing(t *testing.T) {
	b, tk, c := newTestBook(t)
	if _, err := b.Authorize(request(tk, "k1", "4111111111111111")); err != nil {
		t.Fatal(err)
	}
	first := b.Reverse("k1")
	c.advance(time.Minute)
	second := b.Reverse("k1")
	if !second.ReversedAt.Equal(*first.ReversedAt) || second.Outcome != Approved {
		t.Errorf("got %+v, want the first reversal unchanged", second)
	}
}

func TestConcurrentCallsWithOneKeyAuthorizeOnce(t *testing.T) {
	b, tk, _ := newTestBook(t)
	req := request(tk, "k1", "4111111111111111")

	const calls = 50
	codes := make(chan string, calls)
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			auth, err := b.Authorize(req)
			if err != nil {
				t.Error(err)
				return
			}
			codes <- auth.entry.AuthorizationCode
		})
	}
	wg.Wait()
	close(codes)

	seen := map[string]bool{}
	for code := range codes {
		seen[code] = true
	}
	if len(seen) != 1 || len(b.Entries()) != 1 {
		t.Errorf("got %d codes and %d entries, want one of each", len(seen), len(b.Entries()))
	}
}

func TestEntriesAreOldestFirst(t *testing.T) {
	b, tk, c := newTestBook(t)
	for i := range 3 {
		if _, err := b.Authorize(request(tk, fmt.Sprintf("k%d", 3-i), "4111111111111111")); err != nil {
			t.Fatal(err)
		}
		c.advance(time.Second)
	}
	entries := b.Entries()
	if entries[0].Key != "k3" || entries[2].Key != "k1" {
		t.Errorf("got %s, %s, %s, want k3, k2, k1", entries[0].Key, entries[1].Key, entries[2].Key)
	}
}

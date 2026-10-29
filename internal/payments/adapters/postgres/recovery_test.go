package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"go.opentelemetry.io/otel/trace"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/app"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
)

// claim claims the payments due at at, or fails the test.
func claim(t *testing.T, store *postgres.Store, at time.Time) []app.Claim {
	t.Helper()
	claims, err := store.ClaimDue(t.Context(), at, 100)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func failedEvent(p domain.Payment) events.PaymentEvent {
	return events.PaymentEvent{ID: uuid.NewV7().String(), Type: events.PaymentFailed, PaymentID: p.ID, MerchantID: p.MerchantID, OccurredAt: p.UpdatedAt}
}

func TestTheClaimTakesThePaymentsWhoseLeaseExpired(t *testing.T) {
	store, pool := paymentsSetup(t)
	due := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, due)
	approved := newPayment(merchant().ID, "k2", "o2")
	insert(t, store, approved)
	next, a, ev := approve(approved)
	if err := store.RecordAnswer(t.Context(), next, a, ev); err != nil {
		t.Fatal(err)
	}
	later := newPayment(merchant().ID, "k3", "o3")
	later.LeaseUntil = due.LeaseUntil.Add(time.Second)
	insert(t, store, later)

	at := due.LeaseUntil
	claims := claim(t, store, at)
	if len(claims) != 1 || claims[0].Payment.ID != due.ID {
		t.Fatalf("got %+v, want only the payment whose lease expired", claims)
	}
	got := claims[0]
	if got.Payment.Epoch != 2 || !got.Payment.LeaseUntil.Equal(at.Add(domain.Lease)) || !got.Reached {
		t.Errorf("got %+v, want epoch 2, a new lease, and its attempt in doubt", got)
	}
	stored, _ := store.Payment(t.Context(), due.MerchantID, due.ID)
	if stored != got.Payment {
		t.Errorf("stored %+v, want the claimed payment %+v", stored, got.Payment)
	}
	if again := claim(t, store, at); len(again) != 0 {
		t.Errorf("a second claim got %+v, want nothing: the lease moved", again)
	}
	if n := count(t, pool, `SELECT count(*) FROM payment_history WHERE payment_id = $1`, due.ID); n != 2 {
		t.Errorf("the claim wrote history: %d rows, want 2", n)
	}
}

func TestAPaymentReachedTheAcquirerUnlessEveryAttemptWasRefused(t *testing.T) {
	store, _ := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	half := func() float64 { return 0.5 }

	refused := domain.Answer{Outcome: domain.OutcomeNotSent, ResponseCode: "connection_refused"}
	p = p.WithAnswer(refused, p.CreatedAt, half)
	if err := store.RecordAnswer(t.Context(), p, refused, nil); err != nil {
		t.Fatal(err)
	}
	claims := claim(t, store, p.LeaseUntil)
	if len(claims) != 1 || claims[0].Reached {
		t.Fatalf("after a refused attempt: got %+v, want a payment that never reached the acquirer", claims)
	}

	p = claims[0].Payment.NextAttempt()
	if err := store.StartAttempt(t.Context(), p, p.LeaseUntil.Add(-domain.Lease)); err != nil {
		t.Fatal(err)
	}
	// The worker stops before the answer: attempt 2 may have been sent.
	if claims = claim(t, store, p.LeaseUntil); len(claims) != 1 || !claims[0].Reached || claims[0].Payment.Attempts != 2 {
		t.Errorf("after an attempt without an answer: got %+v, want 2 attempts that may have reached it", claims)
	}
}

func TestConcurrentClaimsNeverTakeTheSamePayment(t *testing.T) {
	store, _ := paymentsSetup(t)
	const payments = 20
	var at time.Time
	for i := range payments {
		p := newPayment(merchant().ID, fmt.Sprintf("k%d", i), fmt.Sprintf("o%d", i))
		insert(t, store, p)
		at = p.LeaseUntil
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed = map[string]int{}
	)
	for range 4 {
		wg.Go(func() {
			claims, err := store.ClaimDue(t.Context(), at, 10)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, c := range claims {
				claimed[c.Payment.ID]++
			}
		})
	}
	wg.Wait()
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("payment %s was claimed %d times", id, n)
		}
	}
	if len(claimed) != payments {
		t.Errorf("%d payments were claimed, want %d", len(claimed), payments)
	}
}

func TestARecoveryWriteWithAnOldEpochChangesNothing(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	c := claim(t, store, p.LeaseUntil)[0]
	claim(t, store, c.Payment.LeaseUntil) // another worker takes it over

	stale := c.Payment.NextAttempt()
	if err := store.StartAttempt(t.Context(), stale, c.Payment.LeaseUntil); !errors.Is(err, app.ErrStaleEpoch) {
		t.Errorf("start an attempt: got %v, want ErrStaleEpoch", err)
	}
	if err := store.StartReversal(t.Context(), c.Payment.WithReversalStarted(c.Payment.LeaseUntil), c.Payment.LeaseUntil); !errors.Is(err, app.ErrStaleEpoch) {
		t.Errorf("start a reversal: got %v, want ErrStaleEpoch", err)
	}
	failed := c.Payment.Unavailable(c.Payment.LeaseUntil)
	if err := store.Finish(t.Context(), failed, failedEvent(failed)); !errors.Is(err, app.ErrStaleEpoch) {
		t.Errorf("finish: got %v, want ErrStaleEpoch", err)
	}
	if n := count(t, pool, `SELECT count(*) FROM payment_history WHERE payment_id = $1`, p.ID); n != 2 {
		t.Errorf("stale writers left %d history rows, want the 2 of T1", n)
	}
}

func TestAReversalIsRecordedBeforeItIsSent(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	c := claim(t, store, p.LeaseUntil)[0]
	started := p.LeaseUntil
	reversing := c.Payment.WithReversalStarted(started)
	if err := store.StartReversal(t.Context(), reversing, started); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Payment(t.Context(), p.MerchantID, p.ID)
	if !stored.ReversalStartedAt.Equal(started) || stored.Status != domain.PaymentProcessing {
		t.Fatalf("got %+v, want PROCESSING with the reversal started at %s", stored, started)
	}

	// The first reversal is not confirmed, and the payment waits.
	unclear := domain.Answer{Outcome: domain.OutcomeUncertain, ResponseCode: "503", Latency: 40 * time.Millisecond}
	waiting := reversing.WithReversal(unclear, started.Add(time.Second))
	if err := store.RecordReversal(t.Context(), waiting, unclear, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox`); n != 0 {
		t.Errorf("an unconfirmed reversal wrote %d events", n)
	}

	// The next worker sends it again, and the acquirer confirms it.
	c = claim(t, store, waiting.LeaseUntil)[0]
	again := c.Payment.WithReversalStarted(waiting.LeaseUntil)
	if err := store.StartReversal(t.Context(), again, waiting.LeaseUntil); err != nil {
		t.Fatal(err)
	}
	confirmed := domain.Answer{Outcome: domain.OutcomeReversed, ResponseCode: "200", Latency: 30 * time.Millisecond}
	failed := again.WithReversal(confirmed, waiting.LeaseUntil.Add(time.Second))
	ev := failedEvent(failed)
	if err := store.RecordReversal(t.Context(), failed, confirmed, &ev); err != nil {
		t.Fatal(err)
	}

	stored, _ = store.Payment(t.Context(), p.MerchantID, p.ID)
	if stored != failed || !stored.ReversalStartedAt.Equal(started) {
		t.Errorf("read back %+v, want %+v, with the reversal started the first time", stored, failed)
	}
	history, _ := store.History(t.Context(), p.ID)
	if len(history) != 5 ||
		history[2].Kind != domain.HistoryReversal || history[2].Outcome != "uncertain" || history[2].ResponseCode != "503" ||
		history[3].Kind != domain.HistoryReversal || history[3].Outcome != "reversed" || history[3].LatencyMS != 30 ||
		history[4].Status != domain.PaymentFailed || history[4].Reason != "acquirer_unavailable" {
		t.Errorf("history: got %+v, want both reversals and the final state", history)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE headers ->> 'event-type' = 'payment.failed'`); n != 1 {
		t.Errorf("got %d payment.failed events, want 1", n)
	}
}

func TestAPaymentThatNeverReachedTheAcquirerFinishesWithoutACall(t *testing.T) {
	store, pool := paymentsSetup(t)
	p := newPayment(merchant().ID, "k1", "o1")
	insert(t, store, p)
	c := claim(t, store, p.LeaseUntil)[0]
	failed := c.Payment.Unavailable(p.LeaseUntil)
	if err := store.Finish(t.Context(), failed, failedEvent(failed)); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Payment(t.Context(), p.MerchantID, p.ID)
	if stored != failed {
		t.Errorf("read back %+v, want %+v", stored, failed)
	}
	if n := count(t, pool, `SELECT count(*) FROM outbox WHERE headers ->> 'event-type' = 'payment.failed'`); n != 1 {
		t.Errorf("got %d payment.failed events, want 1", n)
	}
}

func TestUnresolvedReportsTheOldestPaymentInProgress(t *testing.T) {
	store, _ := paymentsSetup(t)
	oldest := newPayment(merchant().ID, "k1", "o1")
	oldest.CreatedAt, oldest.UpdatedAt = oldest.CreatedAt.Add(-10*time.Minute), oldest.CreatedAt.Add(-10*time.Minute)
	insert(t, store, oldest)
	insert(t, store, newPayment(merchant().ID, "k2", "o2"))
	done := newPayment(merchant().ID, "k3", "o3")
	insert(t, store, done)
	next, a, ev := approve(done)
	if err := store.RecordAnswer(t.Context(), next, a, ev); err != nil {
		t.Fatal(err)
	}

	u, err := store.Unresolved(t.Context(), time.Now().Add(time.Minute))
	if err != nil || u.Count != 2 || u.OldestID != oldest.ID || !u.OldestCreatedAt.Equal(oldest.CreatedAt) {
		t.Errorf("got %+v, %v, want the 2 payments in progress, the oldest first", u, err)
	}
	if u, err := store.Unresolved(t.Context(), oldest.CreatedAt); err != nil || u != (app.Unresolved{}) {
		t.Errorf("before the oldest: got %+v, %v, want nothing", u, err)
	}
}

func TestTheClaimCarriesTheTraceOfThePaymentsRequest(t *testing.T) {
	store, _ := paymentsSetup(t)
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	request := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	traced := newPayment(merchant().ID, "k1", "o1")
	if _, _, err := store.InsertPayment(request, traced, admitAll); err != nil {
		t.Fatal(err)
	}
	untraced := newPayment(merchant().ID, "k2", "o2")
	insert(t, store, untraced)

	traces := map[string]string{}
	for _, c := range claim(t, store, untraced.LeaseUntil) {
		traces[c.Payment.ID] = c.Trace
	}
	if got := traces[traced.ID]; got != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Errorf("the traced payment was claimed with trace %q, want its request's", got)
	}
	if got, ok := traces[untraced.ID]; !ok || got != "" {
		t.Errorf("the payment without a trace was claimed with %q, %v", got, ok)
	}
}

package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/payments/domain"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/migrate"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/outbox"
	"github.com/OscarNunezU/payment-processing-service/internal/platform/pgtest"
	platformpg "github.com/OscarNunezU/payment-processing-service/internal/platform/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/dispatcher"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/kafka"
	webhookspg "github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/postgres"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/sender"
	webhooksapp "github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	webhooks "github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
	"github.com/OscarNunezU/payment-processing-service/migrations"
)

const webhookSecret = "whsec_scenario_tienda_uno_0123"

// received is a webhook as the merchant's endpoint got it.
type received struct {
	at        time.Time
	id        string
	signature string
	trace     string
	body      []byte
}

// receiver is a merchant's endpoint: it answers 500 to the first failures
// webhooks, and 200 to the rest.
type receiver struct {
	url      string
	mu       sync.Mutex
	failures int
	got      []received
}

func newReceiver(t *testing.T, failures int) *receiver {
	t.Helper()
	r := &receiver{failures: failures}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got = append(r.got, received{at: time.Now(), id: req.Header.Get("Webhook-Id"), signature: req.Header.Get("Webhook-Signature"),
			trace: req.Header.Get("traceparent"), body: body})
		if len(r.got) <= r.failures {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL
	return r
}

// wait returns the first n webhooks received, once they arrived.
func (r *receiver) wait(t *testing.T, n int, within time.Duration) []received {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		r.mu.Lock()
		got := append([]received(nil), r.got...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d webhooks after %s, want %d", len(got), within, n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// runWebhooks runs what follows a payment's final state, as payments-worker
// and the webhooks service do: the relay to an in-memory Kafka, the consumer
// on a webhooks database of its own, and the dispatcher, which posts to url.
func (s *service) runWebhooks(t *testing.T, url string) {
	t.Helper()
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(4, events.PaymentsTopic), kfake.SeedTopics(1, kafka.DeadLetterTopic))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	brokers := cluster.ListenAddrs()

	dbURL := pgtest.Start(t)
	sqlDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	fsys, err := migrations.For("webhooks")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(t.Context(), sqlDB, fsys); err != nil {
		t.Fatal(err)
	}
	db, err := platformpg.Open(t.Context(), dbURL, "scenarios", 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store := webhookspg.New(db)
	if err := store.CreateEndpoint(t.Context(), webhooks.Endpoint{MerchantID: tiendaUno, URL: url, Secret: webhookSecret}, time.Now()); err != nil {
		t.Fatal(err)
	}

	producer, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := kgo.NewClient(append(kafka.Options(events.PaymentsTopic), kgo.SeedBrokers(brokers...))...)
	if err != nil {
		t.Fatal(err)
	}
	svc := webhooksapp.NewService(webhooksapp.Deps{
		Store: store, Sender: sender.New(webhooks.Timeout),
		NewID: func() string { return uuid.NewV7().String() }, Now: time.Now, Random: noJitter, Log: quiet,
	})
	loop := dispatcher.New(svc, quiet)
	loop.Interval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { outbox.NewRelay(s.db, producer, quiet).Run(ctx) })
	wg.Go(func() { kafka.NewConsumer(consumer, svc, quiet).Run(ctx) })
	wg.Go(func() { loop.Run(ctx) })
	// Registered after the databases' cleanups, so it runs before them.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		consumer.Close()
		producer.Close()
	})
}

// checkWebhook checks that w is the event of payment id, signed with the
// merchant's secret when it arrived, and returns the event.
func checkWebhook(t *testing.T, w received, paymentID string) events.PaymentEvent {
	t.Helper()
	var e events.PaymentEvent
	if err := json.Unmarshal(w.body, &e); err != nil {
		t.Fatalf("a webhook that is not an event: %s", w.body)
	}
	if e.PaymentID != paymentID || e.ID != w.id || e.MerchantID != tiendaUno {
		t.Errorf("got event %+v with Webhook-Id %s, want the event of payment %s, with its id", e, w.id, paymentID)
	}
	if err := webhooks.Verify(webhookSecret, w.signature, w.body, w.at); err != nil {
		t.Errorf("the signature does not verify: %v", err)
	}
	return e
}

// INV06: every final state produces one webhook event.
func TestEveryFinalStateIsReportedByWebhook(t *testing.T) {
	s := start(t)
	r := newReceiver(t, 0)
	s.runWebhooks(t, r.url)
	want := map[string]events.Type{}
	for card, typ := range map[string]events.Type{approves: events.PaymentApproved, declines: events.PaymentDeclined, rejectsTheRequest: events.PaymentFailed} {
		a := s.pay(t, keyUno, "k-"+card, "o-"+card, s.tokenize(t, card), 15000)
		if a.httpStatus != http.StatusCreated {
			t.Fatalf("got %+v, want a final payment", a)
		}
		want[a.ID] = typ
	}

	got := r.wait(t, 3, 10*time.Second)
	time.Sleep(300 * time.Millisecond)
	if again := r.wait(t, 3, 0); len(again) != 3 {
		t.Errorf("got %d webhooks, want one per payment", len(again))
	}
	for _, w := range got {
		var e events.PaymentEvent
		_ = json.Unmarshal(w.body, &e)
		if checkWebhook(t, w, e.PaymentID); want[e.PaymentID] != e.Type {
			t.Errorf("payment %s was reported as %s, want %s", e.PaymentID, e.Type, want[e.PaymentID])
		}
		delete(want, e.PaymentID)
	}
	if len(want) != 0 {
		t.Errorf("no webhook for %v", want)
	}
}

// Feature: Webhooks. Scenario: The merchant's endpoint is down.
func TestTheMerchantsEndpointIsDown(t *testing.T) {
	s := start(t)
	r := newReceiver(t, 2)
	s.runWebhooks(t, r.url)
	a := s.pay(t, keyUno, "k1", "o1", s.tokenize(t, approves), 15000)
	if a.Status != domain.PaymentApproved {
		t.Fatalf("got %+v, want APPROVED", a)
	}

	// The waits after the two failures are 5 and 10 s, without jitter.
	got := r.wait(t, 3, 30*time.Second)
	var ids []string
	for _, w := range got {
		ids = append(ids, checkWebhook(t, w, a.ID).ID)
	}
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Errorf("the attempts carried the events %v, want the same event id each time", ids)
	}
	first, second := got[1].at.Sub(got[0].at), got[2].at.Sub(got[1].at)
	if first < 4*time.Second || second <= first {
		t.Errorf("the waits were %s and %s, want them to increase from about 5 s", first, second)
	}
}

// NFR07: a payment can be followed in a single trace from its request to its
// webhook, whether the request finished it or the recovery did.
func TestAPaymentIsOneTraceFromItsRequestToItsWebhook(t *testing.T) {
	s := start(t)
	r := newReceiver(t, 0)
	s.runWebhooks(t, r.url)

	traces := map[string]string{} // payment id → the trace of its request
	for i, card := range []string{approves, unavailableOnce} {
		traceID := fmt.Sprintf("4bf92f3577b34da6a3ce929d0e0e473%d", i)
		var a answer
		status, _ := s.post(t, s.url+"/v1/payments", keyUno,
			map[string]string{"Idempotency-Key": "k-" + card, "traceparent": "00-" + traceID + "-00f067aa0ba902b7-01"},
			map[string]any{"order_id": "o-" + card, "amount": 15000, "currency": "CLP", "card_token": s.tokenize(t, card)}, &a)
		if status != http.StatusCreated && status != http.StatusAccepted {
			t.Fatalf("got %d %+v", status, a)
		}
		traces[a.ID] = traceID
	}

	for _, w := range r.wait(t, 2, 15*time.Second) {
		var e events.PaymentEvent
		_ = json.Unmarshal(w.body, &e)
		if want := traces[e.PaymentID]; want == "" || !strings.Contains(w.trace, want) {
			t.Errorf("the webhook of payment %s came in trace %q, want the trace of its request %s", e.PaymentID, w.trace, want)
		}
	}
}

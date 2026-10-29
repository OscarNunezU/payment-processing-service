package kafka_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/OscarNunezU/payment-processing-service/internal/events"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/adapters/kafka"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app/apptest"
	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/domain"
)

const merchantID = "00000000-0000-4000-8000-000000000001"

var quiet = slog.New(slog.DiscardHandler)

// cluster starts an in-memory Kafka with the payments topic and the
// dead-letter topic, and returns its address.
func cluster(t *testing.T) []string {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, events.PaymentsTopic, kafka.DeadLetterTopic))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c.ListenAddrs()
}

func client(t *testing.T, brokers []string, opts ...kgo.Opt) *kgo.Client {
	t.Helper()
	c, err := kgo.NewClient(append([]kgo.Opt{kgo.SeedBrokers(brokers...)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func publish(t *testing.T, brokers []string, values ...string) {
	t.Helper()
	var records []*kgo.Record
	for _, v := range values {
		records = append(records, &kgo.Record{Topic: events.PaymentsTopic, Key: []byte("p1"), Value: []byte(v),
			Headers: []kgo.RecordHeader{{Key: "traceparent", Value: []byte("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")}}})
	}
	if err := client(t, brokers).ProduceSync(t.Context(), records...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

func event(id string) string {
	return fmt.Sprintf(`{"id":%q,"type":"payment.approved","payment_id":"p1","merchant_id":%q}`, id, merchantID)
}

// consume runs a consumer of the group on uc until stop is called. Stopping
// it closes its client, which leaves the group, as the service does when it
// shuts down.
func consume(t *testing.T, brokers []string, uc kafka.Ingester) (stop func()) {
	t.Helper()
	kc, err := kgo.NewClient(append(kafka.Options(events.PaymentsTopic), kgo.SeedBrokers(brokers...))...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := kafka.NewConsumer(kc, uc, quiet)
	c.Retry = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
			kc.Close()
		})
	}
	t.Cleanup(stop)
	return stop
}

func service(store *apptest.Store) *app.Service {
	return app.NewService(app.Deps{
		Store: store, Sender: &apptest.Sender{}, NewID: func() string { return uuid.NewV7().String() },
		Now: time.Now, Random: func() float64 { return 0 }, Log: quiet,
	})
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEachEventBecomesOneDeliveryAndBadOnesGoToTheDeadLetterTopic(t *testing.T) {
	brokers := cluster(t)
	store := apptest.NewStore(domain.Endpoint{MerchantID: merchantID, URL: "http://web/webhooks", Secret: "whsec_test_0123456789abcdefgh"})
	e1, e2 := uuid.NewV7().String(), uuid.NewV7().String()
	publish(t, brokers, event(e1), `<html>`, event(e1), event(e2))
	stop := consume(t, brokers, service(store))

	eventually(t, "two deliveries", func() bool { return len(store.Deliveries()) == 2 })
	if d := store.Deliveries()[0]; d.EventID != e1 || d.Traceparent == "" {
		t.Errorf("got %+v, want the first event with its trace", d)
	}

	dlq := client(t, brokers, kgo.ConsumeTopics(kafka.DeadLetterTopic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	records := dlq.PollFetches(ctx).Records()
	if len(records) != 1 || string(records[0].Value) != `<html>` {
		t.Fatalf("the dead-letter topic has %d records, want the one that cannot be decoded", len(records))
	}
	reason := ""
	for _, h := range records[0].Headers {
		if h.Key == "dlq-reason" {
			reason = string(h.Value)
		}
	}
	if reason == "" {
		t.Error("the dead letter does not say why it is there")
	}

	// The offsets were committed: the group's next consumer reads nothing
	// old.
	stop()
	again := &counting{}
	consume(t, brokers, again)
	publish(t, brokers, event(uuid.NewV7().String()))
	eventually(t, "the new event", func() bool { return again.count() == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := again.count(); n != 1 {
		t.Errorf("the next consumer got %d events, want only the new one", n)
	}
}

// counting counts the events it is given, and fails the first fail of them.
type counting struct {
	mu    sync.Mutex
	n     int
	fail  int
	calls int
}

func (c *counting) Ingest(context.Context, app.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls <= c.fail {
		return errors.New("connection refused")
	}
	c.n++
	return nil
}

func (c *counting) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestAnEventIsRetriedWhileTheDatabaseFails(t *testing.T) {
	brokers := cluster(t)
	publish(t, brokers, event(uuid.NewV7().String()))
	uc := &counting{fail: 3}
	consume(t, brokers, uc)
	eventually(t, "the event, after the failures", func() bool { return uc.count() == 1 })
	uc.mu.Lock()
	defer uc.mu.Unlock()
	if uc.calls != 4 {
		t.Errorf("the event was tried %d times, want 4: 3 failures and the success", uc.calls)
	}
}

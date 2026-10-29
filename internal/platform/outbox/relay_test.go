package outbox_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/OscarNunezU/payment-processing-service/internal/platform/outbox"
)

const topic = "payments.events.v1"

// kafka starts an in-memory Kafka with the topic, and returns its address.
func kafka(t *testing.T) []string {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, topic))
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

func write(t *testing.T, pool *pgxpool.Pool, keys ...string) {
	t.Helper()
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		for i, key := range keys {
			if err := outbox.Write(t.Context(), tx, outbox.Message{
				Topic: topic, Key: key, Payload: fmt.Appendf(nil, `{"n":%d}`, i),
				Headers: map[string]string{"event-type": "payment.approved"},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// consume reads the topic from the start until at least n records arrived
// and nothing more comes for half a second, so a duplicate is counted too.
func consume(t *testing.T, brokers []string, n int) []*kgo.Record {
	t.Helper()
	c := client(t, brokers, kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	deadline := time.Now().Add(5 * time.Second)
	var out []*kgo.Record
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		records := c.PollFetches(ctx).Records()
		cancel()
		out = append(out, records...)
		switch {
		case len(records) == 0 && len(out) >= n:
			return out
		case time.Now().After(deadline):
			t.Fatalf("got %d records, want %d", len(out), n)
		}
	}
}

func waitEmpty(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d rows are still in the outbox", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runRelay(t *testing.T, pool *pgxpool.Pool, brokers []string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	r := outbox.NewRelay(pool, client(t, brokers), slog.New(slog.DiscardHandler))
	r.Retry = 50 * time.Millisecond
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestTheRelayPublishesInOrderAndEmptiesTheOutbox(t *testing.T) {
	pool := setup(t)
	brokers := kafka(t)
	write(t, pool, "pay-1", "pay-2", "pay-3")
	runRelay(t, pool, brokers)

	records := consume(t, brokers, 3)
	for i, r := range records {
		if string(r.Key) != fmt.Sprintf("pay-%d", i+1) || string(r.Value) != fmt.Sprintf(`{"n":%d}`, i) {
			t.Errorf("record %d: key %s value %s, want them in the order they were written", i, r.Key, r.Value)
		}
		if len(r.Headers) != 1 || r.Headers[0].Key != "event-type" || string(r.Headers[0].Value) != "payment.approved" {
			t.Errorf("record %d: headers %v", i, r.Headers)
		}
	}
	waitEmpty(t, pool)

	write(t, pool, "pay-4") // the relay keeps going
	if records := consume(t, brokers, 4); len(records) != 4 || string(records[3].Key) != "pay-4" {
		t.Errorf("got %d records, want the fourth to be pay-4", len(records))
	}
}

func TestOnlyOneRelayPublishes(t *testing.T) {
	pool := setup(t)
	brokers := kafka(t)
	runRelay(t, pool, brokers)
	runRelay(t, pool, brokers)
	keys := make([]string, 50)
	for i := range keys {
		keys[i] = fmt.Sprintf("pay-%d", i)
	}
	write(t, pool, keys...)
	waitEmpty(t, pool)

	records := consume(t, brokers, 50)
	seen := map[string]int{}
	for _, r := range records {
		seen[string(r.Key)]++
	}
	for _, k := range keys {
		if seen[k] != 1 {
			t.Errorf("%s was published %d times, want once", k, seen[k])
		}
	}
}

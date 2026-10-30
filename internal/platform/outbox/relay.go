package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// relayLock is the advisory lock that makes one relay the publisher.
const relayLock int64 = 0x6f7574626f78 // "outbox"

// Producer sends records to Kafka and waits until it acknowledges them.
type Producer interface {
	ProduceSync(ctx context.Context, rs ...*kgo.Record) kgo.ProduceResults
}

// Relay publishes the outbox to Kafka in order, and deletes the rows Kafka
// acknowledged (RFC 0001, section 8.2). Several relays may run, one per
// instance, but only the one that holds the advisory lock publishes; the
// others wait to take over.
type Relay struct {
	db    *pgxpool.Pool
	kafka Producer
	log   *slog.Logger
	// Batch is how many rows a round reads; Idle is the wait after a round
	// that did not fill its batch, and Retry the wait after losing the lock
	// or failing.
	Batch int
	Idle  time.Duration
	Retry time.Duration
}

// NewRelay returns a relay from db to kafka.
func NewRelay(db *pgxpool.Pool, kafka Producer, log *slog.Logger) *Relay {
	return &Relay{db: db, kafka: kafka, log: log, Batch: 500, Idle: 20 * time.Millisecond, Retry: time.Second}
}

// Run publishes until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	for {
		if err := r.lead(ctx); err != nil && ctx.Err() == nil {
			r.log.WarnContext(ctx, "the relay stopped publishing; it will try again", "error", err)
		}
		if !sleep(ctx, r.Retry) {
			return
		}
	}
}

// lead publishes for as long as this relay holds the lock. The lock belongs
// to one connection, and every read and delete goes through it: if that
// connection dies, the lock is released and the next query fails, so two
// relays never publish at once for long.
func (r *Relay) lead(ctx context.Context) error {
	conn, err := r.db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("get a connection: %w", err)
	}
	defer conn.Release()
	var leader bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, relayLock).Scan(&leader); err != nil {
		return fmt.Errorf("try the relay lock: %w", err)
	}
	if !leader {
		return nil
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, relayLock)
	}()
	r.log.InfoContext(ctx, "this relay publishes the outbox")

	for {
		n, err := r.publish(ctx, conn)
		if err != nil {
			return err
		}
		// A full batch means more are waiting, so the next one goes at once.
		// Otherwise the relay waits, and the events of the next moment travel
		// together, instead of one request to Kafka and one query each.
		if n < r.Batch && !sleep(ctx, r.Idle) {
			return nil
		}
	}
}

// publish sends one batch, oldest first, and deletes it once Kafka has it.
// A crash between the two publishes the batch again, which consumers
// tolerate.
func (r *Relay) publish(ctx context.Context, conn *pgxpool.Conn) (int, error) {
	rows, err := conn.Query(ctx, `SELECT id, topic, key, payload, headers FROM outbox ORDER BY id LIMIT $1`, r.Batch)
	if err != nil {
		return 0, fmt.Errorf("read the outbox: %w", err)
	}
	var ids []int64
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*kgo.Record, error) {
		var (
			id         int64
			topic, key string
			payload    []byte
			headers    map[string]string
		)
		if err := row.Scan(&id, &topic, &key, &payload, &headers); err != nil {
			return nil, err
		}
		ids = append(ids, id)
		rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: payload}
		for _, k := range slices.Sorted(maps.Keys(headers)) {
			rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(headers[k])})
		}
		return rec, nil
	})
	if err != nil {
		return 0, fmt.Errorf("read the outbox: %w", err)
	}
	if len(records) == 0 {
		return 0, nil
	}
	if err := r.kafka.ProduceSync(ctx, records...).FirstErr(); err != nil {
		return 0, fmt.Errorf("publish %d events: %w", len(records), err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM outbox WHERE id = ANY($1)`, ids); err != nil {
		return 0, fmt.Errorf("delete %d published events: %w", len(ids), err)
	}
	published.Add(ctx, int64(len(records)))
	return len(records), nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

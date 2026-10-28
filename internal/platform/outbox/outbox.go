// Package outbox writes events to the outbox table, in the same transaction as
// the change they describe. The relay publishes them to Kafka afterwards, so an
// event is never lost and never published for a change that rolled back
// (RFC 0001, section 8.3).
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Message is an event waiting to be published.
type Message struct {
	Topic string
	// Key is the Kafka key: events with the same key keep their order.
	Key     string
	Payload []byte
	Headers map[string]string
}

// Write inserts m within tx, which must be the transaction of the change m
// describes. It adds the trace context of ctx to the headers, so the trace
// continues on the consumer's side.
func Write(ctx context.Context, tx pgx.Tx, m Message) error {
	if m.Topic == "" || m.Key == "" {
		return errors.New("an outbox message needs a topic and a key")
	}
	headers := make(map[string]string, len(m.Headers)+2)
	maps.Copy(headers, m.Headers)
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	encoded, err := json.Marshal(headers)
	if err != nil {
		return fmt.Errorf("encode the headers of a %s message: %w", m.Topic, err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO outbox (topic, key, payload, headers) VALUES ($1, $2, $3, $4)`,
		m.Topic, m.Key, m.Payload, encoded,
	); err != nil {
		return fmt.Errorf("write a %s message to the outbox: %w", m.Topic, err)
	}
	return nil
}

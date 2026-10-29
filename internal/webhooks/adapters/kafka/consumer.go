// Package kafka is the consumer of the webhooks service: it reads the payment
// events and turns each one into a delivery (RFC 0001, section 8.3).
package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/OscarNunezU/payment-processing-service/internal/webhooks/app"
)

// DeadLetterTopic receives the events that cannot be decoded, to be looked
// at by hand.
const DeadLetterTopic = "payments.dlq.v1"

// Group is the consumer group of the webhooks service.
const Group = "webhooks"

// Client is the part of a franz-go client the consumer uses. The client must
// consume with DisableAutoCommit and BlockRebalanceOnPoll, as Options sets.
type Client interface {
	PollFetches(ctx context.Context) kgo.Fetches
	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
	AllowRebalance()
	ProduceSync(ctx context.Context, rs ...*kgo.Record) kgo.ProduceResults
}

// Options are what the consumer needs from its client: the group, the topic
// from its start, and commits that the consumer makes itself.
func Options(topic string) []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumerGroup(Group),
		kgo.ConsumeTopics(topic),
		// A new group reads what the topic retains, so no event is skipped.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		// The partitions stay with this consumer until it commits what it
		// polled, so a rebalance does not hand them over halfway.
		kgo.BlockRebalanceOnPoll(),
	}
}

// Ingester is the use case the consumer feeds.
type Ingester interface {
	Ingest(ctx context.Context, m app.Message) error
}

// Consumer reads the payment events at least once.
type Consumer struct {
	client Client
	uc     Ingester
	log    *slog.Logger
	// Retry is the wait before an event is tried again, after an error that
	// is not the event's fault, such as the database being down.
	Retry time.Duration
}

// NewConsumer returns a consumer that feeds uc from client.
func NewConsumer(client Client, uc Ingester, log *slog.Logger) *Consumer {
	return &Consumer{client: client, uc: uc, log: log, Retry: time.Second}
}

// Run consumes until ctx ends. Offsets are committed only after every record
// of a poll was stored, so a crash replays them, and the unique event id
// makes the replay harmless.
func (c *Consumer) Run(ctx context.Context) {
	// A consumer that stops between a poll and its commit must not keep the
	// group from rebalancing: the uncommitted records go to the next one.
	defer c.client.AllowRebalance()
	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			c.log.WarnContext(ctx, "could not fetch payment events", "topic", topic, "partition", partition, "error", err)
		})
		records := fetches.Records()
		for _, r := range records {
			if !c.handle(ctx, r) {
				return
			}
		}
		if len(records) > 0 {
			if err := c.client.CommitRecords(ctx, records...); err != nil && ctx.Err() == nil {
				c.log.WarnContext(ctx, "could not commit the offsets; the events will be read again", "error", err)
			}
		}
		c.client.AllowRebalance()
	}
}

// handle stores the delivery of r. An event that cannot be decoded goes to
// the dead-letter topic. Any other error is retried in place, so the events
// of a partition keep their order. It returns false when ctx ended first.
func (c *Consumer) handle(ctx context.Context, r *kgo.Record) bool {
	for {
		err := c.uc.Ingest(ctx, message(r))
		if errors.Is(err, app.ErrUndecodable) {
			err = c.deadLetter(ctx, r, err)
		}
		if err == nil {
			return true
		}
		c.log.WarnContext(ctx, "could not process a payment event; it will be tried again",
			"partition", r.Partition, "offset", r.Offset, "error", err)
		if !sleep(ctx, c.Retry) {
			return false
		}
	}
}

func (c *Consumer) deadLetter(ctx context.Context, r *kgo.Record, reason error) error {
	c.log.ErrorContext(ctx, "a payment event cannot be decoded; it goes to the dead-letter topic",
		"partition", r.Partition, "offset", r.Offset, "error", reason)
	headers := append(append([]kgo.RecordHeader(nil), r.Headers...),
		kgo.RecordHeader{Key: "dlq-reason", Value: []byte(reason.Error())},
		kgo.RecordHeader{Key: "dlq-source", Value: []byte(r.Topic)})
	return c.client.ProduceSync(ctx, &kgo.Record{Topic: DeadLetterTopic, Key: r.Key, Value: r.Value, Headers: headers}).FirstErr()
}

func message(r *kgo.Record) app.Message {
	m := app.Message{Payload: r.Value}
	for _, h := range r.Headers {
		if h.Key == "traceparent" {
			m.Traceparent = string(h.Value)
		}
	}
	return m
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

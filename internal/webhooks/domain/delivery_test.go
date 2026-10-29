package domain

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 29, 13, 0, 0, 0, time.UTC)

func half() float64 { return 0.5 }

func delivery() Delivery {
	d := NewDelivery("d1", "e1", "payment.approved", "m1", []byte(`{"id":"e1"}`), "", now)
	d.Attempts = 1
	return d
}

func TestA2xxDeliversTheWebhook(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		d := delivery().After(Attempt{StatusCode: status}, now.Add(time.Second), half)
		if d.Status != Delivered || !d.DeliveredAt.Equal(now.Add(time.Second)) || !d.NextAttemptAt.IsZero() {
			t.Errorf("%d: got %+v, want delivered", status, d)
		}
	}
}

func TestAnythingElseIsRetried(t *testing.T) {
	for _, a := range []Attempt{{StatusCode: 500}, {StatusCode: 410}, {StatusCode: 302}, {Error: "timeout"}} {
		d := delivery().After(a, now, half)
		if d.Status != Pending || !d.NextAttemptAt.Equal(now.Add(7500*time.Millisecond)) || d.LastStatus != a.StatusCode || d.LastError != a.Error {
			t.Errorf("%+v: got %+v, want pending for 7.5 s, with the attempt recorded", a, d)
		}
	}
}

func TestBackoffDoublesUpToAnHour(t *testing.T) {
	tests := []struct {
		attempt  int
		min, max time.Duration
	}{
		{1, 5 * time.Second, 10 * time.Second},
		{2, 10 * time.Second, 20 * time.Second},
		{9, 21*time.Minute + 20*time.Second, 42*time.Minute + 40*time.Second},
		{10, 30 * time.Minute, time.Hour},
		{40, 30 * time.Minute, time.Hour},
	}
	for _, tt := range tests {
		lo, hi := Backoff(tt.attempt, func() float64 { return 0 }), Backoff(tt.attempt, func() float64 { return 0.999999 })
		if lo != tt.min || hi > tt.max || hi < tt.max-time.Second {
			t.Errorf("attempt %d waits %s to %s, want %s to %s", tt.attempt, lo, hi, tt.min, tt.max)
		}
	}
}

func TestADeliveryFailsAfter24Hours(t *testing.T) {
	d := delivery()
	d.Attempts = 30
	if got := d.After(Attempt{StatusCode: 503}, now.Add(23*time.Hour), half); got.Status != Pending {
		t.Errorf("after 23 h: got %s, want another attempt within the 24 h", got.Status)
	}
	if got := d.After(Attempt{StatusCode: 503}, now.Add(23*time.Hour+40*time.Minute), half); got.Status != Failed || !got.NextAttemptAt.IsZero() {
		t.Errorf("after 23 h 40 min: got %+v, want failed: the next attempt would be past 24 h", got)
	}
}

func TestASignatureVerifiesOnlyItsBodyAndSecret(t *testing.T) {
	body := []byte(`{"id":"e1","type":"payment.approved"}`)
	header := Sign("whsec_test", body, now)
	if err := Verify("whsec_test", header, body, now.Add(time.Minute)); err != nil {
		t.Fatalf("the signature of the body did not verify: %v", err)
	}
	for name, check := range map[string]error{
		"another body":       Verify("whsec_test", header, []byte(`{"id":"e2"}`), now),
		"another secret":     Verify("whsec_other", header, body, now),
		"a malformed header": Verify("whsec_test", "v1=abc", body, now),
		// The signature of now, with the timestamp of an hour later.
		"a changed timestamp": Verify("whsec_test",
			"t="+strconv.FormatInt(now.Add(time.Hour).Unix(), 10)+header[strings.Index(header, ","):], body, now.Add(time.Hour)),
	} {
		if !errors.Is(check, ErrBadSignature) {
			t.Errorf("%s: got %v, want ErrBadSignature", name, check)
		}
	}
	if err := Verify("whsec_test", header, body, now.Add(SignatureTolerance+time.Second)); !errors.Is(err, ErrStaleSignature) {
		t.Errorf("an old signature: got %v, want ErrStaleSignature", err)
	}
}

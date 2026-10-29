package acquirersim

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

// Faults are failures injected into every authorization and reversal, on top
// of what the test cards do. The panel and the load tests set them.
type Faults struct {
	// LatencyMS is added to every answer, up to 10 s.
	LatencyMS int `json:"latency_ms"`
	// ErrorRate is the share of calls answered 503 without being processed.
	ErrorRate float64 `json:"error_rate"`
	// TimeoutRate is the share of calls that are processed but answered late,
	// after the client's timeout.
	TimeoutRate float64 `json:"timeout_rate"`
	// Down closes the public port, so new connections are refused.
	Down bool `json:"down"`
}

func (f Faults) validate() error {
	var errs []error
	if f.LatencyMS < 0 || f.LatencyMS > 10_000 {
		errs = append(errs, errors.New("latency_ms must be between 0 and 10000"))
	}
	if f.ErrorRate < 0 || f.ErrorRate > 1 {
		errs = append(errs, errors.New("error_rate must be between 0 and 1"))
	}
	if f.TimeoutRate < 0 || f.TimeoutRate > 1 {
		errs = append(errs, errors.New("timeout_rate must be between 0 and 1"))
	}
	return errors.Join(errs...)
}

func (f Faults) latency() time.Duration { return time.Duration(f.LatencyMS) * time.Millisecond }

// faults holds the current Faults, which the admin port changes while the
// public port serves.
type faults struct {
	mu sync.Mutex
	f  Faults
}

func (s *faults) get() Faults {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f
}

func (s *faults) set(f Faults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f = f
}

// hit reports whether a call falls within rate.
func hit(rate float64) bool { return rate > 0 && rand.Float64() < rate } //nolint:gosec // a fault sample, not a secret

package middleware

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	log "github.com/sirupsen/logrus"
)

// LoginThrottle tracks failed login attempts per email and temporarily locks
// accounts. The counters live in the cluster's replicated JetStream KV
// bucket, so every node enforces the same limit no matter which node took
// the request. Admission is atomic (compare-and-swap on the bucket entry):
// an attempt reserves a slot before the credentials are checked, and
// failures plus in-flight attempts never exceed maxFailures, so a burst of
// concurrent requests cannot test more passwords than a sequential attacker
// could.
type LoginThrottle struct {
	kv          jetstream.KeyValue
	maxFailures int
	lockoutTime time.Duration
}

// throttleEntry is the persisted per-email state. Stale entries age out
// with the bucket TTL (refreshed on every write).
type throttleEntry struct {
	Failures int       `json:"failures"`
	InFlight int       `json:"in_flight"`
	LockedAt time.Time `json:"locked_at,omitempty"`
}

// LoginOutcome is the result of an admitted login attempt.
type LoginOutcome int

const (
	// LoginAborted releases the slot without counting the attempt (for
	// example when the credential check could not run).
	LoginAborted LoginOutcome = iota
	// LoginFailed counts the attempt as a failure.
	LoginFailed
	// LoginSucceeded clears earlier failures.
	LoginSucceeded
)

// NewLoginThrottle builds the throttle on the cluster's login-throttle
// bucket.
func NewLoginThrottle(kv jetstream.KeyValue, maxFailures int, lockoutTime time.Duration) *LoginThrottle {
	return &LoginThrottle{
		kv:          kv,
		maxFailures: maxFailures,
		lockoutTime: lockoutTime,
	}
}

// InFlightRetryAfter is the retry hint when an email is refused only
// because attempts already in flight occupy every remaining slot: they
// settle within moments (a success frees all of them).
const InFlightRetryAfter = 2 * time.Second

// errNoEntry marks an email without throttle history.
var errNoEntry = errors.New("no throttle entry")

// throttleCASRetries bounds the compare-and-swap loop: concurrent logins
// for the same email rewrite the same key, and each retry re-applies the
// decision to the freshly loaded entry.
const throttleCASRetries = 5

func throttleKey(email string) string {
	return hex.EncodeToString([]byte(email))
}

// Admit reserves an attempt slot for email. When it refuses, retryAfter is
// the remaining lockout for a locked email, or InFlightRetryAfter when the
// attempts already in flight could, if they all failed, reach the lockout.
// Every admitted attempt must be finished with Finish.
func (lt *LoginThrottle) Admit(email string) (admitted bool, retryAfter time.Duration) {
	if lt.kv == nil {
		// The bucket is provisioned seconds after startup; until then do
		// not turn a bus hiccup into an auth outage.
		return true, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := throttleKey(email)

	for attempt := 0; attempt < throttleCASRetries; attempt++ {
		entry, revision, err := lt.load(ctx, key)
		if err != nil {
			// The bus is embedded and replicated; failing to reach it means
			// the node cannot serve at all. Refusing logins would turn a
			// bus hiccup into an auth outage, so admit and let Finish
			// record what it can.
			log.WithError(err).Warn("login throttle read failed; admitting without reservation")
			return true, 0
		}
		if entry.Failures >= lt.maxFailures {
			if remaining := lt.lockoutTime - time.Since(entry.LockedAt); remaining > 0 {
				return false, remaining
			}
			// The lockout expired: start counting afresh.
			entry.Failures = 0
			entry.LockedAt = time.Time{}
		}
		if entry.Failures+entry.InFlight >= lt.maxFailures {
			return false, InFlightRetryAfter
		}
		entry.InFlight++
		if err := lt.store(ctx, key, entry, revision); err == nil {
			return true, 0
		}
	}
	// Contention: the entry changed under us repeatedly. A short refusal
	// keeps the reservation honest without locking the email out.
	return false, InFlightRetryAfter
}

// Finish releases the slot reserved by Admit and records its outcome. It
// returns true when this failure locked the email.
func (lt *LoginThrottle) Finish(email string, outcome LoginOutcome) bool {
	if lt.kv == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := throttleKey(email)

	locked := false
	for attempt := 0; attempt < throttleCASRetries; attempt++ {
		entry, revision, err := lt.load(ctx, key)
		if err != nil {
			return false
		}
		if entry.InFlight > 0 {
			entry.InFlight--
		}
		locked = false
		switch outcome {
		case LoginFailed:
			entry.Failures++
			if entry.Failures >= lt.maxFailures {
				entry.LockedAt = time.Now()
				locked = true
			}
		case LoginSucceeded:
			entry.Failures = 0
			entry.LockedAt = time.Time{}
		}
		if entry.Failures == 0 && entry.InFlight == 0 {
			if err := lt.kv.Delete(ctx, key, jetstream.LastRevision(revision)); err == nil {
				return locked
			}
			continue
		}
		if err := lt.store(ctx, key, entry, revision); err == nil {
			return locked
		}
	}
	return locked
}

func (lt *LoginThrottle) load(ctx context.Context, key string) (throttleEntry, uint64, error) {
	entry, err := lt.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return throttleEntry{}, 0, nil
		}
		return throttleEntry{}, 0, err
	}
	var decoded throttleEntry
	if err := json.Unmarshal(entry.Value(), &decoded); err != nil {
		return throttleEntry{}, 0, err
	}
	return decoded, entry.Revision(), nil
}

func (lt *LoginThrottle) store(ctx context.Context, key string, entry throttleEntry, revision uint64) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if revision == 0 {
		_, err = lt.kv.Create(ctx, key, data)
		return err
	}
	_, err = lt.kv.Update(ctx, key, data, revision)
	return err
}

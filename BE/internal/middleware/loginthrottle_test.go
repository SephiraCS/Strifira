package middleware

import (
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginThrottleLocksAfterMaxFailures(t *testing.T) {
	kv := bus.NewTestKV(t, "login-throttle-test", 30*time.Minute)
	lt := NewLoginThrottle(func() jetstream.KeyValue { return kv }, 3, time.Minute)
	email := "attacker@cluster.test"

	// Three admitted attempts that all fail reach the lockout; the last
	// failure is the one that locks the account.
	for i := 0; i < 3; i++ {
		admitted, retryAfter := lt.Admit(email)
		require.True(t, admitted, "attempt %d should be admitted", i+1)
		require.Zero(t, retryAfter)
		assert.Equal(t, i == 2, lt.Finish(email, LoginFailed), "failure %d", i+1)
	}
	admitted, retryAfter := lt.Admit(email)
	assert.False(t, admitted, "locked email must be refused")
	assert.Positive(t, retryAfter)

	// Another account is unaffected.
	admitted, _ = lt.Admit("victim@cluster.test")
	assert.True(t, admitted)
}

func TestLoginThrottleSuccessClearsFailures(t *testing.T) {
	kv := bus.NewTestKV(t, "login-throttle-test", 30*time.Minute)
	lt := NewLoginThrottle(func() jetstream.KeyValue { return kv }, 3, time.Minute)
	email := "human@cluster.test"

	for i := 0; i < 2; i++ {
		admitted, _ := lt.Admit(email)
		require.True(t, admitted)
		lt.Finish(email, LoginFailed)
	}
	admitted, _ := lt.Admit(email)
	require.True(t, admitted)
	lt.Finish(email, LoginSucceeded)

	for i := 0; i < 2; i++ {
		admitted, _ = lt.Admit(email)
		require.True(t, admitted)
		lt.Finish(email, LoginFailed)
	}
	admitted, _ = lt.Admit(email)
	assert.True(t, admitted, "a successful login resets the counter")
}

func TestLoginThrottleInFlightSlotsAreReserved(t *testing.T) {
	kv := bus.NewTestKV(t, "login-throttle-test", 30*time.Minute)
	lt := NewLoginThrottle(func() jetstream.KeyValue { return kv }, 3, time.Minute)
	email := "burst@cluster.test"

	// Three concurrent attempts hold every slot; a fourth is refused with
	// the short in-flight hint instead of the lockout.
	for i := 0; i < 3; i++ {
		admitted, _ := lt.Admit(email)
		require.True(t, admitted)
	}
	admitted, retryAfter := lt.Admit(email)
	assert.False(t, admitted)
	assert.Equal(t, InFlightRetryAfter, retryAfter)
}

package middleware

import (
	"sync"
	"time"
)

// LoginThrottle tracks failed login attempts per email and temporarily locks
// accounts. Admission is atomic: an attempt reserves a slot before the
// credentials are checked, and failures plus in-flight attempts never exceed
// maxFailures, so a burst of concurrent requests cannot test more passwords
// than a sequential attacker could. The mutex is only held for bookkeeping,
// never while a password is being verified.
type LoginThrottle struct {
	mu          sync.Mutex
	attempts    map[string]*loginAttempt
	maxFailures int
	lockoutTime time.Duration
}

type loginAttempt struct {
	failures int
	inFlight int
	lockedAt time.Time
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

func NewLoginThrottle(maxFailures int, lockoutTime time.Duration) *LoginThrottle {
	lt := &LoginThrottle{
		attempts:    make(map[string]*loginAttempt),
		maxFailures: maxFailures,
		lockoutTime: lockoutTime,
	}
	go lt.cleanup()
	return lt
}

// LockoutDuration is how long an email stays locked after maxFailures.
func (lt *LoginThrottle) LockoutDuration() time.Duration {
	return lt.lockoutTime
}

// Admit reserves an attempt slot for email. It returns false when the email
// is locked or when the attempts already in flight could, if they all fail,
// reach the lockout. Every admitted attempt must be finished with Finish.
func (lt *LoginThrottle) Admit(email string) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	a, exists := lt.attempts[email]
	if !exists {
		a = &loginAttempt{}
		lt.attempts[email] = a
	}
	if a.failures >= lt.maxFailures {
		if time.Since(a.lockedAt) < lt.lockoutTime {
			return false
		}
		// The lockout expired: start counting afresh.
		a.failures = 0
		a.lockedAt = time.Time{}
	}
	if a.failures+a.inFlight >= lt.maxFailures {
		return false
	}
	a.inFlight++
	return true
}

// Finish releases the slot reserved by Admit and records its outcome. It
// returns true when this failure locked the email.
func (lt *LoginThrottle) Finish(email string, outcome LoginOutcome) bool {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	a, exists := lt.attempts[email]
	if !exists {
		return false
	}
	if a.inFlight > 0 {
		a.inFlight--
	}
	locked := false
	switch outcome {
	case LoginFailed:
		a.failures++
		if a.failures >= lt.maxFailures {
			a.lockedAt = time.Now()
			locked = true
		}
	case LoginSucceeded:
		a.failures = 0
		a.lockedAt = time.Time{}
	}
	if a.failures == 0 && a.inFlight == 0 {
		delete(lt.attempts, email)
	}
	return locked
}

func (lt *LoginThrottle) cleanup() {
	for {
		time.Sleep(5 * time.Minute)
		lt.mu.Lock()
		for email, a := range lt.attempts {
			if a.inFlight == 0 && time.Since(a.lockedAt) > lt.lockoutTime*2 {
				delete(lt.attempts, email)
			}
		}
		lt.mu.Unlock()
	}
}

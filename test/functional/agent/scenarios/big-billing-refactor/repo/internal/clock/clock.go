// Package clock is the source of time for everything under internal/.
package clock

import "time"

// Clock tells the time.
type Clock interface {
	Now() time.Time
}

// Real is the system clock.
type Real struct{}

// Now returns the current time.
func (Real) Now() time.Time { return time.Now() }

// Fixed is a clock stopped at a time, for tests.
type Fixed struct{ T time.Time }

// Now returns the fixed time.
func (f Fixed) Now() time.Time { return f.T }

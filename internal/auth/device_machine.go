package auth

import (
	"errors"
	"time"
)

// DevicePollState is the state the device-code polling loop moves into after a
// single token-endpoint answer. It makes the RFC 8628 polling state machine
// visible to callers instead of hiding it inside DevicePoll's loop.
type DevicePollState string

// DevicePollState values reported by ClassifyDevicePoll.
const (
	// DevicePollAuthorized means the endpoint returned a token; polling is done.
	DevicePollAuthorized DevicePollState = "authorized"
	// DevicePollWaiting means the user has not finished yet; poll again after Wait.
	DevicePollWaiting DevicePollState = "waiting"
	// DevicePollExpired means the device code (or the polling window) ran out.
	DevicePollExpired DevicePollState = "expired"
	// DevicePollDenied means the user declined the authorization.
	DevicePollDenied DevicePollState = "denied"
	// DevicePollFailed means an unexpected response; stop without retrying.
	DevicePollFailed DevicePollState = "failed"
)

// String implements fmt.Stringer.
func (s DevicePollState) String() string { return string(s) }

// DevicePollDecision is one step of the device-code polling state machine.
// Wait is the delay before the next poll and is meaningful only when State is
// DevicePollWaiting; for every terminal state it is zero.
type DevicePollDecision struct {
	State DevicePollState
	Wait  time.Duration
}

// ClassifyDevicePoll maps one token-endpoint result into the next polling
// decision. It is a pure function: no I/O, no clock, no sleeps, so callers can
// drive the state machine in tests or reuse the classification independently of
// DevicePoll.
//
// A nil err is success (the response already carries the token). A tokenError
// with code authorization_pending keeps the loop waiting at the same interval;
// slow_down also keeps it waiting but lengthens the interval by
// deviceSlowDownStep; expired_token and access_denied are terminal. Any other
// error (including a non-tokenError transport failure) is a failure that stops
// the loop. The supplied interval is passed through unchanged except on
// slow_down, so a caller can feed the previous decision's Wait back in.
func ClassifyDevicePoll(err error, interval time.Duration) DevicePollDecision {
	if err == nil {
		return DevicePollDecision{State: DevicePollAuthorized}
	}
	var failure *tokenError
	if !errors.As(err, &failure) {
		return DevicePollDecision{State: DevicePollFailed}
	}
	switch failure.Code {
	case "authorization_pending":
		return DevicePollDecision{State: DevicePollWaiting, Wait: interval}
	case "slow_down":
		return DevicePollDecision{State: DevicePollWaiting, Wait: interval + deviceSlowDownStep}
	case "expired_token":
		return DevicePollDecision{State: DevicePollExpired}
	case "access_denied":
		return DevicePollDecision{State: DevicePollDenied}
	default:
		return DevicePollDecision{State: DevicePollFailed}
	}
}

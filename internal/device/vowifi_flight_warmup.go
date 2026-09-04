package device

import (
	"context"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost"
)

const (
	defaultVoWiFiFlightWarmupWait = 5 * time.Second
	defaultVoWiFiFlightWarmupPoll = 250 * time.Millisecond
)

type voWiFiFlightWarmupState struct {
	Ready         bool
	RegStatus     int
	RegStatusText string
	Attempts      int
}

func waitVoWiFiFlightWarmup(ctx context.Context, modem runtimehost.Modem, maxWait, poll time.Duration) voWiFiFlightWarmupState {
	if ctx == nil {
		ctx = context.Background()
	}
	if modem == nil {
		return voWiFiFlightWarmupState{}
	}
	if maxWait <= 0 {
		maxWait = defaultVoWiFiFlightWarmupWait
	}
	if poll <= 0 {
		poll = defaultVoWiFiFlightWarmupPoll
	}

	deadline := time.Now().Add(maxWait)
	state := voWiFiFlightWarmupState{}
	for {
		state.RegStatus, state.RegStatusText = modem.GetRegStatus()
		state.Attempts++
		if !isVoWiFiRegisteredState(state.RegStatus) {
			state.Ready = true
			return state
		}
		if time.Now().Add(poll).After(deadline) {
			return state
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return state
		case <-timer.C:
		}
	}
}

func isVoWiFiRegisteredState(status int) bool {
	return status == 1 || status == 5
}

package vowifihost

import (
	"errors"
	"testing"
	"time"

	"github.com/iniwex5/vowifi-go/runtimehost"
)

func TestManagerBeginAndFailStartOwnsStartupMutationAndBroadcast(t *testing.T) {
	manager := NewManager()
	deviceID := "dev-start"
	ch, unsub := manager.SubscribeState(deviceID)
	defer unsub()

	claim := manager.BeginStart(deviceID)
	if !claim.Accepted {
		t.Fatalf("BeginStart() = %+v, want Accepted", claim)
	}
	if !manager.RuntimeStore().Starting(deviceID) {
		t.Fatal("runtime should be marked starting")
	}

	manager.FailStart(deviceID, claim.Epoch, runtimehost.State{DeviceID: deviceID}, errors.New("start failed"))

	if manager.RuntimeStore().Starting(deviceID) {
		t.Fatal("runtime starting flag should be cleared")
	}
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected failure broadcast")
	}
}

func TestManagerFailStartClassifiesSOCKS5UDPTimeout(t *testing.T) {
	manager := NewManager()
	deviceID := "dev-proxy-timeout"
	claim := manager.BeginStart(deviceID)
	err := errors.New("SWU tunnel establishment failed: read udp 127.0.0.1:46072->127.0.0.1:50743: i/o timeout")

	manager.FailStart(deviceID, claim.Epoch, runtimehost.State{DeviceID: deviceID}, err)

	state, ok := manager.RuntimeStore().State(deviceID)
	if !ok {
		t.Fatal("State() ok=false, want failed startup state")
	}
	if state.Phase != runtimehost.PhaseError {
		t.Fatalf("phase=%q, want error", state.Phase)
	}
	if state.LastErrorClass != "proxy" {
		t.Fatalf("last_error_class=%q, want proxy", state.LastErrorClass)
	}
	if state.LastError != err.Error() {
		t.Fatalf("last_error=%q, want %q", state.LastError, err.Error())
	}
}

func TestManagerShouldRunMatchesRuntimeEpoch(t *testing.T) {
	manager := NewManager()
	deviceID := "dev-epoch"
	claim := manager.BeginStart(deviceID)

	if !manager.ShouldRun(deviceID, claim.Epoch) {
		t.Fatal("ShouldRun() = false for current epoch, want true")
	}
	manager.InvalidateRuntime(deviceID, "test")
	if manager.ShouldRun(deviceID, claim.Epoch) {
		t.Fatal("ShouldRun() = true for stale epoch, want false")
	}
}

func TestManagerClaimStartedAcceptsCurrentAndRejectsStaleEpoch(t *testing.T) {
	manager := NewManager()
	deviceID := "dev-claim"
	stale := manager.CurrentEpoch(deviceID)
	manager.InvalidateRuntime(deviceID, "test")

	if manager.ClaimStarted(deviceID, stale, &runtimehost.Instance{}) {
		t.Fatal("ClaimStarted() = true for stale epoch, want false")
	}
	if manager.RuntimeStore().Active(deviceID) {
		t.Fatal("stale claim should not activate runtime")
	}

	current := manager.CurrentEpoch(deviceID)
	inst := &runtimehost.Instance{}
	if !manager.ClaimStarted(deviceID, current, inst) {
		t.Fatal("ClaimStarted() = false for current epoch, want true")
	}
	if manager.RuntimeStore().Instance(deviceID) != inst {
		t.Fatal("current claim should store active instance")
	}
}

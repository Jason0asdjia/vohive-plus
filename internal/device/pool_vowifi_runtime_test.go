package device

import (
	"context"
	"errors"
	"testing"
	"time"

	qmimanager "github.com/iniwex5/quectel-qmi-go/pkg/manager"
	"github.com/swordstudiox/vohive-plus/internal/backend"
	"github.com/swordstudiox/vohive-plus/internal/config"
)

type mockReadinessBackend struct {
	backend.DeviceBackend
	r qmimanager.UIMReadiness
	e error
}

func (m *mockReadinessBackend) GetUIMReadiness(ctx context.Context) (qmimanager.UIMReadiness, error) {
	return m.r, m.e
}

func TestWaitUIMIdentityReady_ReadyWithIdentity(t *testing.T) {
	p := NewPool(&config.Config{})
	w := &Worker{ID: "test-mbim", Backend: &mockReadinessBackend{
		r: qmimanager.UIMReadiness{
			Reason: qmimanager.UIMReadinessReady,
			ICCID:  "123",
			IMSI:   "456",
		},
	}}
	p.mu.Lock()
	p.workers["test-mbim"] = w
	p.mu.Unlock()

	err := p.WaitQMICoreReady("test-mbim", 1*time.Second)
	if err != nil {
		t.Fatalf("expected nil error when identity is ready, got %v", err)
	}
}

func TestWaitQMICoreReady_IdentityEmpty(t *testing.T) {
	p := NewPool(&config.Config{})
	w := &Worker{ID: "test-mbim", Backend: &mockReadinessBackend{
		r: qmimanager.UIMReadiness{
			Reason: qmimanager.UIMReadinessIdentityEmpty,
		},
	}}
	p.mu.Lock()
	p.workers["test-mbim"] = w
	p.mu.Unlock()

	err := p.WaitQMICoreReady("test-mbim", 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout (DeadlineExceeded) when identity is empty, got %v", err)
	}
}

func TestWaitQMICoreReady_CardAbsent(t *testing.T) {
	p := NewPool(&config.Config{})
	w := &Worker{ID: "test-mbim", Backend: &mockReadinessBackend{
		r: qmimanager.UIMReadiness{
			Reason:      qmimanager.UIMReadinessCardAbsent,
			CardPresent: false,
		},
	}}
	p.mu.Lock()
	p.workers["test-mbim"] = w
	p.mu.Unlock()

	err := p.WaitQMICoreReady("test-mbim", 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout (DeadlineExceeded) when card is absent, got %v", err)
	}
}

func TestWaitQMICoreReady_TransportFatal(t *testing.T) {
	p := NewPool(&config.Config{})
	w := &Worker{ID: "test-mbim", Backend: &mockReadinessBackend{
		r: qmimanager.UIMReadiness{
			Reason:         qmimanager.UIMReadinessTransportFatal,
			TransportReady: false,
		},
	}}
	p.mu.Lock()
	p.workers["test-mbim"] = w
	p.mu.Unlock()

	err := p.WaitQMICoreReady("test-mbim", 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout (DeadlineExceeded) when transport is fatal, got %v", err)
	}
}

type flightWarmupModemStub struct {
	statuses []int
	calls    int
}

func (m *flightWarmupModemStub) DeviceID() string                { return "wwan0" }
func (m *flightWarmupModemStub) IsHealthy() bool                 { return true }
func (m *flightWarmupModemStub) IsSimInserted() bool             { return true }
func (m *flightWarmupModemStub) QuerySIMInserted() (bool, error) { return true, nil }
func (m *flightWarmupModemStub) GetNetworkMode() string          { return "LTE" }
func (m *flightWarmupModemStub) Stop()                           {}
func (m *flightWarmupModemStub) GetRegStatus() (int, string) {
	if m.calls >= len(m.statuses) {
		m.calls++
		return m.statuses[len(m.statuses)-1], "last"
	}
	status := m.statuses[m.calls]
	m.calls++
	return status, "seq"
}

func TestWaitVoWiFiFlightWarmupWaitsUntilModemLeavesRegisteredState(t *testing.T) {
	modem := &flightWarmupModemStub{statuses: []int{5, 5, 0}}

	state := waitVoWiFiFlightWarmup(context.Background(), modem, 50*time.Millisecond, time.Nanosecond)

	if !state.Ready {
		t.Fatalf("warmup ready=false, state=%+v", state)
	}
	if state.RegStatus != 0 {
		t.Fatalf("warmup final reg status=%d, want 0", state.RegStatus)
	}
	if modem.calls != 3 {
		t.Fatalf("GetRegStatus calls=%d, want 3", modem.calls)
	}
}

func TestWaitVoWiFiFlightWarmupTimesOutButReturnsLastRegisteredState(t *testing.T) {
	modem := &flightWarmupModemStub{statuses: []int{5, 5, 5, 5}}

	state := waitVoWiFiFlightWarmup(context.Background(), modem, time.Millisecond, time.Nanosecond)

	if state.Ready {
		t.Fatalf("warmup ready=true, state=%+v", state)
	}
	if state.RegStatus != 5 {
		t.Fatalf("warmup final reg status=%d, want 5", state.RegStatus)
	}
	if modem.calls == 0 {
		t.Fatal("GetRegStatus was not called")
	}
}

package device

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/swordstudiox/vohive-plus/internal/backend"
	"github.com/swordstudiox/vohive-plus/internal/cardpolicy"
	"github.com/swordstudiox/vohive-plus/internal/config"
)

// Hide QMI-only capabilities: this backend must exercise the actual AT path.
type atSwitchReloadBackend struct {
	backend.DeviceBackend
	iccid          string
	imsi           string
	modes          []backend.OperatingMode
	mode           backend.OperatingMode
	onMode         func(backend.OperatingMode) error
	onRead         func()
	activate       bool
	iccidErr       error
	onlineFailures int
}

func (b *atSwitchReloadBackend) Mode() string { return backend.BackendAT }
func (b *atSwitchReloadBackend) GetICCIDLive(context.Context) (string, error) {
	if b.onRead != nil {
		b.onRead()
	}
	return b.iccid, b.iccidErr
}
func (b *atSwitchReloadBackend) GetIMSILive(context.Context) (string, error) { return b.imsi, nil }
func (b *atSwitchReloadBackend) GetOperatingMode(context.Context) (backend.OperatingMode, error) {
	return b.mode, nil
}
func (b *atSwitchReloadBackend) SetOperatingMode(ctx context.Context, mode backend.OperatingMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.modes = append(b.modes, mode)
	if mode == backend.ModeOnline && b.onlineFailures > 0 {
		b.onlineFailures--
		return errors.New("CFUN=1 transient failure")
	}
	if b.onMode != nil {
		if err := b.onMode(mode); err != nil {
			return err
		}
	}
	if mode == backend.ModeOnline && b.mode == backend.ModeLowPower && b.activate {
		b.iccid, b.imsi = "target", "204047123456789"
	}
	b.mode = mode
	return nil
}

func newATSwitchReloadTest(t *testing.T) (*Pool, *Worker, *atSwitchReloadBackend, esimSwitchContext) {
	t.Helper()
	withFastPostSwitchIdentityPolling(t)
	p := NewPool(&config.Config{})
	t.Cleanup(p.cancel)
	be := &atSwitchReloadBackend{DeviceBackend: &esimSwitchRestoreBackendStub{}, iccid: "old", imsi: "234159123456789", mode: backend.ModeOnline, activate: true}
	w := &Worker{ID: "dev-at", Config: config.DeviceConfig{ID: "dev-at"}, Backend: be}
	p.workers[w.ID] = w
	snapshot := p.beginESIMSwitch(w.ID, "target")
	snapshot.IdentityGeneration = w.BeginSIMIdentityTransition("target", "test")
	return p, w, be, snapshot
}

func TestATSwitchReloadOnlineDefaultConfigurationActivatesTarget(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Ready || result.Degraded {
		t.Fatalf("convergence=%+v", result)
	}
	if !reflect.DeepEqual(be.modes, []backend.OperatingMode{backend.ModeLowPower, backend.ModeOnline}) {
		t.Fatalf("modes=%v; want CFUN=0 then 1", be.modes)
	}
	if ready, err := p.refreshPostSwitchIdentity(w.ID, w, snapshot); !ready || err != nil {
		t.Fatalf("identity ready=%v err=%v", ready, err)
	}
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
	if result := p.resolveAndApplyPolicy(w, "esim_switched"); !result.Applied || result.ICCID != "target" || !w.Config.RoamingEnabled {
		t.Fatalf("target policy not applied: %+v", result)
	}
}

func TestATSwitchReloadSkipsNaturallyActiveTarget(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onRead = func() { be.iccid = "target" }
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Ready || len(be.modes) != 0 {
		t.Fatalf("result=%+v modes=%v", result, be.modes)
	}
}

func TestATSwitchReloadFailureKeepsIdentityUnconfirmed(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onMode = func(mode backend.OperatingMode) error {
		if mode == backend.ModeLowPower {
			return errors.New("CFUN=0 failed")
		}
		return nil
	}
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Degraded || result.Ready || !w.SIMIdentityUnconfirmed() {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(be.modes, []backend.OperatingMode{backend.ModeLowPower, backend.ModeOnline}) {
		t.Fatalf("must compensate uncertain CFUN=0 result: %v", be.modes)
	}
}

func TestATSwitchReloadRejectsOldTokenBeforeHardwareMutation(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onRead = func() { p.clearESIMSwitchIfToken(w.ID, snapshot.SwitchToken) }
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Degraded || len(be.modes) != 0 {
		t.Fatalf("result=%+v modes=%v", result, be.modes)
	}
}

func TestATSwitchReloadOnlineFailureIsDegraded(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onMode = func(mode backend.OperatingMode) error {
		if mode == backend.ModeOnline {
			return errors.New("CFUN=1 failed")
		}
		return nil
	}
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Degraded || result.Ready || !w.SIMIdentityUnconfirmed() {
		t.Fatalf("failed online restore cannot report success: %+v", result)
	}
	if be.mode != backend.ModeLowPower {
		t.Fatalf("permanent CFUN=1 failure must not claim modem is online: mode=%v", be.mode)
	}
}

func TestATSwitchReloadRetriesTransientOnlineFailureUntilOnline(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onlineFailures = 1

	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Ready || result.Degraded || be.mode != backend.ModeOnline {
		t.Fatalf("transient CFUN=1 failure was not recovered: result=%+v mode=%v", result, be.mode)
	}
	onlineCalls := 0
	for _, mode := range be.modes {
		if mode == backend.ModeOnline {
			onlineCalls++
		}
	}
	if onlineCalls < 2 {
		t.Fatalf("online calls=%d; want retry after transient CFUN=1 failure", onlineCalls)
	}
}

func TestATSwitchReloadCancellationRestoresOnline(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onMode = func(mode backend.OperatingMode) error {
		if mode == backend.ModeLowPower {
			p.cancel()
		}
		return nil
	}
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Degraded || be.mode != backend.ModeOnline {
		t.Fatalf("result=%+v mode=%v modes=%v", result, be.mode, be.modes)
	}
}

func TestATSwitchReloadInvalidationWhileLowPowerOnlyRestoresOnline(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.onMode = func(mode backend.OperatingMode) error {
		if mode == backend.ModeLowPower {
			p.clearESIMSwitchIfToken(w.ID, snapshot.SwitchToken)
		}
		return nil
	}
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Degraded || be.mode != backend.ModeOnline || w.ConfirmedICCID() != "" {
		t.Fatalf("result=%+v mode=%v", result, be.mode)
	}
}

func TestATSwitchReloadRejectsStaleGeneration(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	w.BeginSIMIdentityTransition("next-target", "next-switch")
	result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
	if !result.Degraded || len(be.modes) != 0 {
		t.Fatalf("result=%+v modes=%v", result, be.modes)
	}
}

func TestATSwitchReloadStaleGenerationCannotReplaceNewTarget(t *testing.T) {
	p, w, _, snapshot := newATSwitchReloadTest(t)
	w.BeginSIMIdentityTransition("next-target", "next-switch")

	ready, err := p.refreshPostSwitchIdentityWithPolling(w.ID, w, snapshot, 20*time.Millisecond, time.Millisecond)
	if err == nil || ready {
		t.Fatalf("identity ready=%v err=%v; stale generation must be rejected", ready, err)
	}
	if !w.SIMIdentityConvergenceMatches("next-target", 0) {
		t.Fatal("stale refresh replaced the newer switch target")
	}
}

func TestATSwitchReloadStillOldDoesNotConfirmIdentity(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.activate = false
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
	p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
	if !w.SIMIdentityUnconfirmed() || w.Config.RoamingEnabled {
		t.Fatal("unchanged live identity must not confirm target or apply its policy")
	}
	lowPowerCalls := 0
	for _, mode := range be.modes {
		if mode == backend.ModeLowPower {
			lowPowerCalls++
		}
	}
	if lowPowerCalls != 1 {
		t.Fatalf("low power calls=%d want one controlled reload", lowPowerCalls)
	}
}

func TestATSwitchReloadDoesNotCommitTargetWithoutIMSI(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.iccid = "target"
	be.imsi = ""

	ready, err := p.refreshPostSwitchIdentityWithPolling(w.ID, w, snapshot, 20*time.Millisecond, time.Millisecond)
	if err == nil || ready {
		t.Fatalf("identity ready=%v err=%v; target ICCID without IMSI must remain unconfirmed", ready, err)
	}
	if w.ConfirmedICCID() != "" || !w.SIMIdentityUnconfirmed() {
		t.Fatalf("target identity was committed without IMSI: confirmed_iccid=%q", w.ConfirmedICCID())
	}
}

func TestATSwitchReloadDoesNotCommitTargetWhenLiveICCIDReadFails(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	be.iccid = "target"
	be.iccidErr = errors.New("QCCID read failed")

	ready, err := p.refreshPostSwitchIdentityWithPolling(w.ID, w, snapshot, 20*time.Millisecond, time.Millisecond)
	if err == nil || ready {
		t.Fatalf("identity ready=%v err=%v; ICCID read error must block target commit", ready, err)
	}
	if w.ConfirmedICCID() != "" || !w.SIMIdentityUnconfirmed() {
		t.Fatalf("target identity was committed after ICCID read failure: confirmed_iccid=%q", w.ConfirmedICCID())
	}
}

func TestATSwitchReloadTokenInvalidatedAfterIdentityReadDoesNotCommitOrApplyPolicy(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
	be.onRead = func() {
		if be.iccid == "target" {
			p.clearESIMSwitchIfToken(w.ID, snapshot.SwitchToken)
		}
	}

	p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
	if w.ConfirmedICCID() != "" || w.Config.RoamingEnabled {
		t.Fatalf("invalidated token committed identity or policy: iccid=%q roaming=%v", w.ConfirmedICCID(), w.Config.RoamingEnabled)
	}
}

func TestATSwitchReloadDuplicateFinalizePerTokenRunsOneReload(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	lowPowerStarted := make(chan struct{}, 1)
	releaseLowPower := make(chan struct{})
	be.onMode = func(mode backend.OperatingMode) error {
		if mode == backend.ModeLowPower {
			lowPowerStarted <- struct{}{}
			<-releaseLowPower
		}
		return nil
	}

	firstDone := make(chan struct{})
	go func() {
		p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
		close(firstDone)
	}()
	select {
	case <-lowPowerStarted:
	case <-time.After(time.Second):
		t.Fatal("first finalize did not reach CFUN=0")
	}

	secondDone := make(chan struct{})
	go func() {
		p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("duplicate finalize entered a second reload instead of returning")
	}
	close(releaseLowPower)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first finalize did not finish")
	}
	lowPowerCalls := 0
	for _, mode := range be.modes {
		if mode == backend.ModeLowPower {
			lowPowerCalls++
		}
	}
	if lowPowerCalls != 1 {
		t.Fatalf("low power calls=%d; want exactly one per switch token", lowPowerCalls)
	}
}

func TestATSwitchReloadOldFinalizeCannotReplaceNewSwitchTargetAfterClaim(t *testing.T) {
	p, w, _, first := newATSwitchReloadTest(t)
	claimed := make(chan struct{})
	release := make(chan struct{})
	p.postSwitchFinalizeClaimHook = func() {
		close(claimed)
		<-release
	}
	t.Cleanup(func() { p.postSwitchFinalizeClaimHook = nil })

	firstDone := make(chan struct{})
	go func() {
		p.handleESIMSwitchAfter(w.ID, first.SwitchToken)
		close(firstDone)
	}()
	select {
	case <-claimed:
	case <-time.After(time.Second):
		t.Fatal("first finalize did not pause after claim")
	}

	secondToken := p.handleESIMSwitchBefore(w.ID, "target-2")
	if secondToken == first.SwitchToken {
		t.Fatal("second switch did not receive a new token")
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("old finalize did not return after newer switch began")
	}
	if !w.SIMIdentityConvergenceMatches("target-2", 0) || w.ConfirmedICCID() != "" {
		t.Fatalf("old finalize changed newer target: target2_matches=%v confirmed=%q", w.SIMIdentityConvergenceMatches("target-2", 0), w.ConfirmedICCID())
	}
}

func TestATSwitchReloadRetryCancelledTokenDoesNotRefreshOrApplyPolicy(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
	originalDelays := append([]time.Duration(nil), postSwitchIdentityRetryDelays...)
	postSwitchIdentityRetryDelays = []time.Duration{40 * time.Millisecond}
	t.Cleanup(func() { postSwitchIdentityRetryDelays = originalDelays })
	read := make(chan struct{}, 1)
	be.onRead = func() { read <- struct{}{} }

	p.schedulePostSwitchIdentityRefreshes(w.ID, snapshot)
	p.clearESIMSwitchIfToken(w.ID, snapshot.SwitchToken)
	time.Sleep(100 * time.Millisecond)
	select {
	case <-read:
		t.Fatal("cancelled retry still read SIM identity")
	default:
	}
	if w.ConfirmedICCID() != "" || w.Config.RoamingEnabled {
		t.Fatalf("cancelled retry committed identity or policy: iccid=%q roaming=%v", w.ConfirmedICCID(), w.Config.RoamingEnabled)
	}
}

func TestATSwitchReloadPolicyRejectsTokenOrGenerationChangedAtCommit(t *testing.T) {
	p, w, _, snapshot := newATSwitchReloadTest(t)
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
	p.postSwitchPolicyProjectionHook = func() {
		p.clearESIMSwitchIfToken(w.ID, snapshot.SwitchToken)
		w.BeginSIMIdentityTransition("target-2", "newer_switch")
	}
	t.Cleanup(func() { p.postSwitchPolicyProjectionHook = nil })

	p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
	if w.Config.RoamingEnabled || !w.SIMIdentityConvergenceMatches("target-2", 0) {
		t.Fatalf("policy projected after authorization changed: roaming=%v target2_matches=%v", w.Config.RoamingEnabled, w.SIMIdentityConvergenceMatches("target-2", 0))
	}
}

func TestATSwitchReloadFinalizeAppliesTargetPolicy(t *testing.T) {
	p, w, be, snapshot := newATSwitchReloadTest(t)
	p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
	p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
	if w.ConfirmedICCID() != "target" || !w.Config.RoamingEnabled || p.IsESIMSwitching(w.ID) {
		t.Fatalf("identity=%q policy roaming=%v switching=%v", w.ConfirmedICCID(), w.Config.RoamingEnabled, p.IsESIMSwitching(w.ID))
	}
	if !reflect.DeepEqual(be.modes, []backend.OperatingMode{backend.ModeLowPower, backend.ModeOnline}) {
		t.Fatalf("modes=%v want one controlled reload", be.modes)
	}
}

package device

import (
	"context"
	"fmt"
	"time"

	"github.com/swordstudiox/vohive-plus/internal/backend"
	"github.com/swordstudiox/vohive-plus/pkg/logger"
)

const atSwitchOnlineRestoreAttempts = 3

// AT has no UIM readiness/power controller. Pool owns its reload just as it
// owns QMI recovery. RFOff (CFUN=4) does not reload the SIM on these modems;
// minimum functionality (CFUN=0) followed by Online does.
func (p *Pool) preparePostSwitchATIdentity(deviceID string, token uint64, worker *Worker, snapshot esimSwitchContext) postSwitchConvergenceResult {
	fail := func(err error) postSwitchConvergenceResult {
		logger.Warn("AT 切卡后 SIM 重载未完成", "device", deviceID, "switch_token", token, "err", err)
		return postSwitchConvergenceResult{Degraded: true, Reason: err.Error()}
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	current := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !p.switchTokenStillCurrent(deviceID, token, "at_sim_reload") {
			return fmt.Errorf("post_switch_token_stale")
		}
		if snapshot.IdentityGeneration != 0 && !worker.SIMIdentityConvergenceMatches(snapshot.TargetICCID, snapshot.IdentityGeneration) {
			return fmt.Errorf("post_switch_identity_generation_stale")
		}
		return nil
	}
	if err := current(); err != nil {
		return fail(err)
	}
	target := normalizeSIMIdentityForCompare(snapshot.TargetICCID)
	if target == "" {
		return postSwitchConvergenceResult{Ready: true, Reason: "at_target_unspecified_live_identity_fallback"}
	}
	reader, ok := worker.Backend.(liveSIMIdentityReader)
	if !ok {
		return fail(fmt.Errorf("live_identity_not_supported"))
	}
	// Give refresh=true or an earlier radio cycle a short chance to settle.
	wait := postSwitchIdentityPollTimeout
	if wait <= 0 || wait > 2*time.Second {
		wait = 2 * time.Second
	}
	interval := postSwitchIdentityPollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(wait)
	for {
		if err := current(); err != nil {
			return fail(err)
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		iccid, err := reader.GetICCIDLive(probeCtx)
		cancel()
		if stale := current(); stale != nil {
			return fail(stale)
		}
		if err == nil && normalizeSIMIdentityForCompare(iccid) == target {
			return postSwitchConvergenceResult{Ready: true, Reason: "at_target_already_active"}
		}
		if time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fail(ctx.Err())
		case <-timer.C:
		}
	}
	if err := current(); err != nil {
		return fail(err)
	}
	logger.Info("AT 切卡后目标身份未生效，执行一次 CFUN=0→1 SIM 重载", "device", deviceID, "switch_token", token, "target_iccid", snapshot.TargetICCID)
	offCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	offErr := worker.Backend.SetOperatingMode(offCtx, backend.ModeLowPower)
	cancel()
	var interrupted error
	if offErr == nil {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		interrupted = current()
	}
	// Always compensate once CFUN=0 was attempted, including timeout/cancel:
	// the modem may have accepted it even if the response was lost. This is
	// cleanup only; an invalidated switch must not publish identity or policy.
	onErr := restoreATOnlineAfterSwitch(worker)
	if offErr != nil || onErr != nil {
		return fail(fmt.Errorf("at_sim_reload_failed: low_power=%v online=%v", offErr, onErr))
	}
	if interrupted != nil {
		return fail(interrupted)
	}
	if err := current(); err != nil {
		return fail(err)
	}
	logger.Info("AT 切卡后 CFUN=0→1 完成，等待 live 目标身份确认", "device", deviceID, "switch_token", token)
	return postSwitchConvergenceResult{Ready: true, Reason: "at_sim_reload_submitted"}
}

func restoreATOnlineAfterSwitch(worker *Worker) error {
	if worker == nil || worker.Backend == nil {
		return fmt.Errorf("worker_or_backend_missing")
	}
	var lastErr error
	for attempt := 1; attempt <= atSwitchOnlineRestoreAttempts; attempt++ {
		setCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		setErr := worker.Backend.SetOperatingMode(setCtx, backend.ModeOnline)
		cancel()

		modeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		mode, modeErr := worker.Backend.GetOperatingMode(modeCtx)
		cancel()
		if modeErr == nil && mode == backend.ModeOnline {
			return nil
		}
		lastErr = fmt.Errorf("attempt=%d set_online=%v actual_mode=%v mode_read=%v", attempt, setErr, mode, modeErr)
	}
	return fmt.Errorf("at_sim_reload_online_restore_failed: %w", lastErr)
}

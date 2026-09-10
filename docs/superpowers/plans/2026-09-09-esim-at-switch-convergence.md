# eSIM AT 切卡收敛 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** AT 后端 eSIM 切卡只有在实时 ICCID 等于目标且 IMSI 已就绪后，才投影目标卡策略；默认配置下主动执行一次受控 SIM 重载。

**Architecture:** 将 AT 的身份收敛放在 Pool 的切卡收尾阶段，而不是以 eUICC profile 缓存作为成功依据。`preparePostSwitchATIdentity` 先短轮询实时 ICCID，未命中时执行一次 `ModeLowPower (CFUN=0)` 到 `ModeOnline (CFUN=1)`，随后复用既有的 ICCID+IMSI 轮询与 identity generation 门控；过期 token、取消或重载错误都不得提交新身份或策略。

**Tech Stack:** Go 1.26、internal/device Pool、AT DeviceBackend、Go testing。

---

### Task 1: 为默认 AT 切卡补齐受控重载路径

**Files:**
- Create: `internal/device/post_switch_at_reload.go`
- Modify: `internal/device/post_switch_convergence.go:314-328`
- Test: `internal/device/post_switch_at_reload_test.go`

- [ ] **Step 1: 写出默认 AT 情况下目标 profile 未真正激活的失败测试**

```go
func TestATSwitchReloadOnlineDefaultConfigurationActivatesTarget(t *testing.T) {
    p, w, be, snapshot := newATSwitchReloadTest(t)
    result := p.runPostSwitchConvergence(w.ID, snapshot.SwitchToken, w, snapshot)
    if !result.Ready || result.Degraded {
        t.Fatalf("convergence=%+v", result)
    }
    if !reflect.DeepEqual(be.modes, []backend.OperatingMode{backend.ModeLowPower, backend.ModeOnline}) {
        t.Fatalf("modes=%v; want CFUN=0 then 1", be.modes)
    }
}
```

- [ ] **Step 2: 运行测试，确认旧 AT fallback 不会发出 CFUN=0→1**

Run: `go test ./internal/device -run '^TestATSwitchReloadOnlineDefaultConfigurationActivatesTarget$' -count=1 -v`

Expected: FAIL，断言 `modes` 为空或不等于 `[ModeLowPower ModeOnline]`。

- [ ] **Step 3: 为没有 UIM readiness 的 AT backend 路由到 Pool 所有的重载函数**

```go
if !ok {
    if worker.Backend.Mode() == backend.BackendAT {
        return p.preparePostSwitchATIdentity(deviceID, token, worker, snapshot)
    }
    return postSwitchConvergenceResult{Ready: true, Reason: "uim_readiness_not_supported_live_identity_fallback"}
}
```

`preparePostSwitchATIdentity` 必须在每个硬件动作前检查 `switchTokenStillCurrent` 与 `SIMIdentityConvergenceMatches`，目标 ICCID 短轮询失败后只调用一次 `SetOperatingMode(ModeLowPower)` 与一次补偿性的 `SetOperatingMode(ModeOnline)`；它不得写入 worker 身份或策略。

- [ ] **Step 4: 运行 Task 1 的成功、自然生效、失败、取消、过期 token 和旧 generation 测试**

Run: `go test ./internal/device -run '^TestATSwitchReload(OnlineDefaultConfigurationActivatesTarget|SkipsNaturallyActiveTarget|FailureKeepsIdentityUnconfirmed|RejectsOldTokenBeforeHardwareMutation|OnlineFailureIsDegraded|CancellationRestoresOnline|InvalidationWhileLowPowerOnlyRestoresOnline|RejectsStaleGeneration)$' -count=1 -v`

Expected: PASS，且失败/取消场景最终不会留下 RF off。

### Task 2: 只以实时 AT 身份提交目标卡策略

**Files:**
- Modify: `internal/device/pool_esim_switch.go:430-480,667-706,1022-1140`
- Test: `internal/device/post_switch_at_reload_test.go:171-199`
- Test: `internal/device/pool_esim_switch_restore_test.go`

- [ ] **Step 1: 写出“CFUN 已执行但 ICCID 仍为旧卡”时不得投影目标策略的失败测试**

```go
func TestATSwitchReloadStillOldDoesNotConfirmIdentity(t *testing.T) {
    p, w, be, snapshot := newATSwitchReloadTest(t)
    be.activate = false
    p.SetPolicyResolver(&stubPolicyResolver{pol: cardpolicy.Policy{ICCID: "target", RoamingEnabled: true}})
    p.handleESIMSwitchAfter(w.ID, snapshot.SwitchToken)
    if !w.SIMIdentityUnconfirmed() || w.Config.RoamingEnabled {
        t.Fatal("unchanged live identity must not confirm target or apply its policy")
    }
}
```

- [ ] **Step 2: 运行测试，确认旧实现在 profile/APDU 成功时会错误应用目标策略**

Run: `go test ./internal/device -run '^TestATSwitchReloadStillOldDoesNotConfirmIdentity$' -count=1 -v`

Expected: FAIL，旧路径会接受缓存 profile 或把 `RoamingEnabled` 设为目标策略值。

- [ ] **Step 3: 保持提交门控为目标 ICCID 与非空 IMSI，并在成功后才 resolve policy**

```go
if targetICCIDKey != "" && newICCIDKey != targetICCIDKey {
    worker.MarkSIMIdentityDegraded("post_switch_finalize", fmt.Errorf("post_switch_target_iccid_not_active"))
    return false, err
}
if snapshot.IdentityGeneration != 0 && !worker.SIMIdentityConvergenceMatches(snapshot.TargetICCID, snapshot.IdentityGeneration) {
    return false, fmt.Errorf("post_switch_identity_generation_stale")
}
```

调用 `resolveAndApplyPolicy(worker, "esim_switched")` 必须位于 `refreshPostSwitchIdentity` 返回无错误之后；补刷新也必须使用同一代际检查再应用策略。

- [ ] **Step 4: 运行完整 eSIM 收敛回归集**

Run: `go test ./internal/device -run 'TestATSwitchReload|TestHandleESIMSwitchAfter|TestRefreshPostSwitchIdentityRejectsStaleGeneration|TestSchedulePostSwitchIdentityRefreshAppliesTargetCardPolicyAfterDeferredConvergence' -count=1 -v`

Expected: PASS。

- [ ] **Step 5: 以真实 AT 身份验收并记录 token 链路**

在实机执行 VOXI→Lebara→VOXI，每一次记录同一 `switch_token` 的 EnableProfile 结果、`AT+QCCID`、`AT+CIMI`、CFUN=0/1 结果和最终策略 ICCID。只有 `QCCID == target` 且 `CIMI` 非空后才标记成功；HTTP 200 和 overview 的 Enabled 状态都不作为验收依据。

### Task 3: 让切卡授权覆盖有限的延迟身份收敛

**Files:**
- Modify: `internal/device/pool.go`
- Modify: `internal/device/pool_esim_switch.go`
- Test: `internal/device/pool_esim_switch_restore_test.go`

- [ ] **Step 1: 写出正常 finalize 后延迟重试被旧 cleanup 取消的失败用例**

```go
func TestSchedulePostSwitchIdentityRefreshKeepsCurrentSessionUntilRetryCommits(t *testing.T) {
    p, worker, snapshot := newPostSwitchRetryTest(t)
    p.handleESIMSwitchAfter(worker.ID, snapshot.SwitchToken)
    advancePostSwitchRetryClock(t)
    if worker.ConfirmedICCID() != snapshot.TargetICCID {
        t.Fatalf("iccid=%q, want delayed target %q", worker.ConfirmedICCID(), snapshot.TargetICCID)
    }
}
```

- [ ] **Step 2: 运行测试，确认现有正常路径先清 token 导致 retry 被 stale check 拒绝**

Run: `go test ./internal/device -run '^TestSchedulePostSwitchIdentityRefreshKeepsCurrentSessionUntilRetryCommits$' -count=1 -v`

Expected: FAIL，日志含 `post_switch_token_stale` 或目标身份保持未确认。

- [ ] **Step 3: 将 token 的 finalize claim 与 retry lease 建模为同一短生命周期 session**

```go
type postSwitchSession struct {
    Token              uint64
    IdentityGeneration uint64
    FinalizeClaimed    bool
    RetryLeases        int
    RetryDeadline      time.Time
}
```

在 `switchMu` 内只完成 session 的 token/generation 比对、领取/释放 finalize claim 和 retry lease；不得在锁内调用 AT、刷新身份、数据库或策略投影。每个 retry 在启动、身份提交和策略投影前均复核 session；新切卡替换 token 时，旧 session 的 lease 立即失效。最后一个 lease 完成或超过 `RetryDeadline` 时清理 session。

- [ ] **Step 4: 运行正常延迟收敛、取消、旧 token 和新切卡覆盖的回归集**

Run: `go test ./internal/device -run 'TestSchedulePostSwitchIdentityRefreshKeepsCurrentSessionUntilRetryCommits|TestATSwitchReloadRetryCancelledTokenDoesNotRefreshOrApplyPolicy|TestATSwitchReloadOldFinalizeCannotReplaceNewSwitchTargetAfterClaim|TestATSwitchReloadPolicyRejectsTokenOrGenerationChangedAtCommit' -count=1 -v`

Expected: PASS；正常 retry 可以提交当前目标，取消和新 token 会阻止旧 retry。

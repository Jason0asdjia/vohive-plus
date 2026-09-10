# Lebara IKE_SA_INIT 诊断与最小修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用可比较的实机 IKE_SA_INIT 证据确定 Lebara 不能拉起 WiFi Calling 的网络或 proposal 根因，仅在证据表明协商差异时改动 IKE offer。

**Architecture:** VoWiFi 启动以实时 IMSI 与 SIM home MCC/MNC 派生 ePDG，默认前置代理优先于 MCC 国家规则。IKE_SA_INIT 超时发生在 EAP-AKA 之前，故探针固定 SOCKS5 UDP 出口，先比较 VOXI 与 Lebara 的目标/relay/响应，再比较 proposal；若两者皆无响应，收敛到代理出口而非协议代码。

**Tech Stack:** Go 1.26、vowifi-go SWU、SOCKS5 UDP、实机 ePDG。

---

### Task 1: 锁定 Lebara 的 ePDG 与默认代理选择

**Files:**
- Modify: `internal/device/pool_vowifi_country_proxy_test.go`
- Modify: `internal/device/vowifi_start_profile_test.go`
- Modify: `internal/device/vowifi_start_orchestrator.go:286-354`

- [ ] **Step 1: 写出 Lebara 画像与默认代理优先的失败测试**

```go
func TestResolveVoWiFiUpstreamProxyPrefersDefaultForLebara(t *testing.T) {
    // 在测试库写入 defaultProxy 和 NL 国家代理后，断言 resolver 返回 defaultProxy。
    got := resolveVoWiFiUpstreamProxy("204", "trace", "device")
    if got == nil || got.ID != defaultProxy.ID {
        t.Fatalf("proxy=%+v, want default proxy %d", got, defaultProxy.ID)
    }
}
```

并新增 profile 断言：Lebara 的 live MCC/MNC `204/04` 进入 `identity.PrepareStart` 后 ePDG 为 `epdg.epc.mnc004.mcc204.pub.3gppnetwork.org:4500`。

- [ ] **Step 2: 运行测试，确认旧路径会先命中 MCC=204 的国家规则**

Run: `go test ./internal/device -run 'TestResolveVoWiFiUpstreamProxyPrefersDefaultForLebara|TestBuildVoWiFiStartProfile' -count=1 -v`

Expected: 在默认代理优先逻辑缺失时 FAIL，返回 NL 国家代理或直连。

- [ ] **Step 3: 将启动代理解析固定为 默认代理 → MCC 国家规则 → 直连**

```go
defaultProxy, err := db.GetDefaultVoWiFiUpstreamProxy()
if err == nil && defaultProxy != nil {
    return runtimeProxyConfigFromDB(defaultProxy)
}
proxy, _, err := db.GetHomeMCCUpstreamProxy(homeMCC)
if err != nil || proxy == nil {
    return nil
}
return runtimeProxyConfigFromDB(proxy)
```

不得按 IMSI 直接拼出 MCC/MNC；`buildVoWiFiStartProfile` 仅接受实时或同 IMSI 的缓存 Native MCC/MNC，避免切卡迟到缓存污染 Lebara 画像。

- [ ] **Step 4: 运行启动画像及代理回归集**

Run: `go test ./internal/device -run 'Test.*VoWiFi.*(Profile|Proxy|Country)|Test.*SIM.*PLMN' -count=1 -v`

Expected: PASS。

### Task 2: 执行同出口 IKE_SA_INIT 对照并按证据决定协议改动

**Files:**
- Test: `third_party/vowifi-go/engine/swu/socks5_udp_transport_live_test.go:70-219`
- Modify only if evidence requires: `third_party/vowifi-go/engine/swu/ikev2/sa.go`
- Test only if SA changes: `third_party/vowifi-go/engine/swu/ikev2/sa_test.go`

- [ ] **Step 1: 记录默认 proposal 下的 VOXI 基准**

Run: `$env:VOHIVE_LIVE_IKE_TARGET='epdg.epc.mnc015.mcc234.pub.3gppnetwork.org:4500'; Remove-Item Env:VOHIVE_LIVE_IKE_SUITE -ErrorAction SilentlyContinue; go test -tags live ./third_party/vowifi-go/engine/swu -run '^TestLiveSOCKS5UDPIKEInitToEPDG$' -count=1 -v`

Expected: 输出 SOCKS5 UDP relay、目标、Non-ESP marker、请求长度及 IKE 响应或超时。

- [ ] **Step 2: 在同一 SOCKS5 UDP 地址下执行 Lebara 默认 proposal 对照**

Run: `$env:VOHIVE_LIVE_IKE_TARGET='epdg.epc.mnc004.mcc204.pub.3gppnetwork.org:4500'; Remove-Item Env:VOHIVE_LIVE_IKE_SUITE -ErrorAction SilentlyContinue; go test -tags live ./third_party/vowifi-go/engine/swu -run '^TestLiveSOCKS5UDPIKEInitToEPDG$' -count=1 -v`

Expected: 记录与 Step 1 相同字段，不以单元测试替代该结果。

- [ ] **Step 3: 仅在 Lebara 默认 proposal 超时且 VOXI 有响应时测试 AES256/SHA256/PRF512/MODP2048**

Run: `$env:VOHIVE_LIVE_IKE_SUITE='aes256-sha256-prfsha512-modp2048'; go test -tags live ./third_party/vowifi-go/engine/swu -run '^TestLiveSOCKS5UDPIKEInitToEPDG$' -count=1 -v`

Expected: 若 Lebara 收到响应，保留输出作为 SA 变更依据；若仍 0 响应，停止协议改动并排查该代理出口到 Lebara ePDG 的 UDP 可达性。

- [ ] **Step 4: 只有 AES256 probe 成功时才添加最小默认 offer 与单元断言**

```go
// sa.go: append exactly the evidence-backed transform before the legacy fallbacks.
IKEProposal{Encryption: AES_CBC_256, Integrity: AUTH_HMAC_SHA2_256_128, PRF: PRF_HMAC_SHA2_512, DH: MODP_2048}
```

在 `sa_test.go` 断言该 transform 已包含，且不移除当前 VOXI 所需的 legacy transforms。

- [ ] **Step 5: 运行 SWU 回归并在实机重新验证两卡**

Run: `go test ./third_party/vowifi-go/engine/swu ./third_party/vowifi-go/engine/swu/ikev2 -count=1 -v`

Expected: PASS；随后按 VOXI→Lebara→VOXI 启动 WiFi Calling，日志必须显示实际 ePDG、proxy route、UDP relay 与 IKE 响应阶段。

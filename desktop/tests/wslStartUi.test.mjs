import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

test('desktop runtime service exposes explicit WSL startup command', () => {
  const source = readFileSync(new URL('../src/services/runtime.ts', import.meta.url), 'utf8')

  assert.match(source, /startWsl\(\)/)
  assert.match(source, /invoke<ActionResult>\('start_wsl'\)/)
})

test('desktop runtime service exposes explicit WSL stop command', () => {
  const source = readFileSync(new URL('../src/services/runtime.ts', import.meta.url), 'utf8')

  assert.match(source, /stopWsl\(\)/)
  assert.match(source, /invoke<ActionResult>\('stop_wsl'\)/)
})

test('desktop UI offers a start WSL action in the runtime panel', () => {
  const source = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')

  assert.match(source, /启动 WSL/)
  assert.match(source, /runtimeService\.startWsl/)
})

test('desktop UI offers a stop WSL action in the runtime panel', () => {
  const source = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')

  assert.match(source, /停止 WSL/)
  assert.match(source, /runtimeService\.stopWsl/)
})

test('desktop title displays the Tauri application version', () => {
  const source = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')

  assert.match(source, /getVersion/)
  assert.match(source, /VoHive Plus v\{\{\s*appVersion\s*\}\}/)
})

test('desktop runtime service exposes backend variant selection command', () => {
  const source = readFileSync(new URL('../src/services/runtime.ts', import.meta.url), 'utf8')

  assert.match(source, /setBackendVariant/)
  assert.match(source, /invoke<ActionResult>\('set_backend_variant'/)
})

test('desktop UI presents iniwex5 vohive as the backup runtime selection', () => {
  const source = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')

  assert.match(source, /selectedBackendVariant\.description/)
  assert.match(source, /切换前请先停止后端/)
  assert.match(source, /selected_backend_variant/)
  assert.match(source, /setBackendVariant/)
})

test('desktop UI presents VoCat as a managed runtime deployed to opt vocat', () => {
  const source = readFileSync(new URL('../src/App.vue', import.meta.url), 'utf8')

  assert.match(source, /三者共享 7575 端口/)
  assert.match(source, /VoCat 首次部署密码见诊断日志/)
  assert.doesNotMatch(source, /VoHive Plus 与 iniwex5\/vohive 部署到 \/opt\/vohive/)
})

test('WSL USB prepare script uses the VoHive Plus helper binary', () => {
  const source = readFileSync(
    new URL('../src-tauri/resources/vohive/vohive-usb-prepare.sh', import.meta.url),
    'utf8'
  )

  assert.match(source, /vohive-plus/)
  assert.doesNotMatch(source, /script_dir\/vohive"\s+--prepare-usb/)
})

import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

const [router, shell, service, page] = await Promise.all([
  readFile(new URL('../src/router/index.ts', import.meta.url), 'utf8'),
  readFile(new URL('../src/layouts/AuthenticatedShell.vue', import.meta.url), 'utf8'),
  readFile(new URL('../src/services/automatic-tasks.ts', import.meta.url), 'utf8'),
  readFile(new URL('../src/views/AutomaticTasks.vue', import.meta.url), 'utf8')
])

test('automatic tasks page is reachable from the authenticated sidebar', () => {
  assert.match(router, /path:\s*'\/automatic-tasks'/)
  assert.match(router, /AutomaticTasks\.vue/)
  assert.match(shell, /index:\s*'\/automatic-tasks',\s*label:\s*'自动任务'/)
})

test('automatic tasks service exposes CRUD, run-now, and history endpoints', () => {
  assert.match(service, /api\.get<[^>]+>\('\/automatic-tasks'\)/)
  assert.match(service, /api\.post<AutomaticTask>\('\/automatic-tasks'/)
  assert.match(service, /`\/automatic-tasks\/\$\{id\}`/)
  assert.match(service, /`\/automatic-tasks\/\$\{id\}\/actions\/run`/)
  assert.match(service, /'\/automatic-task-runs'/)
})

test('automatic tasks editor supports profile, environment, SMS, call, retry, and notifications', () => {
  for (const text of ['SIM / eSIM Profile', 'VoWiFi', '基站直连', '发送短信', '拨打电话并自动挂断', '失败重试次数', '完成后通知']) {
    assert.ok(page.includes(text), `missing automatic task field: ${text}`)
  }
  assert.match(page, /setInterval\([\s\S]*5000/)
})

test('automatic tasks editor ignores stale profile responses', () => {
  assert.match(page, /profileRequestSequence/)
  assert.match(page, /requestSequence !== profileRequestSequence \|\| form\.device_id !== deviceID/)
  assert.match(page, /watch\(editorOpen/)
})

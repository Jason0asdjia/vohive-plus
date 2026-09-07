import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(new URL('../scripts/sync-backend-resource.mjs', import.meta.url))

function runSync(env) {
  const targetDir =
    env.VOHIVE_TAURI_TARGET_DIR ||
    join(dirnameFromDestination(env.VOHIVE_BACKEND_DEST), 'src-tauri', 'target')
  return spawnSync(process.execPath, [script], {
    env: { ...process.env, VOHIVE_TAURI_TARGET_DIR: targetDir, ...env },
    encoding: 'utf8',
  })
}

function dirnameFromDestination(destination) {
  if (!destination) {
    return mkdtempSync(join(tmpdir(), 'vohive-sync-target-default-'))
  }
  const marker = `${join('src-tauri', 'resources', 'vohive')}`
  const index = destination.indexOf(marker)
  if (index >= 0) {
    return destination.slice(0, index)
  }
  return mkdtempSync(join(tmpdir(), 'vohive-sync-target-default-'))
}

test('sync backend resource copies built Linux runtime into desktop resources', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vohive-sync-resource-'))
  try {
    const source = join(dir, 'dist', 'vohive-open_linux_amd64')
    const destination = join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-open_linux_amd64')
    mkdirSync(join(dir, 'dist'), { recursive: true })
    writeFileSync(source, 'linux-runtime')

    const result = runSync({
      VOHIVE_BACKEND_SOURCE: source,
      VOHIVE_BACKEND_DEST: destination,
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(destination, 'utf8'), 'linux-runtime')
    assert.match(result.stdout, /Synced Linux backend resource/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync backend resource copies iniwex5 backup runtime from an explicit source when provided', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vohive-sync-orson-resource-'))
  try {
    const mainSource = join(dir, 'dist', 'vohive-open_linux_amd64')
    const source = join(dir, 'release', 'vohive_v1.5.5-10-gf9eb85d_linux_amd64')
    const mainDestination = join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-open_linux_amd64')
    const destination = join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-orson-v1.5.5_linux_amd64')
    mkdirSync(join(dir, 'dist'), { recursive: true })
    mkdirSync(join(dir, 'release'), { recursive: true })
    writeFileSync(mainSource, 'main-runtime')
    writeFileSync(source, 'orson-runtime')

    const result = runSync({
      VOHIVE_BACKEND_SOURCE: mainSource,
      VOHIVE_BACKEND_DEST: mainDestination,
      VOHIVE_ORSON_BACKEND_SOURCE: source,
      VOHIVE_ORSON_BACKEND_DEST: destination,
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(destination, 'utf8'), 'orson-runtime')
    assert.match(result.stdout, /Synced iniwex5\/vohive backup backend resource/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync backend resource defaults to the vendored iniwex5 backup runtime', () => {
  const source = readFileSync(script, 'utf8')

  assert.match(source, /'vendor'[\s\S]*'vohive-backends'/)
  assert.match(source, /iniwex5-vohive-v1\.5\.5-10-gf9eb85d_linux_amd64/)
  assert.doesNotMatch(source, /defaultOrsonDownloadUrl/)
})

test('sync backend resource refreshes built desktop target resources', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vohive-sync-target-resource-'))
  try {
    const source = join(dir, 'dist', 'vohive-open_linux_amd64')
    const primaryDestination = join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-open_linux_amd64')
    const targetDir = join(dir, 'desktop', 'src-tauri', 'target')
    const debugDestination = join(targetDir, 'debug', 'resources', 'vohive', 'vohive-open_linux_amd64')
    const releaseDestination = join(targetDir, 'release', 'resources', 'vohive', 'vohive-open_linux_amd64')
    mkdirSync(join(dir, 'dist'), { recursive: true })
    mkdirSync(join(targetDir, 'debug', 'resources', 'vohive'), { recursive: true })
    mkdirSync(join(targetDir, 'release', 'resources', 'vohive'), { recursive: true })
    writeFileSync(source, 'new-linux-runtime')
    writeFileSync(debugDestination, 'old-debug-runtime')
    writeFileSync(releaseDestination, 'old-release-runtime')

    const result = runSync({
      VOHIVE_BACKEND_SOURCE: source,
      VOHIVE_BACKEND_DEST: primaryDestination,
      VOHIVE_TAURI_TARGET_DIR: targetDir,
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(primaryDestination, 'utf8'), 'new-linux-runtime')
    assert.equal(readFileSync(debugDestination, 'utf8'), 'new-linux-runtime')
    assert.equal(readFileSync(releaseDestination, 'utf8'), 'new-linux-runtime')
    assert.match(result.stdout, /Synced Linux backend target resource/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync backend resource does not recreate missing debug target resources', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vohive-sync-no-debug-target-'))
  try {
    const source = join(dir, 'dist', 'vohive-open_linux_amd64')
    const primaryDestination = join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-open_linux_amd64')
    const targetDir = join(dir, 'desktop', 'src-tauri', 'target')
    const debugDestination = join(targetDir, 'debug', 'resources', 'vohive', 'vohive-open_linux_amd64')
    const releaseDestination = join(targetDir, 'release', 'resources', 'vohive', 'vohive-open_linux_amd64')
    mkdirSync(join(dir, 'dist'), { recursive: true })
    mkdirSync(join(targetDir, 'release', 'resources', 'vohive'), { recursive: true })
    writeFileSync(source, 'new-linux-runtime')
    writeFileSync(releaseDestination, 'old-release-runtime')

    const result = runSync({
      VOHIVE_BACKEND_SOURCE: source,
      VOHIVE_BACKEND_DEST: primaryDestination,
      VOHIVE_TAURI_TARGET_DIR: targetDir,
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(primaryDestination, 'utf8'), 'new-linux-runtime')
    assert.equal(readFileSync(releaseDestination, 'utf8'), 'new-linux-runtime')
    assert.equal(existsSync(debugDestination), false)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync backend resource accepts an existing copied runtime', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vohive-existing-resource-'))
  try {
    const source = join(dir, 'missing', 'vohive-open_linux_amd64')
    const destination = join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-open_linux_amd64')
    mkdirSync(join(dir, 'desktop', 'src-tauri', 'resources', 'vohive'), { recursive: true })
    writeFileSync(destination, 'existing-runtime')

    const result = runSync({
      VOHIVE_BACKEND_SOURCE: source,
      VOHIVE_BACKEND_DEST: destination,
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(destination, 'utf8'), 'existing-runtime')
    assert.match(result.stdout, /Synced Linux backend resource from existing/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync backend resource fails clearly when no runtime exists', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vohive-missing-resource-'))
  try {
    const result = runSync({
      VOHIVE_BACKEND_SOURCE: join(dir, 'dist', 'vohive-open_linux_amd64'),
      VOHIVE_BACKEND_DEST: join(dir, 'desktop', 'src-tauri', 'resources', 'vohive', 'vohive-open_linux_amd64'),
    })

    assert.equal(result.status, 1)
    assert.match(result.stderr, /Missing Linux backend resource/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

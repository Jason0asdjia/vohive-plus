import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawn, spawnSync } from 'node:child_process'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(new URL('../scripts/sync-vocat-resource.mjs', import.meta.url))

function runScript(env) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [script], {
      env: { ...process.env, ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk) => {
      stdout += chunk
    })
    child.stderr.on('data', (chunk) => {
      stderr += chunk
    })
    child.on('close', (status) => {
      resolve({ status, stdout, stderr })
    })
  })
}

test('sync vocat resource copies explicit local runtime and writes version marker', () => {
  const dir = mkdtempSync(join(tmpdir(), 'vocat-sync-resource-'))
  try {
    const source = join(dir, 'vocat-linux-amd64')
    const destination = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'vocat-linux-amd64')
    const version = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'VOCAT_VERSION')
    mkdirSync(dir, { recursive: true })
    writeFileSync(source, 'vocat-runtime')

    const result = spawnSync(process.execPath, [script], {
      env: {
        ...process.env,
        VOHIVE_VOCAT_SOURCE: source,
        VOHIVE_VOCAT_DEST: destination,
        VOHIVE_VOCAT_VERSION_DEST: version,
        VOHIVE_VOCAT_VERSION: 'v9.9.9',
      },
      encoding: 'utf8',
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(destination, 'utf8'), 'vocat-runtime')
    assert.equal(readFileSync(version, 'utf8'), 'v9.9.9')
    assert.match(result.stdout, /Synced VoCat runtime resource/)
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync vocat resource downloads latest runtime when no local source exists', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'vocat-sync-skip-'))
  const runtimeBody = 'vocat-runtime-from-release'
  const runtimeHash = 'f043334740fbffb665d0e498cf8c7740e76c73f876399d8fab0a02b4e8fdd19a'
  let baseUrl = ''
  const server = createServer((req, res) => {
    if (req.url === '/releases/latest') {
      res.setHeader('Content-Type', 'application/json')
      res.end(
        JSON.stringify({
          tag_name: 'v0.2.28',
          assets: [
            { name: 'vocat-linux-amd64', browser_download_url: `${baseUrl}/assets/vocat-linux-amd64` },
            { name: 'SHA256SUMS', browser_download_url: `${baseUrl}/assets/SHA256SUMS` },
          ],
        })
      )
      return
    }
    if (req.url === '/assets/vocat-linux-amd64') {
      res.end(runtimeBody)
      return
    }
    if (req.url === '/assets/SHA256SUMS') {
      res.end(`${runtimeHash}  vocat-linux-amd64\n`)
      return
    }
    if (req.url === '/LICENSE') {
      res.end('Vocat Research & Evaluation License\n')
      return
    }
    res.statusCode = 404
    res.end('not found')
  })
  try {
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
    const port = server.address().port
    baseUrl = `http://127.0.0.1:${port}`
    const destination = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'vocat-linux-amd64')
    const checksums = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'SHA256SUMS')
    const version = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'VOCAT_VERSION')
    const license = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'LICENSE')
    const result = await runScript({
      VOHIVE_VOCAT_SOURCE: join(dir, 'missing-vocat-linux-amd64'),
      VOHIVE_VOCAT_DEST: destination,
      VOHIVE_VOCAT_CHECKSUMS_DEST: checksums,
      VOHIVE_VOCAT_VERSION_DEST: version,
      VOHIVE_VOCAT_LICENSE_DEST: license,
      VOHIVE_VOCAT_RELEASE_API_URL: `${baseUrl}/releases/latest`,
      VOHIVE_VOCAT_LICENSE_URL: `${baseUrl}/LICENSE`,
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(readFileSync(destination, 'utf8'), runtimeBody)
    assert.equal(readFileSync(checksums, 'utf8'), `${runtimeHash}  vocat-linux-amd64\n`)
    assert.equal(readFileSync(version, 'utf8'), 'v0.2.28')
    assert.match(readFileSync(license, 'utf8'), /Research & Evaluation License/)
    assert.match(result.stdout, /Downloaded VoCat runtime resource/)
  } finally {
    server.closeAllConnections()
    await new Promise((resolve) => server.close(resolve))
    rmSync(dir, { recursive: true, force: true })
  }
})

test('sync vocat resource skips unavailable downloads without failing local builds', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'vocat-sync-unavailable-'))
  const server = createServer((_req, res) => {
    res.statusCode = 503
    res.end('temporarily unavailable')
  })
  try {
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
    const port = server.address().port
    const destination = join(dir, 'desktop', 'src-tauri', 'resources', 'vocat', 'vocat-linux-amd64')
    const result = await runScript({
      VOHIVE_VOCAT_SOURCE: join(dir, 'missing-vocat-linux-amd64'),
      VOHIVE_VOCAT_DEST: destination,
      VOHIVE_VOCAT_RELEASE_API_URL: `http://127.0.0.1:${port}/releases/latest`,
      VOHIVE_VOCAT_FETCH_ATTEMPTS: '1',
    })

    assert.equal(result.status, 0, result.stderr)
    assert.equal(existsSync(destination), false)
    assert.match(result.stderr, /Skipped VoCat runtime resource download/)
    assert.match(result.stderr, /VoCat runtime resource is not packaged/)
  } finally {
    server.closeAllConnections()
    await new Promise((resolve) => server.close(resolve))
    rmSync(dir, { recursive: true, force: true })
  }
})

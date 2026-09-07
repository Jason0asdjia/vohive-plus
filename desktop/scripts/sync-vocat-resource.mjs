import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const desktopDir = resolve(scriptDir, '..')
const resourcesDir = resolve(desktopDir, 'src-tauri', 'resources', 'vocat')

const source = process.env.VOHIVE_VOCAT_SOURCE
const destination = resolve(process.env.VOHIVE_VOCAT_DEST || resolve(resourcesDir, 'vocat-linux-amd64'))
const checksumsDestination = resolve(
  process.env.VOHIVE_VOCAT_CHECKSUMS_DEST || resolve(resourcesDir, 'SHA256SUMS')
)
const versionDestination = resolve(
  process.env.VOHIVE_VOCAT_VERSION_DEST || resolve(resourcesDir, 'VOCAT_VERSION')
)
const licenseDestination = resolve(
  process.env.VOHIVE_VOCAT_LICENSE_DEST || resolve(resourcesDir, 'LICENSE')
)
const releaseApiUrl =
  process.env.VOHIVE_VOCAT_RELEASE_API_URL ||
  'https://api.github.com/repos/MengMengCode/VoCat/releases/latest'
const licenseUrl =
  process.env.VOHIVE_VOCAT_LICENSE_URL ||
  'https://raw.githubusercontent.com/MengMengCode/VoCat/master/LICENSE'
const fetchAttempts = Number.parseInt(process.env.VOHIVE_VOCAT_FETCH_ATTEMPTS || '4', 10)

class DownloadUnavailableError extends Error {
  constructor(message) {
    super(message)
    this.name = 'DownloadUnavailableError'
  }
}

if (source && existsSync(source)) {
  mkdirSync(dirname(destination), { recursive: true })
  copyFileSync(source, destination)
  chmodSync(destination, 0o755)
  mkdirSync(dirname(versionDestination), { recursive: true })
  writeFileSync(versionDestination, process.env.VOHIVE_VOCAT_VERSION || 'local', 'ascii')
  console.log(`Synced VoCat runtime resource from ${source}`)
} else if (existsSync(destination)) {
  chmodSync(destination, 0o755)
  console.log(`Using existing VoCat runtime resource at ${destination}`)
} else {
  try {
    await downloadLatestVocat()
  } catch (err) {
    if (!(err instanceof DownloadUnavailableError)) {
      throw err
    }
    console.warn(`Skipped VoCat runtime resource download: ${err.message}`)
    console.warn(
      `VoCat runtime resource is not packaged. Put vocat-linux-amd64 at ${destination} ` +
        'or set VOHIVE_VOCAT_SOURCE to include it in a local build.'
    )
  }
}

async function downloadLatestVocat() {
  const release = await fetchJson(releaseApiUrl)
  const runtimeAsset = release.assets?.find((asset) => asset.name === 'vocat-linux-amd64')
  const checksumsAsset = release.assets?.find((asset) => asset.name === 'SHA256SUMS')
  if (!runtimeAsset) {
    throw new Error(`VoCat latest release ${release.tag_name || ''} is missing vocat-linux-amd64`)
  }
  if (!checksumsAsset) {
    throw new Error(`VoCat latest release ${release.tag_name || ''} is missing SHA256SUMS`)
  }

  mkdirSync(dirname(destination), { recursive: true })
  const runtime = await fetchBytes(resolveAssetUrl(runtimeAsset.browser_download_url))
  const checksums = await fetchText(resolveAssetUrl(checksumsAsset.browser_download_url))
  const expectedHash = expectedRuntimeHash(checksums)
  const actualHash = createHash('sha256').update(runtime).digest('hex')
  if (actualHash !== expectedHash.toLowerCase()) {
    throw new Error(`VoCat runtime SHA256 mismatch: expected ${expectedHash}, got ${actualHash}`)
  }

  writeFileSync(destination, runtime)
  chmodSync(destination, 0o755)
  writeFileSync(checksumsDestination, checksums, 'ascii')
  writeFileSync(versionDestination, release.tag_name || 'latest', 'ascii')
  writeFileSync(licenseDestination, await fetchText(licenseUrl), 'utf8')
  console.log(`Downloaded VoCat runtime resource ${release.tag_name || 'latest'} to ${destination}`)
}

async function fetchJson(url) {
  const res = await fetchWithRetry(url)
  return res.json()
}

async function fetchText(url) {
  const res = await fetchWithRetry(url)
  return res.text()
}

async function fetchBytes(url) {
  const res = await fetchWithRetry(url)
  return Buffer.from(await res.arrayBuffer())
}

async function fetchWithRetry(url) {
  const attempts = Number.isFinite(fetchAttempts) && fetchAttempts > 0 ? fetchAttempts : 4
  let lastError
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      const res = await fetch(url, { headers: { 'User-Agent': 'vohive-plus-desktop' } })
      if (res.ok) {
        return res
      }
      lastError = new Error(`HTTP ${res.status}`)
      if (res.status !== 429 && res.status < 500) {
        break
      }
    } catch (err) {
      lastError = err
    }
    if (attempt < attempts) {
      await sleep(1000 * attempt)
    }
  }
  const reason = lastError instanceof Error ? lastError.message : String(lastError)
  throw new DownloadUnavailableError(
    `Failed to download ${url}: ${reason}. ` +
      'Set VOHIVE_VOCAT_SOURCE to a local vocat-linux-amd64 file if GitHub is unreachable.'
  )
}

function resolveAssetUrl(url) {
  return new URL(url, releaseApiUrl).toString()
}

function expectedRuntimeHash(checksums) {
  for (const line of checksums.split(/\r?\n/)) {
    const parts = line.trim().split(/\s+/)
    if (parts.length >= 2 && parts[1].replace(/^\*/, '') === 'vocat-linux-amd64') {
      return parts[0]
    }
  }
  throw new Error('SHA256SUMS does not contain vocat-linux-amd64')
}

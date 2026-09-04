import { chmodSync, copyFileSync, existsSync, mkdirSync, rmSync } from 'node:fs'
import { createWriteStream } from 'node:fs'
import { get } from 'node:https'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const desktopDir = resolve(scriptDir, '..')
const repoRoot = resolve(desktopDir, '..')

const resourcesDir = resolve(desktopDir, 'src-tauri', 'resources', 'vohive')
const defaultOrsonDownloadUrl =
  'https://raw.githubusercontent.com/Orson-Yan/Vohive-155/main/release/vohive_v1.5.5-10-gf9eb85d_linux_amd64'
const orsonDownloadUrl = Object.hasOwn(process.env, 'VOHIVE_ORSON_BACKEND_URL')
  ? process.env.VOHIVE_ORSON_BACKEND_URL
  : defaultOrsonDownloadUrl
const mainRuntime = {
  label: 'Linux backend',
  missingLabel: 'Linux backend resource',
  required: true,
  source: resolve(
    process.env.VOHIVE_BACKEND_SOURCE || resolve(repoRoot, 'dist', 'vohive-open_linux_amd64')
  ),
  destination: resolve(
    process.env.VOHIVE_BACKEND_DEST || resolve(resourcesDir, 'vohive-open_linux_amd64')
  ),
}
const orsonRuntime = {
  label: 'Orson fallback backend',
  missingLabel: 'Orson fallback backend resource',
  required: false,
  downloadUrl: orsonDownloadUrl,
  source: resolve(
    process.env.VOHIVE_ORSON_BACKEND_SOURCE ||
      resolve(repoRoot, '.tmp', 'vohive_orson_linux_amd64')
  ),
  destination: resolve(
    process.env.VOHIVE_ORSON_BACKEND_DEST ||
      resolve(resourcesDir, 'vohive-orson-v1.5.5_linux_amd64')
  ),
}

await syncRuntime(mainRuntime)
await syncRuntime(orsonRuntime)

async function syncRuntime(runtime) {
  if (existsSync(runtime.source)) {
    mkdirSync(dirname(runtime.destination), { recursive: true })
    copyFileSync(runtime.source, runtime.destination)
    chmodSync(runtime.destination, 0o755)
    console.log(`Synced ${runtime.label} resource from ${runtime.source}`)
    return
  }
  if (existsSync(runtime.destination)) {
    console.log(`Using existing ${runtime.label} resource at ${runtime.destination}`)
    return
  }
  if (!runtime.required) {
    if (runtime.downloadUrl) {
      mkdirSync(dirname(runtime.destination), { recursive: true })
      try {
        await download(runtime.downloadUrl, runtime.destination)
        chmodSync(runtime.destination, 0o755)
        console.log(`Downloaded optional ${runtime.label} resource from ${runtime.downloadUrl}`)
      } catch (err) {
        rmSync(runtime.destination, { force: true })
        console.log(`Skipping optional ${runtime.label} resource; download failed: ${err.message}`)
      }
      return
    }
    console.log(`Skipping optional ${runtime.label} resource; source not found at ${runtime.source}`)
    return
  }
  console.error(
    [
      `Missing ${runtime.missingLabel} for desktop build.`,
      `Expected source: ${runtime.source}`,
      `Expected destination: ${runtime.destination}`,
      'Build the backend first, then rerun the desktop build.',
    ].join('\n')
  )
  process.exit(1)
}

function download(url, destination) {
  return new Promise((resolveDownload, rejectDownload) => {
    const file = createWriteStream(destination)
    get(url, (response) => {
      if (response.statusCode && response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
        file.close()
        download(response.headers.location, destination).then(resolveDownload, rejectDownload)
        return
      }
      if (response.statusCode !== 200) {
        file.close()
        rejectDownload(new Error(`download failed with status ${response.statusCode}`))
        return
      }
      response.pipe(file)
      file.on('finish', () => file.close(resolveDownload))
    }).on('error', (err) => {
      file.close()
      rejectDownload(err)
    })
  })
}

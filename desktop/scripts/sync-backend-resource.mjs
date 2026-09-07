import { chmodSync, copyFileSync, existsSync, mkdirSync } from 'node:fs'
import { basename, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const desktopDir = resolve(scriptDir, '..')
const repoRoot = resolve(desktopDir, '..')

const resourcesDir = resolve(desktopDir, 'src-tauri', 'resources', 'vohive')
const tauriTargetDir = resolve(
  process.env.VOHIVE_TAURI_TARGET_DIR || resolve(desktopDir, 'src-tauri', 'target')
)
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
  label: 'iniwex5/vohive backup backend',
  missingLabel: 'iniwex5/vohive backup backend resource',
  required: false,
  source: resolve(
    process.env.VOHIVE_ORSON_BACKEND_SOURCE ||
      resolve(
        desktopDir,
        'vendor',
        'vohive-backends',
        'iniwex5-vohive-v1.5.5-10-gf9eb85d_linux_amd64'
      )
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
    syncRuntimeFile(runtime, runtime.source, `from ${runtime.source}`)
    return
  }
  if (existsSync(runtime.destination)) {
    syncRuntimeFile(runtime, runtime.destination, `from existing ${runtime.destination}`)
    return
  }
  if (!runtime.required) {
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

function syncRuntimeFile(runtime, source, sourceLabel) {
  mkdirSync(dirname(runtime.destination), { recursive: true })
  copyFileSync(source, runtime.destination)
  chmodSync(runtime.destination, 0o755)
  console.log(`Synced ${runtime.label} resource ${sourceLabel}`)
  syncTargetResources(runtime, runtime.destination)
}

function syncTargetResources(runtime, source) {
  for (const mode of ['debug', 'release']) {
    const resourceDir = resolve(tauriTargetDir, mode, 'resources', 'vohive')
    if (!existsSync(resourceDir)) {
      console.log(`Skipping ${runtime.label} target resource for missing ${mode} target`)
      continue
    }
    const destination = resolve(resourceDir, basename(runtime.destination))
    copyFileSync(source, destination)
    chmodSync(destination, 0o755)
    console.log(`Synced ${runtime.label} target resource to ${destination}`)
  }
}

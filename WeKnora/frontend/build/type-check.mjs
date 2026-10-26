import { spawnSync } from 'node:child_process'
import { constants } from 'node:os'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const frontend = dirname(dirname(fileURLToPath(import.meta.url)))
const require = createRequire(import.meta.url)
const configs = ['tsconfig.node.json', 'tsconfig.app.json', 'tsconfig.tests.json']

// Separate processes release each TypeScript program before checking the next.
export function runTypeChecks(args = [], compiler = require.resolve('vue-tsc/bin/vue-tsc.js'), cwd = frontend) {
  for (const config of configs) {
    console.log(`Checking ${config}`)
    const result = spawnSync(process.execPath, [compiler, '--build', config, ...args], { cwd, stdio: 'inherit' })
    if (result.error) {
      console.error(result.error.message)
      return 1
    }
    if (result.status !== 0) return result.status ?? 128 + (constants.signals[result.signal] ?? 1)
  }
  return 0
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = runTypeChecks(process.argv.slice(2))
}

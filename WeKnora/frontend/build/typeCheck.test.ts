import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import test from 'node:test'
import ts from 'typescript'

const frontend = dirname(dirname(fileURLToPath(import.meta.url)))
const checker = process.env.TYPE_CHECK_TEST_MODULE || join(frontend, 'build/type-check.mjs')

function run(failure = '', args: string[] = []) {
  const directory = mkdtempSync(join(tmpdir(), 'weknora-type-check-'))
  try {
    const log = join(directory, 'events.jsonl')
    const compiler = join(directory, 'compiler.cjs')
    writeFileSync(compiler, `
      const fs=require('node:fs');
      fs.appendFileSync(process.env.TYPE_CHECK_TEST_LOG,JSON.stringify(process.argv.slice(2))+'\\n');
      const config=process.argv[3];
      if(config===process.env.TYPE_CHECK_TEST_FAIL)process.exit(17);
      if(config===process.env.TYPE_CHECK_TEST_SIGNAL)process.kill(process.pid,'SIGTERM');
    `)
    const entry = join(directory, 'entry.mjs')
    writeFileSync(entry, `import {runTypeChecks} from ${JSON.stringify(pathToFileURL(checker).href)};process.exitCode=runTypeChecks(${JSON.stringify(args)},${JSON.stringify(compiler)},${JSON.stringify(directory)})`)
    const result = spawnSync(process.execPath, [entry], {
      env: { ...process.env, TYPE_CHECK_TEST_LOG: log, TYPE_CHECK_TEST_FAIL: failure === 'signal' ? '' : failure,
        TYPE_CHECK_TEST_SIGNAL: failure === 'signal' ? 'tsconfig.app.json' : '' },
      encoding: 'utf8', timeout: 10000,
    })
    const events = readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line))
    return { result, events }
  } finally { rmSync(directory, { recursive: true, force: true }) }
}

test('type checking runs all three programs in independent processes and forwards compiler flags', () => {
  const { result, events } = run('', ['--force', '--pretty', 'false'])
  assert.equal(result.status, 0, result.stderr)
  assert.deepEqual(events, ['node', 'app', 'tests'].map(name => ['--build', `tsconfig.${name}.json`, '--force', '--pretty', 'false']))
})

test('a failed type program stops subsequent programs and preserves its exit status', () => {
  const { result, events } = run('tsconfig.app.json')
  assert.equal(result.status, 17)
  assert.deepEqual(events.map(args => args[1]), ['tsconfig.node.json', 'tsconfig.app.json'])
})

test('a terminated compiler cannot be treated as a successful check', () => {
  const { result, events } = run('signal')
  assert.equal(result.status, 143)
  assert.equal(events.length, 2)
})

test('the split retains all previously checked app and test roots and compiler safeguards', () => {
  const load = (filename: string, override?: Record<string, unknown>) => {
    const config = ts.readConfigFile(join(frontend, filename), ts.sys.readFile)
    assert.equal(config.error, undefined)
    const parsed = ts.parseJsonConfigFileContent({ ...config.config, ...override }, ts.sys, frontend, undefined, join(frontend, filename), undefined,
      [{ extension: '.vue', isMixedContent: true, scriptKind: ts.ScriptKind.Deferred }])
    assert.deepEqual(parsed.errors, [])
    return parsed
  }
  const original = load('tsconfig.app.json', { exclude: ['src/**/__tests__/*'] })
  const app = load('tsconfig.app.json')
  const tests = load('tsconfig.tests.json')
  assert.deepEqual([...new Set([...app.fileNames, ...tests.fileNames])].sort(), [...original.fileNames].sort())
  assert.ok(tests.fileNames.some(file => file.endsWith('.test.ts')))
  assert.ok(app.fileNames.every(file => !/\.test\.tsx?$/.test(file)))
  for (const program of [app, tests]) {
    const { tsBuildInfoFile: _buildInfo, configFilePath: _config, ...options } = program.options
    const { tsBuildInfoFile: _originalBuildInfo, configFilePath: _originalConfig, ...originalOptions } = original.options
    assert.deepEqual(options, originalOptions)
  }
})

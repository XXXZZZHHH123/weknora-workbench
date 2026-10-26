import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const frontend = dirname(dirname(fileURLToPath(import.meta.url)))
const runner = process.env.npm_execpath || join(dirname(process.execPath), '../lib/node_modules/npm/bin/npm-cli.js')
const script = JSON.parse(readFileSync(join(frontend, 'package.json'), 'utf8')).scripts['build-with-types']
const worker = `
const fs=require('node:fs'),path=require('node:path');
const task=process.argv[2], log=process.env.SERIAL_BUILD_TEST_LOG;
const lock=path.join(process.env.SERIAL_BUILD_TEST_DIR,'active');
const record=value=>fs.appendFileSync(log,JSON.stringify(value)+'\\n');
let owned=false;
try {fs.closeSync(fs.openSync(lock,'wx'));owned=true} catch(error) {if(error.code!=='EEXIST')throw error;record({overlap:task})}
record({task,event:'start',args:process.argv.slice(3)});
setTimeout(()=>{record({task,event:'end'});if(owned)fs.unlinkSync(lock);process.exit(process.env.SERIAL_BUILD_TEST_FAIL===task?17:0)},task==='type'?500:40);
`

function run(failure = '', args: string[] = []) {
  const directory = mkdtempSync(join(tmpdir(), 'weknora-serial-build-'))
  try {
    const log = join(directory, 'events.jsonl')
    writeFileSync(join(directory, 'worker.cjs'), worker)
    writeFileSync(join(directory, 'package.json'), JSON.stringify({
      name: 'isolated-serial-build-test', private: true,
      scripts: { 'type-check': 'node worker.cjs type', 'build-only': 'node worker.cjs build', 'build-with-types': script },
    }))
    const result = spawnSync(process.execPath, [runner, 'run', 'build-with-types', '--', ...args], {
      cwd: directory,
      env: { ...process.env, PATH: [join(frontend, 'node_modules/.bin'), dirname(process.execPath), process.env.PATH].join(delimiter),
        SERIAL_BUILD_TEST_LOG: log, SERIAL_BUILD_TEST_DIR: directory, SERIAL_BUILD_TEST_FAIL: failure },
      encoding: 'utf8', timeout: 15000,
    })
    const events = readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line))
    return { result, events }
  } finally { rmSync(directory, { recursive: true, force: true }) }
}

test('the combined frontend command checks types before building and forwards build arguments', () => {
  const { result, events } = run('', ['--outDir', 'folder with spaces'])
  assert.equal(result.status, 0, result.stderr)
  assert.deepEqual(events.map(value => [value.task, value.event]), [['type', 'start'], ['type', 'end'], ['build', 'start'], ['build', 'end']])
  assert.deepEqual(events.find(value => value.task === 'type' && value.event === 'start').args, [])
  assert.deepEqual(events.find(value => value.task === 'build' && value.event === 'start').args, ['--outDir', 'folder with spaces'])
})

test('a type error stops the combined command before any bundler starts', () => {
  const { result, events } = run('type')
  assert.notEqual(result.status, 0)
  assert.ok(events.every(value => value.task === 'type'))
  assert.deepEqual(events.map(value => value.event), ['start', 'end'])
})

test('a bundler failure is propagated after a successful type check', () => {
  const { result, events } = run('build')
  assert.notEqual(result.status, 0)
  assert.deepEqual(events.map(value => [value.task, value.event]), [['type', 'start'], ['type', 'end'], ['build', 'start'], ['build', 'end']])
})

import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { nextTick, ref } from 'vue'
import ts from 'typescript'

const source = readFileSync(new URL('./ResearchWorkbenchGuide.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(`${script}\n;({ active, finish, dismiss })`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText

function fixture() {
  const props={steps:[],saving:false,saveError:''}
  let globalDone = false, otherGuide = false
  const mounted: Array<() => void> = [], unmounted: Array<() => void> = [], events: string[] = []
  const timers = new Map<number, () => void>()
  let timerId = 0
  const view = runInNewContext(code, { nextTick, ref, defineProps: () => props, defineEmits: () => (event: string) => events.push(event),
    onMounted: (fn: () => void) => mounted.push(fn), onBeforeUnmount: (fn: () => void) => unmounted.push(fn),
    isGlobalUserGuideDone: () => globalDone, document: { querySelector: () => otherGuide ? {} : null },
    setTimeout: (fn: () => void) => { timers.set(++timerId, fn); return timerId }, clearTimeout: (id: number) => timers.delete(id),
  })
  return { view, props, events, timers, mount: () => mounted.forEach(fn => fn()), unmount: () => unmounted.forEach(fn => fn()),
    globalDone: () => { globalDone = true }, otherGuide: (active: boolean) => { otherGuide = active },
    advance() { const pending = [...timers.values()]; timers.clear(); pending.forEach(fn => fn()) } }
}

test('contextual guide waits for the native welcome guide and avoids stacking overlays', async () => {
  const f = fixture()
  f.mount(); await nextTick()
  assert.equal(f.view.active.value, false)
  assert.equal(f.timers.size, 1)
  f.globalDone(); f.otherGuide(true); f.advance(); await nextTick()
  assert.equal(f.view.active.value, false)
  f.otherGuide(false); f.advance(); await nextTick()
  assert.equal(f.view.active.value, true)
  f.unmount()
})

test('dismiss and finish are separate outcomes despite native paired events', () => {
  const skipped = fixture()
  skipped.view.dismiss(); skipped.view.finish()
  assert.deepEqual(skipped.events, ['dismiss'])
  const completed = fixture()
  completed.view.finish()
  assert.deepEqual(completed.events, ['finish'])
})

test('leaving while the native guide is active cancels pending contextual work', async () => {
  const f = fixture()
  f.mount()
  assert.equal(f.timers.size, 1)
  f.unmount(); f.globalDone(); f.advance(); await nextTick()
  assert.equal(f.timers.size, 0)
  assert.equal(f.view.active.value, false)
})

test('failed skip can be retried as completion without retaining a sticky dismissal', () => {
  const f=fixture()
  f.view.dismiss();f.view.finish()
  f.props.saving=true
  f.view.dismiss();f.view.finish()
  assert.deepEqual(f.events,['dismiss'])
  f.props.saving=false;f.props.saveError='Unavailable'
  f.view.finish()
  assert.deepEqual(f.events,['dismiss','finish'])
})

test('failed completion can be retried as skip and does not close the active tour', async () => {
  const f=fixture()
  f.globalDone();f.mount();await nextTick()
  f.view.finish()
  f.props.saveError='Unavailable'
  f.view.dismiss();f.view.finish()
  assert.deepEqual(f.events,['finish','dismiss'])
  assert.equal(f.view.active.value,true)
  f.unmount()
})

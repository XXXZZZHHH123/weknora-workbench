import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { computed, nextTick, ref } from 'vue'
import ts from 'typescript'
import { followUntilStable } from './spotlightFrame'

const source = readFileSync(new URL('./SpotlightGuide.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1]!.split('</script>')[0]!.replace(/^import .*$/gm, '')
const code = ts.transpileModule(`${script}\n;({ stepTitle, stepDesc, finish, dismiss })`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText

function labels(step: Record<string, string>,options:Record<string,unknown>={}) {
  const events:unknown[][]=[]
  const view=runInNewContext(code, { computed, nextTick, ref, defineProps: () => ({ active: false, steps: [step], stepI18nPrefix: 'existing.steps',...options }),
    withDefaults: (props: object,defaults:object) => ({...defaults,...props}), defineEmits: () => (...args:unknown[]) => events.push(args), defineExpose() {},
    useI18n: () => ({ t: (key: string) => `translated:${key}` }), watch() {}, onBeforeUnmount() {},
    window: { innerWidth: 1200, innerHeight: 800 },
  })
  return {...view,events}
}

test('contextual labels can safely name the current bound resource', () => {
  const view = labels({ key: 'scope', title: 'Current library', description: 'Verified preparation status' })
  assert.equal(view.stepTitle.value, 'Current library')
  assert.equal(view.stepDesc.value, 'Verified preparation status')
})

test('existing native guides keep their translated labels', () => {
  const view = labels({ key: 'welcome' })
  assert.equal(view.stepTitle.value, 'translated:existing.steps.welcome.title')
  assert.equal(view.stepDesc.value, 'translated:existing.steps.welcome.desc')
})

test('controlled guide completion stays open until its owner saves the outcome', () => {
  const view=labels({key:'done'},{closeOnFinish:false})
  view.finish()
  assert.deepEqual(view.events,[['finish']])
})

test('saving disables duplicate dismiss and completion attempts', () => {
  const view=labels({key:'done'},{busy:true,closeOnFinish:false})
  view.dismiss();view.finish()
  assert.deepEqual(view.events,[])
})

test('native guide default completion still closes immediately', () => {
  const view=labels({key:'done'})
  view.finish()
  assert.deepEqual(view.events,[['finish'],['update:active',false]])
})

const trackingCode = ts.transpileModule(`${script}\n;({ locate, targetRect, onMotion })`, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
}).outputText
function drawer(lefts: number[]) {
  let frame = 0
  const queue: (() => void)[] = []
  // Tagged like a real element so Vue leaves it unproxied, as it does with DOM nodes.
  const element = { [Symbol.toStringTag]: 'HTMLDivElement', isConnected: true, ownerDocument: { defaultView: null }, previousElementSibling: null, nextElementSibling: null,
    scrollIntoView() {}, getBoundingClientRect: () => { const left = lefts[Math.min(frame, lefts.length - 1)]!
      return { left, top: 230, width: 600, height: 280, right: left + 600, bottom: 510, x: left, y: 230 } } }
  const view = runInNewContext(trackingCode, { computed, nextTick, ref, setTimeout, clearTimeout, followUntilStable,
    defineProps: () => ({ active: true, steps: [{ key: 'generate', target: '#dg-generator' }] }),
    withDefaults: (props: object, defaults: object) => ({ ...defaults, ...props }), defineEmits: () => () => {}, defineExpose() {},
    useI18n: () => ({ t: (key: string) => key }), watch() {}, onBeforeUnmount() {}, getComputedStyle: () => ({ marginBottom: '0px' }),
    document: { querySelector: () => element }, spotlightDocument: (_frame: unknown, fallback: unknown) => fallback,
    spotlightRect: (el: typeof element) => el.getBoundingClientRect(),
    window: { innerWidth: 1200, innerHeight: 800, requestAnimationFrame: (cb: () => void) => queue.push(() => { frame++; cb() }), cancelAnimationFrame() {} },
  })
  return { view, flush() { while (queue.length) queue.shift()!() }, slide(next: number[]) { lefts.splice(0, lefts.length, ...next); frame = 0 } }
}

test('the spotlight settles on a drawer that is still sliding in when first measured', async () => {
  const guide = drawer([560, 420, 260, 140, 125, 125])
  await guide.view.locate(0, true)
  assert.equal(guide.view.targetRect.value.left, 560)
  guide.flush()
  assert.equal(guide.view.targetRect.value.left, 125)
})

test('a drawer that starts sliding after its request finishes is followed once the animation runs', async () => {
  const guide = drawer([1180])
  await guide.view.locate(0, true)
  guide.flush()
  assert.equal(guide.view.targetRect.value.left, 1180)
  guide.slide([900, 400, 125, 125])
  guide.view.onMotion({ target: null })
  guide.flush()
  assert.equal(guide.view.targetRect.value.left, 125)
})

import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import { computed, nextTick, ref } from 'vue'
import ts from 'typescript'

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

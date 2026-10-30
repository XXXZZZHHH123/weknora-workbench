import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import test from 'node:test'
import ts from 'typescript'
import { computed, reactive, ref } from 'vue'

const source = readFileSync(new URL('./Input-field.vue', import.meta.url), 'utf8')
const script = source.split('<script setup lang="ts">')[1].split('</script>')[0]
const ast = ts.createSourceFile('composer.ts', script, ts.ScriptTarget.Latest, true)
const names = new Set([
  'selectedModelId', 'selectedModel', 'handleModelChange', 'EMBEDDED_AUTO_MODEL',
  'embeddedPickedModel', 'embeddedDefaultModel', 'followsDefaultModel', 'composerModel', 'composerModelId',
])
const selection = ast.statements.filter(statement => ts.isVariableStatement(statement)
  && statement.declarationList.declarations.some(declaration => names.has(declaration.name.getText(ast))))
const compiled = ts.transpileModule(selection.map(statement => statement.getText(ast)).join('\n'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022 },
}).outputText

function fixture(scoped: boolean) {
  const props = reactive({ embeddedMode: true, knowledgeScope: scoped ? { kbId: 'research-kb' } : undefined,
    modelId: 'host-model', defaultModelId: 'default-model' })
  const settingsStore = reactive({ conversationModels: { selectedChatModelId: 'main-model' },
    updateConversationModels() { throw new Error('an embedded picker must not update main-chat settings') } })
  const emitted: unknown[][] = []
  const output: any = {}
  runInNewContext(compiled + '\nObject.assign(output, { composerModelId, followsDefaultModel, handleModelChange });', {
    props, settingsStore, computed, scopedModelId: ref('research-model'), showModelSelector: ref(true),
    availableModels: ref(['main-model', 'host-model', 'default-model', 'research-model', 'next-model'].map(id => ({ id }))),
    emit: (...args: unknown[]) => emitted.push(args), output,
    writeLastChatModelID() { throw new Error('an embedded picker must not overwrite the browser model preference') },
  })
  return { props, settingsStore, emitted, ...output }
}

test('research scope displays and sends its own chosen model while embedded', () => {
  const f = fixture(true)
  assert.equal(f.composerModelId.value, 'research-model')
  assert.equal(f.followsDefaultModel.value, false)
  f.handleModelChange('next-model')
  assert.equal(f.composerModelId.value, 'next-model')
  assert.equal(f.settingsStore.conversationModels.selectedChatModelId, 'main-model')
  assert.deepEqual(f.emitted, [])
})

test('official assistant model selection follows the host and preserves main-chat settings', () => {
  const f = fixture(false)
  assert.equal(f.composerModelId.value, 'host-model')
  f.handleModelChange('next-model')
  assert.deepEqual(f.emitted, [['update:modelId', 'next-model']])
  f.props.modelId = 'next-model'
  assert.equal(f.composerModelId.value, 'next-model')
  f.handleModelChange('__auto__')
  f.props.modelId = ''
  assert.equal(f.composerModelId.value, 'default-model')
  assert.equal(f.followsDefaultModel.value, true)
  assert.equal(f.settingsStore.conversationModels.selectedChatModelId, 'main-model')
})

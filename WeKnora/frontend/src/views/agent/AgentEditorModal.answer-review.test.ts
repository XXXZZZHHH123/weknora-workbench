import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'
import ts from 'typescript'
import { serializeAgentPrompts } from '../../utils/agentPromptTemplates'

const source = readFileSync(new URL('./AgentEditorModal.vue', import.meta.url), 'utf8')
const defaultsStart = source.indexOf('const defaultFormData = {')
const defaultsEnd = source.indexOf('const formData = ref(', defaultsStart)
const hydrate = source.match(/agentData\.config = \{ \.\.\.defaultFormData\.config, \.\.\.agentData\.config \};\s*\/\/[^\n]*\n\s*agentData\.config\.answer_review_mode = [^;]+;/)?.[0]
const save = source.match(/const payload = \{ \.\.\.formData\.value, config: serializeAgentPrompts\([^\n]+;/)?.[0]
assert.ok(defaultsStart >= 0 && defaultsEnd > defaultsStart && hydrate && save)

function editorPayload(config?: Record<string, unknown>, selected?: 'standard' | 'strict') {
  const input = structuredClone(config ?? {})
  const context: Record<string, any> = {
    props: { agent: { config: input } },
    agentData: { config: { ...input } },
    serializeAgentPrompts,
    promptTemplates: { value: null },
    allowedToolsToSave: () => input.allowed_tools ?? [],
  }
  const code = `${source.slice(defaultsStart, defaultsEnd)}
    ${config ? hydrate : 'agentData = JSON.parse(JSON.stringify(defaultFormData));'}
    const formData = { value: agentData };
    ${selected ? `formData.value.config.answer_review_mode = '${selected}';` : ''}
    ${save}
    globalThis.result = payload;`
  runInNewContext(ts.transpileModule(code, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, context)
  assert.deepEqual(input, config ?? {}, 'editing and saving must not mutate the supplied agent')
  return JSON.parse(JSON.stringify(context.result))
}

test('new agents explicitly choose practical answer review', () => {
  assert.equal(editorPayload().config.answer_review_mode, 'standard')
})

test('legacy review defaults and explicit saved selections survive ordinary edits', () => {
  for (const mode of [undefined, '', 'standard', 'strict']) {
    const input = { model_id: 'chosen-model', knowledge_bases: ['chosen-library'], allowed_tools: ['read_document'],
      ...(mode === undefined ? {} : { answer_review_mode: mode }) }
    const output = editorPayload(input).config
    assert.equal(output.answer_review_mode, mode ?? '')
    assert.equal(output.model_id, input.model_id)
    assert.deepEqual(output.knowledge_bases, input.knowledge_bases)
    assert.deepEqual(output.allowed_tools, input.allowed_tools)
  }
})

test('changing answer review saves either explicit mode without altering source scope', () => {
  for (const mode of ['standard', 'strict'] as const) {
    const output = editorPayload({ answer_review_mode: '', model_id: 'chosen-model', knowledge_bases: ['chosen-library'] }, mode).config
    assert.equal(output.answer_review_mode, mode)
    assert.equal(output.model_id, 'chosen-model')
    assert.deepEqual(output.knowledge_bases, ['chosen-library'])
  }
})

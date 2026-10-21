import assert from 'node:assert/strict'
import test from 'node:test'
import { generatedImageDelivery, loadGeneratedImage } from './imageGenerationResult'
import { filterModelsByType } from '../components/modelSelectorFilter'
import type { ModelConfig } from '../api/model'

const input = {
  session_id: 'session-1', message_id: 'message-1',
  artifacts: [{ index: 3, file_name: 'new-image.png', file_type: '.png', file_size: 4 }],
}

test('image loading uses the persisted message artifact identity and preserves its index', async () => {
  const delivery = generatedImageDelivery({ ...input, url: 'https://vendor.invalid/temporary.png' })!
  let args: unknown[] = []
  const image = new Blob(['png'], { type: 'image/png' })
  const result = await loadGeneratedImage(delivery, delivery.artifacts[0]!, async (...values) => { args = values; return image })
  assert.equal(result, image)
  assert.deepEqual(args, ['session-1', 'message-1', 3])
})

test('incomplete or invalid message identities cannot load arbitrary paths or provider URLs', () => {
  for (const data of [
    { image_url: 'https://vendor.invalid/image.png' },
    { ...input, session_id: 'session/../../other' },
    { ...input, message_id: 'message?other=1' },
    { ...input, message_id: '%2e%2e' },
    { ...input, artifacts: [{ index: -1, file_name: 'a.png' }] },
    { ...input, artifacts: [] },
  ]) assert.equal(generatedImageDelivery(data), null)
})

test('error bodies and invalid image payloads cannot render as generated images', async () => {
  const delivery = generatedImageDelivery(input)!
  for (const blob of [new Blob(), new Blob(['access denied'], { type: 'text/html' }), new Blob(['{}'], { type: 'application/json' })]) {
    await assert.rejects(loadGeneratedImage(delivery, delivery.artifacts[0]!, async () => blob), /Invalid saved image/)
  }
  await assert.rejects(loadGeneratedImage(delivery, delivery.artifacts[0]!, async () => { throw new Error('404') }), /404/)
})

test('image generation models stay out of chat and vision selectors', () => {
  const image = { id: 'generation', type: 'ImageGeneration', name: 'configured-model', source: 'remote', parameters: { supports_vision: true } } as ModelConfig
  const chat = { ...image, id: 'chat', type: 'KnowledgeQA' } as ModelConfig
  assert.deepEqual(filterModelsByType([image, chat], 'ImageGeneration').map(m => m.id), ['generation'])
  assert.deepEqual(filterModelsByType([image, chat], 'KnowledgeQA').map(m => m.id), ['chat'])
  assert.deepEqual(filterModelsByType([image, chat], 'VLLM').map(m => m.id), ['chat'])
})

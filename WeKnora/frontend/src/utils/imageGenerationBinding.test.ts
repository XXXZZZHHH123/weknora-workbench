import assert from 'node:assert/strict'
import test from 'node:test'
import { imageGenerationTools } from './imageGenerationBinding.ts'

test('selecting an image model adds its tool without dropping existing tools', () => {
  const before = ['read_document', 'web_search']
  assert.deepEqual(imageGenerationTools(before, true), [...before, 'generate_image'])
  assert.deepEqual(before, ['read_document', 'web_search'])
})

test('disabling or clearing removes only image generation and remains disabled on save', () => {
  const tools = imageGenerationTools(['generate_image', 'read_document'], false)
  assert.deepEqual(tools, ['read_document'])
  assert.deepEqual(imageGenerationTools(tools, false), tools)
})

test('enabling an inherited list preserves defaults, but an explicit empty list stays empty', () => {
  const defaults = ['search_conversations', 'read_document']
  assert.deepEqual(imageGenerationTools(null, true, defaults), [...defaults, 'generate_image'])
  assert.deepEqual(imageGenerationTools([], true, defaults), ['generate_image'])
})

test('changing models retains a single image tool entry', () => {
  assert.deepEqual(imageGenerationTools(['generate_image', 'read_document', 'generate_image'], true), ['read_document', 'generate_image'])
})

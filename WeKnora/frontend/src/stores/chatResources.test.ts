import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { runInNewContext } from 'node:vm'
import { createPinia } from 'pinia'
import ts from 'typescript'

const require = createRequire(import.meta.url)
const compiled = ts.transpileModule(readFileSync(new URL('./chatResources.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

function fixture(options: { sharedFailure?: boolean } = {}) {
  const requests: ReturnType<typeof deferred<{ data: any[] }>>[] = []
  const detailRequests: ReturnType<typeof deferred<{ data: any }>>[] = []
  const modelRequests: ReturnType<typeof deferred<any[]>>[] = []
  let sharedCalls = 0
  const exports: any = {}
  runInNewContext(compiled, {
    exports,
    require(name: string) {
      if (name === '@/api/knowledge-base') return { getKnowledgeBaseById: () => {
        const request = deferred<{ data: any }>()
        detailRequests.push(request)
        return request.promise
      }, listKnowledgeBases: () => {
        const request = deferred<{ data: any[] }>()
        requests.push(request)
        return request.promise
      } }
      if (name === '@/stores/organization') return { useOrganizationStore: () => ({ fetchSharedKnowledgeBases: async () => {
        sharedCalls++
        if (options.sharedFailure) throw new Error('shared catalog unavailable')
      } }) }
      if (name === '@/api/model') return { listModels: () => {
        const request = deferred<any[]>()
        modelRequests.push(request)
        return request.promise
      } }
      if (name === '@/utils/request') return { getCurrentLanguage: () => 'zh-CN' }
      if (name.startsWith('@/api/')) return {}
      return require(name)
    },
  })
  return { store: exports.useChatResourcesStore(createPinia()), requests, detailRequests, modelRequests, sharedCalls: () => sharedCalls }
}

test('a knowledge-base response after tenant invalidation cannot repopulate the list or loaded snapshot', async () => {
  const f = fixture()
  const pending = f.store.fetchKnowledgeBasesForList()
  f.store.invalidate()
  f.requests[0].resolve({ data: [{ id: 'previous-tenant-library', name: '旧库' }] })
  assert.equal((await pending).length, 0)
  assert.equal(f.store.rawKnowledgeBases.length, 0)
  assert.equal(f.store.isLoaded('knowledgeBases'), false)
  assert.equal(f.sharedCalls(), 1, 'main refreshes the shared catalog alongside the own list')
  f.store.$dispose()
})

test('forced knowledge-base refresh waits for a post-write request before completing', async () => {
  const f = fixture()
  const previous = f.store.fetchKnowledgeBasesForList()
  const current = f.store.fetchKnowledgeBasesForList(undefined, true)
  assert.equal(f.requests.length, 1, 'main queues a forced refresh after the current request')
  f.requests[0].resolve({ data: [{ id: 'outdated-library', name: '旧库' }] })
  await previous
  assert.equal(f.requests.length, 2)
  f.requests[1].resolve({ data: [{ id: 'current-library', name: '新库' }] })
  assert.equal((await current)[0].id, 'current-library')
  assert.equal(f.store.rawKnowledgeBases[0].id, 'current-library')
  assert.equal(f.sharedCalls(), 2)
  f.store.$dispose()
})

test('knowledge-base-only invalidation also retires pending list requests', async () => {
  const f = fixture()
  const previous = f.store.fetchKnowledgeBasesForList()
  f.store.invalidate('knowledgeBases')
  const current = f.store.fetchKnowledgeBasesForList()
  f.requests[0].resolve({ data: [{ id: 'outdated-library', name: '旧库' }] })
  await previous
  assert.equal(f.store.rawKnowledgeBases.length, 0)
  const reused = f.store.fetchKnowledgeBasesForList()
  assert.equal(f.requests.length, 2)
  f.requests[1].resolve({ data: [{ id: 'current-library', name: '新库' }] })
  await Promise.all([current, reused])
  assert.equal(f.store.rawKnowledgeBases[0].id, 'current-library')
  f.store.$dispose()
})

test('concurrent readers share one request and later ensure fetches a new snapshot without a TTL', async () => {
  const f = fixture()
  const one = f.store.ensureKnowledgeBases()
  const two = f.store.fetchKnowledgeBasesForList()
  assert.equal(f.requests.length, 1)
  f.requests[0].resolve({ data: [{ id: 'empty-library', name: '尚未配置模型的库' }] })
  await Promise.all([one, two])
  assert.equal(f.store.rawKnowledgeBases.length, 1)
  assert.equal(f.store.validKnowledgeBases.length, 0)
  const refresh = f.store.ensureKnowledgeBases()
  assert.equal(f.requests.length, 2, 'subsequent reads use the main no-TTL contract')
  f.requests[1].resolve({ data: [{ id: 'updated-library' }] })
  await refresh
  assert.equal(f.store.rawKnowledgeBases[0].id, 'updated-library')
  f.store.$dispose()
})

test('explicit source-only knowledge bases remain selectable without indexing models', async () => {
  const f = fixture()
  const pending = f.store.ensureKnowledgeBases()
  f.requests[0].resolve({ data: [
    { id: 'source-container', indexing_strategy: { disabled: true } },
    { id: 'unconfigured-document' },
    { id: 'contradictory-strategy', indexing_strategy: { disabled: true, vector_enabled: true } },
    { id: 'configured-document', summary_model_id: 'summary', embedding_model_id: 'embedding' },
  ] })
  await pending
  assert.deepEqual(Array.from(f.store.validKnowledgeBases, (kb: any) => kb.id), ['source-container', 'configured-document'])
  f.store.$dispose()
})

test('a detail request retired by identity invalidation returns no previous knowledge base', async () => {
  const f = fixture()
  const previous = f.store.fetchKnowledgeBaseById('library')
  f.store.invalidate()
  f.detailRequests[0].resolve({ data: { id: 'library', name: '旧空间详情' } })
  assert.equal(await previous, null)
  const current = f.store.fetchKnowledgeBaseById('library')
  assert.equal(f.detailRequests.length, 2)
  f.detailRequests[1].resolve({ data: { id: 'library', name: '当前详情' } })
  assert.equal((await current).name, '当前详情')
  f.store.$dispose()
})

test('a retired detail request cannot remove a newer request or overwrite its cache', async () => {
  const f = fixture()
  const previous = f.store.fetchKnowledgeBaseById('library')
  f.store.invalidateKnowledgeBaseDetail('library')
  const current = f.store.fetchKnowledgeBaseById('library')
  f.detailRequests[0].resolve({ data: { id: 'library', name: '旧详情' } })
  assert.equal(await previous, null)
  const reused = f.store.fetchKnowledgeBaseById('library')
  assert.equal(f.detailRequests.length, 2)
  f.detailRequests[1].resolve({ data: { id: 'library', name: '新详情' } })
  assert.equal((await current).name, '新详情')
  assert.equal((await reused).name, '新详情')
  assert.equal((await f.store.fetchKnowledgeBaseById('library')).name, '新详情')
  f.store.$dispose()
})

test('an agent knowledge-base request invalidated during an identity switch cannot refill its cache', async () => {
  const f = fixture()
  const previous = f.store.ensureAgentKnowledgeBases('agent')
  f.store.invalidate('knowledgeBases')
  f.requests[0].resolve({ data: [{ id: 'previous-library' }] })
  assert.equal((await previous).length, 0)
  const current = f.store.ensureAgentKnowledgeBases('agent')
  assert.equal(f.requests.length, 2)
  f.requests[1].resolve({ data: [{ id: 'current-library' }] })
  assert.equal((await current)[0].id, 'current-library')
  f.store.$dispose()
})


test('a shared catalog failure does not discard the successful own knowledge-base list', async () => {
  const f = fixture({ sharedFailure: true })
  const pending = f.store.fetchKnowledgeBasesForList()
  f.requests[0].resolve({ data: [{ id: 'own-library' }] })
  assert.equal((await pending)[0].id, 'own-library')
  assert.equal(f.store.rawKnowledgeBases[0].id, 'own-library')
  assert.equal(f.store.isLoaded('knowledgeBases'), true)
  f.store.$dispose()
})

test('filtered creator lists also suppress responses from a retired identity', async () => {
  const f = fixture()
  const pending = f.store.fetchKnowledgeBasesForList({ creator: 'mine' })
  f.store.invalidate()
  f.requests[0].resolve({ data: [{ id: 'previous-owner-library' }] })
  assert.equal((await pending).length, 0)
  f.store.$dispose()
})

test('replacing models keeps the current snapshot when a previous read finishes afterwards', async () => {
  const f = fixture()
  const pending = f.store.ensureModels()
  f.store.replaceModels([{ id: 'saved-model', type: 'KnowledgeQA' }])
  f.modelRequests[0].resolve([{ id: 'old-model', type: 'KnowledgeQA' }])
  await pending
  assert.equal(f.store.allModels[0].id, 'saved-model')
  assert.equal(f.store.isLoaded('models'), true)
  f.store.$dispose()
})

test('identity invalidation retires model snapshots as well as knowledge-base snapshots', async () => {
  const f = fixture()
  const pending = f.store.ensureModels()
  f.store.invalidate()
  f.modelRequests[0].resolve([{ id: 'previous-tenant-model', type: 'KnowledgeQA' }])
  await pending
  assert.equal(f.store.allModels.length, 0)
  assert.equal(f.store.isLoaded('models'), false)
  f.store.$dispose()
})

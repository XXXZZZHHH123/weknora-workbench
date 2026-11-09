import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { reactive } from 'vue'
import { fixture, flush } from '../../test-utils/vueFixture'
import type { PDFProcessingSettings } from '../../api/pdf-processing'

const initial: PDFProcessingSettings = { overlap_pages: 1, grouping_request_bytes: 24000, queue_timeout_seconds: 86400, execution_timeout_seconds: 7200 }
function setup(t: TestContext, admin = true, read = async () => ({ data: { ...initial }, defaults: { ...initial }, can_edit: true })) {
  const f = fixture(t)
  const auth = reactive({ admin, effectiveTenantId: 1, hasRole() { return this.admin } })
  const api = {
    getPDFProcessingSettings: t.mock.fn(read),
    updatePDFProcessingSettings: t.mock.fn(async (value: PDFProcessingSettings) => ({ data: value, defaults: { ...initial }, can_edit: true })),
  }
  const { vm } = f.component(new URL('./PDFProcessingSettings.vue', import.meta.url), {
    'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
    '@/stores/auth': { useAuthStore: () => auth },
    '@/api/pdf-processing': api,
  })
  return { vm, auth, api }
}

test('PDF policy loads first; hours/minutes/KB save in canonical units', async t => {
  const { vm, api } = setup(t)
  assert.equal(vm.editable, false)
  await vm.save()
  assert.equal(api.updatePDFProcessingSettings.mock.callCount(), 0)
  await flush()
  assert.equal(vm.editable, true)
  vm.form.overlap = 2; vm.form.batch = 32; vm.form.queue = 8; vm.form.execution = 90
  await vm.save()
  assert.deepEqual(JSON.parse(JSON.stringify(api.updatePDFProcessingSettings.mock.calls[0].arguments[0])), {
    overlap_pages: 2, grouping_request_bytes: 32000, queue_timeout_seconds: 28800, execution_timeout_seconds: 5400,
  })
  assert.equal(vm.saved, true)
  assert.equal(vm.dirty, false)
  vm.restore()
  assert.equal(vm.form.overlap, 1)
  assert.equal(vm.form.queue, 24)
  assert.equal(vm.dirty, true)
  assert.equal(api.updatePDFProcessingSettings.mock.callCount(), 1, 'restoring only edits the form until Save')
})

test('ordinary members neither fetch nor control PDF processing settings', async t => {
  const { vm, api } = setup(t, false)
  await flush(); vm.form.overlap = 3; await vm.save()
  assert.equal(vm.isAdmin, false)
  assert.equal(api.getPDFProcessingSettings.mock.callCount(), 0)
  assert.equal(api.updatePDFProcessingSettings.mock.callCount(), 0)
})

test('failed load cannot overwrite persisted admin policy', async t => {
  const { vm, api } = setup(t, true, async () => { throw new Error('unavailable') })
  await flush(); await vm.save()
  assert.equal(vm.loaded, false)
  assert.equal(api.updatePDFProcessingSettings.mock.callCount(), 0)
  api.getPDFProcessingSettings.mock.mockImplementation(async () => ({ data: initial, defaults: initial, can_edit: true }))
  await vm.load()
  assert.equal(vm.loaded, true)
})

test('a response from an old workspace cannot replace current workspace policy', async t => {
  let resolve!: (r: any) => void
  const { vm, auth, api } = setup(t, true, () => new Promise(done => { resolve = done }))
  api.getPDFProcessingSettings.mock.mockImplementation(async () => ({ data: { ...initial, overlap_pages: 3 }, defaults: initial, can_edit: true }))
  auth.effectiveTenantId = 2
  await flush()
  assert.equal(vm.form.overlap, 3)
  resolve({ data: initial, defaults: initial, can_edit: true })
  await flush()
  assert.equal(vm.form.overlap, 3)
})

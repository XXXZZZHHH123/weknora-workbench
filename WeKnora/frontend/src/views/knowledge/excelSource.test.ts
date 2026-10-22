import assert from 'node:assert/strict'
import test from 'node:test'
import { excelSourceExtension, isPdfSource, isStructuredSource, isExcelFilename, isExcelSource, sameNativeUploadScope, structuredSourceLocation } from './excelSource.ts'

test('Excel source routing uses the actual extension and leaves other formats alone', () => {
  assert.equal(isExcelFilename('Budget.XLSX'), true)
  assert.equal(isExcelFilename('Budget.xls'), true)
  for (const name of ['xlsx', 'Budget.xlsx.pdf', 'raw.csv', 'notes.md', 'new.xlsm']) assert.equal(isExcelFilename(name), false)
  assert.equal(excelSourceExtension({ type: 'file', file_type: 'XLSX', file_name: 'display name' }), 'xlsx')
  assert.equal(excelSourceExtension({ type: 'file', file_name: 'legacy.xls' }), 'xls')
  assert.equal(isExcelSource({ type: 'manual', file_name: 'title.xlsx' }), false)
})

test('an upload confirmation cannot move to another user, workspace, or knowledge base', () => {
  const scope = { userId: 'user-a', tenantId: '10000', knowledgeBaseId: 'kb-a' }
  assert.equal(sameNativeUploadScope(scope, { ...scope }), true)
  for (const patch of [{ userId: 'user-b' }, { tenantId: '10001' }, { knowledgeBaseId: 'kb-b' }]) {
    assert.equal(sameNativeUploadScope(scope, { ...scope, ...patch }), false)
  }
  assert.equal(sameNativeUploadScope({ ...scope, userId: '' }, { ...scope, userId: '' }), false)
})

test('structured parsing keeps real source ID and knowledge-base scope for legacy originals', () => {
  const source = { id: 'native-source', type: 'file', file_type: 'xlsx', knowledge_base_id: 'kb-a' }
  assert.deepEqual(structuredSourceLocation('kb-a', source), {
    path: '/platform/knowledge-bases/kb-a/structured-data', query: { mode: 'import', source_knowledge_id: 'native-source' },
  })
  assert.equal(structuredSourceLocation('kb-b', source), null)
  assert.equal(structuredSourceLocation('kb-a', { ...source, file_type: 'xls' }), null)
  assert.equal(structuredSourceLocation('kb-a', { ...source, id: '' }), null)
})

 test('PDF structure entry preserves original scope and keeps normal document classification', () => {
  const file = { id: 'pdf-source', type: 'file', file_type: 'PDF', file_name: 'native.pdf', knowledge_base_id: 'kb-a' }
  assert.equal(isPdfSource(file), true)
  assert.equal(isStructuredSource(file), true)
  assert.equal(isExcelSource(file), false)
  assert.deepEqual(structuredSourceLocation('kb-a', file), {path: '/platform/knowledge-bases/kb-a/structured-data', query: { mode: 'import', source_knowledge_id: 'pdf-source', source_file_type: 'pdf' }})
  assert.equal(structuredSourceLocation('kb-b', file), null)
  assert.equal(isPdfSource({...file, type: 'manual'}), false)
  assert.equal(isPdfSource({...file, file_type: 'docx'}), false)
})

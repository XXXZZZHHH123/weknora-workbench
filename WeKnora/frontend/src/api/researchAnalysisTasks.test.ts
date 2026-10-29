import assert from 'node:assert/strict'
import test from 'node:test'
import { analysisJobId, listAnalysisJobs, readAnalysisJobs } from './researchAnalysisTasks'

test('global task reads use only authenticated GET with explicit tenant and account scope', async () => {
  const calls: Array<{ path: string; options: RequestInit }> = []
  const jobs = await listAnalysisJobs({ userId: 'user-a', tenantId: '17', token: 'synthetic-token' }, () => true, async (url, options) => {
    calls.push({ path: String(url), options: options! })
    return new Response(JSON.stringify({ items: [{ id: 'job-a', workbench_id: 'wb-a', state: 'running', phase: 'analyzing', result_available: false }] }), { headers: { 'Content-Type': 'application/json' } })
  })
  assert.equal(calls[0]!.path, '/research/api/research/analysis-jobs?limit=20'); assert.equal(calls[0]!.options.method, 'GET')
  const headers = new Headers(calls[0]!.options.headers); assert.equal(headers.get('Authorization'), 'Bearer synthetic-token'); assert.equal(headers.get('X-Tenant-ID'), '17'); assert.equal(headers.get('X-Research-User'), 'user-a')
  assert.equal(calls[0]!.options.body, undefined); assert.equal(jobs[0]!.id, 'job-a')
})
test('metadata discards raw result, credentials, arbitrary titles and unsupported job identifiers', () => {
  const jobs = readAnalysisJobs({ items: [{ id: 'job-a', workbench_id: 'wb-a', state: 'succeeded', phase: 'private-phase', result_available: true, title: 'private filename', source_text: 'source', bearer: 'secret', result: { markdown: 'private result' } }, { id: '../other', workbench_id: 'wb-a', state: 'running' }] })
  assert.equal(jobs.length, 1); assert.equal(jobs[0]!.title, '研究逻辑分析'); assert.equal(jobs[0]!.phase, '')
  assert.doesNotMatch(JSON.stringify(jobs), /private|secret|source_text|bearer|markdown/)
  assert.equal(analysisJobId(['job-a']), ''); assert.equal(analysisJobId('x'.repeat(129)), '')
})
test('forbidden reads have a stable status for bounded polling and never expose provider bodies', async () => {
  await assert.rejects(listAnalysisJobs({ userId: 'user-a', tenantId: '17', token: 'token' }, () => true, async () => new Response('private provider body', { status: 403 })), error => (error as { status?: number }).status === 403 && !String(error).includes('private'))
})

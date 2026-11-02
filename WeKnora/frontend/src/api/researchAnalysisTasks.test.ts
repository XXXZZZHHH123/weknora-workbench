import assert from 'node:assert/strict'
import test from 'node:test'
import { analysisJobId, listAnalysisJobs, readAnalysisJobs } from './researchAnalysisTasks'
import * as analysisApi from './researchAnalysisTasks'

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

test('diagram task metadata keeps its fixed purpose and generation phases without trusting a model title', () => {
  const jobs = readAnalysisJobs({ items: [
    { id: 'diagram-job', workbench_id: 'wb-a', purpose: 'diagram', state: 'running', phase: 'generating', title: 'private title' },
    { id: 'legacy-job', workbench_id: 'wb-a', state: 'queued', phase: 'queued' },
    { id: 'unsupported-job', workbench_id: 'wb-a', purpose: 'arbitrary', state: 'queued' },
  ] })
  assert.equal(jobs.length, 2)
  assert.equal(jobs[0]!.purpose, 'diagram'); assert.equal(jobs[0]!.title, '产业图配置生成'); assert.equal(jobs[0]!.phase, 'generating')
  assert.equal(jobs[1]!.purpose, 'judgments')
})

test('dismiss metadata is explicit and cannot hide running work', () => {
  const jobs = readAnalysisJobs({ items: [
    { id: 'done', workbench_id: 'wb-a', state: 'succeeded', dismissed: true, result_available: true },
    { id: 'active', workbench_id: 'wb-a', state: 'running', dismissed: true },
    { id: 'old', workbench_id: 'wb-a', state: 'superseded', dismissed: true },
  ] })
  assert.equal(jobs[0]!.dismissed, true)
  assert.notEqual(jobs[1]!.dismissed, true)
  assert.equal(jobs[2]!.state, 'superseded')
})

test('ignore uses an authenticated scoped POST and returns only checked task metadata', async () => {
  assert.equal(typeof analysisApi.dismissAnalysisJob, 'function')
  const calls: Array<{ path: string; options: RequestInit }> = []
  const result = await analysisApi.dismissAnalysisJob({ userId: 'user-a', tenantId: '17', token: 'synthetic-token' }, 'job-a', () => true, async (url, options) => {
    calls.push({ path: String(url), options: options! })
    return new Response(JSON.stringify({ id: 'job-a', workbench_id: 'wb-a', state: 'succeeded', dismissed: true, result_available: true, source_text: 'private' }))
  })
  assert.equal(calls[0]!.path, '/research/api/research/analysis-jobs/job-a/dismiss')
  assert.equal(calls[0]!.options.method, 'POST')
  const headers = new Headers(calls[0]!.options.headers)
  assert.equal(headers.get('Authorization'), 'Bearer synthetic-token')
  assert.equal(headers.get('X-Tenant-ID'), '17'); assert.equal(headers.get('X-Research-User'), 'user-a')
  assert.equal(calls[0]!.options.body, undefined)
  assert.equal(result.dismissed, true); assert.equal(result.result_available, true)
  assert.doesNotMatch(JSON.stringify(result), /private|source_text/)
})

test('ignore rejects stale identities, unsafe IDs and mismatched acknowledgements', async () => {
  assert.equal(typeof analysisApi.dismissAnalysisJob, 'function')
  const owner = { userId: 'user-a', tenantId: '17', token: 'token' }
  let calls = 0
  const fetcher: typeof fetch = async () => { calls++; return new Response(JSON.stringify({ id: 'other', workbench_id: 'wb-a', state: 'succeeded', dismissed: true })) }
  await assert.rejects(analysisApi.dismissAnalysisJob(owner, '../job', () => true, fetcher))
  assert.equal(calls, 0)
  await assert.rejects(analysisApi.dismissAnalysisJob(owner, 'job-a', () => true, fetcher))
  await assert.rejects(analysisApi.dismissAnalysisJob(owner, 'job-a', () => false, fetcher))
  assert.equal(calls, 1)
})

test('ignore errors preserve a safe status without exposing provider bodies', async () => {
  assert.equal(typeof analysisApi.dismissAnalysisJob, 'function')
  await assert.rejects(analysisApi.dismissAnalysisJob({ userId: 'user-a', tenantId: '17', token: 'token' }, 'job-a', () => true,
    async () => new Response('private provider body', { status: 409 })), error => (error as { status?: number }).status === 409 && !String(error).includes('private'))
})

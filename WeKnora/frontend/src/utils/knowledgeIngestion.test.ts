import assert from 'node:assert/strict'
import test from 'node:test'
import { knowledgeCoverageSummary, knowledgeIngestion } from './knowledgeIngestion'
import { itemPhase, needsParsePolling, classifyUploadResult } from '../stores/uploadTasksState'

test('native processing continues even if a document becomes searchable first', () => {
  const item = { id:'one',batchId:'batch',name:'source.xlsx',relativePath:'',size:10,loaded:10,transfer:'uploaded' as const,knowledgeId:'doc',parseStatus:'completed',processingStatus:'processing',processingStage:'正在整理工作表' }
  assert.equal(itemPhase(item),'parsing')
  assert.equal(needsParsePolling(item),true)
})
test('partial publication has a bounded attention count, not a complete-success claim', () => {
  const metadata = {_knowledge_ingestion:{status:'needs_attention',stage:'可用内容已入库',attention_count:3}}
  assert.deepEqual(knowledgeIngestion(metadata),{status:'needs_attention',stage:'可用内容已入库',attentionCount:3,result:undefined})
  assert.equal(knowledgeIngestion({_knowledge_ingestion:{attention_count:Infinity}}).attentionCount,0)
  const receipt=classifyUploadResult({success:true,data:{id:'doc',parse_status:'processing',metadata}})
  assert.equal(receipt.kind,'uploaded')
  if(receipt.kind==='uploaded') assert.equal(receipt.attentionCount,3)
})

test('coverage receipt reports only explicit parser and structured-ingestion evidence', () => {
  const noTable = knowledgeCoverageSummary({
    _content_processing_v1: { text_chunk_count: 2, image_count: 0 },
    _knowledge_ingestion: { result: { table_count: 0, accepted_count: 0, attention_count: 0 } },
  }, 'completed')
  assert.ok(noTable.includes('正文：已保存 2 个文本分块'))
  assert.ok(noTable.includes('图片：已保存 0 张'))
  assert.ok(noTable.includes('表格：识别 0 张'))
  assert.ok(noTable.includes('可计算数据：未统计'), 'no table count cannot invent a numeric count')
  const published = knowledgeCoverageSummary({
    _content_processing_v1: { text_chunk_count: 12, image_count: 2 },
    _knowledge_ingestion: { attention_count: 2, result: { table_count: 1, accepted_count: 1, computable_value_count: 2 } },
  }, 'completed')
  assert.ok(published.includes('表格：自动入库 1 张（识别片段 1）'))
  assert.ok(published.includes('可计算数据：已入库 2 个有效数值'))
  assert.ok(published.includes('内容覆盖尚未核验'))
  assert.ok(published.includes('待处理 2'))
  assert.ok(knowledgeCoverageSummary({
    _knowledge_ingestion: { result: { sheets: [{ name: 'Sheet1', status: 'auto_ready' }] } },
  }).includes('工作表结果 1 项'))
  assert.ok(knowledgeCoverageSummary({ _knowledge_ingestion: { result: { table_count: '0' } } }).includes('表格：未统计'))
})

test('current native receipt keeps nested unresolved groups visible', () => {
  assert.ok(knowledgeCoverageSummary({ _knowledge_ingestion: {
    status: 'completed', result: { accepted_count: 6, attention_count: 21 },
  } }, 'completed').includes('待处理 21'))
  const unknown = knowledgeCoverageSummary({ pages: 15, chunk_count: 5 }, 'completed')
  for (const type of ['正文', '图片', '表格', '可计算数据']) assert.ok(unknown.includes(`${type}：未统计`))
  assert.ok(unknown.includes('内容覆盖尚未核验'))
})

test('completed document keeps deferred preview and unavailable originals visible', () => {
  const summary = knowledgeCoverageSummary({ pptx_media_processing_v1: {
    deferred_vector_previews: 3, unavailable_vector_parts: 1,
  } }, 'completed')
  assert.ok(summary.includes('预览待生成 3'))
  assert.ok(summary.includes('原图不可用 1'))
  const invalid = knowledgeCoverageSummary({ pptx_media_processing_v1: {
    deferred_vector_previews: -1, unavailable_vector_parts: Infinity,
  } }, 'completed')
  assert.ok(!invalid.some(item => item.startsWith('预览待生成') || item.startsWith('原图不可用')))
})

test('workbook catalog chunks and malformed counts do not claim body coverage', () => {
  const summary = knowledgeCoverageSummary({
    _content_processing_v1: { text_chunk_count: 1, text_is_catalog: true, image_count: -1 },
    _knowledge_ingestion: { result: { computable_value_count: Infinity } },
  }, 'completed')
  assert.ok(summary.includes('正文：未统计（另存目录 1 分块）'))
  assert.ok(summary.includes('图片：未统计'))
  assert.ok(summary.includes('可计算数据：未统计'))
})

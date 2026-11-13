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
  assert.deepEqual(knowledgeCoverageSummary({
    _knowledge_ingestion: { result: { table_count: 0, accepted_count: 0, attention_count: 0 } },
  }, 'completed'), ['处理流程已结束', '未识别到结构化表格'])
  assert.deepEqual(knowledgeCoverageSummary({
    chunk_count: 12,
    image_count: 2,
    _knowledge_ingestion: { attention_count: 2, result: { accepted_count: 1, attention_count: 2 } },
  }, 'completed'), ['处理流程已结束', '自动入库 1 张表', '图片 2', '分块 12', '待处理 2'])
  assert.deepEqual(knowledgeCoverageSummary({
    _knowledge_ingestion: { result: { sheets: [{ name: 'Sheet1', status: 'auto_ready' }] } },
  }), ['工作表结果 1 项'])
  assert.deepEqual(knowledgeCoverageSummary({ _knowledge_ingestion: { result: { table_count: '0' } } }), [])
})

test('current native receipt keeps nested unresolved groups visible', () => {
  assert.deepEqual(knowledgeCoverageSummary({ _knowledge_ingestion: {
    status: 'completed', result: { accepted_count: 6, attention_count: 21 },
  } }, 'completed'), ['处理流程已结束', '自动入库 6 张表', '待处理 21'])
  assert.deepEqual(knowledgeCoverageSummary({}, 'completed'), ['处理流程已结束'])
})

test('completed document keeps deferred preview and unavailable originals visible', () => {
  assert.deepEqual(knowledgeCoverageSummary({ pptx_media_processing_v1: {
    deferred_vector_previews: 3, unavailable_vector_parts: 1,
  } }, 'completed'), ['处理流程已结束', '预览待生成 3', '原图不可用 1'])
  assert.deepEqual(knowledgeCoverageSummary({ pptx_media_processing_v1: {
    deferred_vector_previews: -1, unavailable_vector_parts: Infinity,
  } }, 'completed'), ['处理流程已结束'])
})

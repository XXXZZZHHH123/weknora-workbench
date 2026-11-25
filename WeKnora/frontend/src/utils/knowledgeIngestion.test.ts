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
  assert.ok(noTable.includes('已保存 2 段正文'))
  assert.ok(noTable.includes('图片：已保存 0 张'))
  assert.ok(noTable.includes('没有识别到表格'))
  assert.ok(noTable.includes('可计算的数值尚未统计'), 'no table count cannot invent a numeric count')
  const published = knowledgeCoverageSummary({
    _content_processing_v1: { text_chunk_count: 12, image_count: 2 },
    _knowledge_ingestion: { attention_count: 2, result: { table_count: 1, accepted_count: 1, computable_value_count: 2 } },
  }, 'completed')
  assert.ok(published.includes('已自动整理 1 张表，尚待人工核对（识别到 1 处表格内容）'))
  assert.ok(published.includes('已有 2 个数值可用于计算'))
  assert.ok(published.includes('当前结果可能只包含原件中的部分内容'))
  assert.ok(published.includes('2 处需要核对，请打开文件查看处理结果'))
  assert.ok(knowledgeCoverageSummary({
    _knowledge_ingestion: { result: { sheets: [{ name: 'Sheet1', status: 'auto_ready' }] } },
  }).includes('可查看 1 张工作表的处理结果'))
  assert.ok(knowledgeCoverageSummary({ _knowledge_ingestion: { result: { table_count: '0' } } }).includes('表格数量未统计'))
})

test('current native receipt keeps nested unresolved groups visible', () => {
  assert.ok(knowledgeCoverageSummary({ _knowledge_ingestion: {
    status: 'completed', result: { accepted_count: 6, attention_count: 21 },
  } }, 'completed').includes('21 处需要核对，请打开文件查看处理结果'))
  const unknown = knowledgeCoverageSummary({ pages: 15, chunk_count: 5 }, 'completed')
  for (const type of ['正文数量未统计', '图片：未统计', '表格数量未统计', '可计算的数值尚未统计']) assert.ok(unknown.includes(type))
  assert.ok(unknown.includes('当前结果可能只包含原件中的部分内容'))
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
  assert.ok(summary.includes('已保存目录中的 1 段，正文收录范围尚未确认'))
  assert.ok(summary.includes('图片：未统计'))
  assert.ok(summary.includes('可计算的数值尚未统计'))
})

import assert from 'node:assert/strict'
import test from 'node:test'
import { knowledgeIngestion } from './knowledgeIngestion'
import { itemPhase, needsParsePolling, classifyUploadResult } from '../stores/uploadTasksState'

test('native processing continues even if a document becomes searchable first', () => {
  const item = { id:'one',batchId:'batch',name:'source.xlsx',relativePath:'',size:10,loaded:10,transfer:'uploaded' as const,knowledgeId:'doc',parseStatus:'completed',processingStatus:'processing',processingStage:'正在整理工作表' }
  assert.equal(itemPhase(item),'parsing')
  assert.equal(needsParsePolling(item),true)
})
test('partial publication has a bounded attention count, not a complete-success claim', () => {
  const metadata = {_knowledge_ingestion:{status:'needs_attention',stage:'可用内容已入库',attention_count:3}}
  assert.deepEqual(knowledgeIngestion(metadata),{status:'needs_attention',stage:'可用内容已入库',attentionCount:3})
  assert.equal(knowledgeIngestion({_knowledge_ingestion:{attention_count:Infinity}}).attentionCount,0)
  const receipt=classifyUploadResult({success:true,data:{id:'doc',parse_status:'processing',metadata}})
  assert.equal(receipt.kind,'uploaded')
  if(receipt.kind==='uploaded') assert.equal(receipt.attentionCount,3)
})

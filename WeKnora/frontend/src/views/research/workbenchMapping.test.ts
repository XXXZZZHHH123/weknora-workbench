import assert from 'node:assert/strict'
import test from 'node:test'
import { applyMetadataRows, parseHeaderRows } from './workbenchMapping'

const block={header_rows:[1,2,3],columns:[{column:'B',metric:'unconfirmed candidate',unit:null,header_cells:['B1']},{column:'C',metric:'another candidate',unit:null,header_cells:['C1']}]}
const preview=[[{cell:'B1',value:'Actual field A'},{cell:'C1',value:'Actual field B'}],[{cell:'B2',value:'fixture-id-a'},{cell:'C2',value:'fixture-id-b'}],[{cell:'B3',value:'kg'},{cell:'C3',value:null}]]
test('metadata applies only explicitly selected source rows and retains original evidence coordinates',()=>{
  const result=applyMetadataRows(block,preview,1,3)
  assert.equal(result.columns[0]!.metric,'Actual field A')
  assert.equal(result.columns[0]!.unit,'kg')
  assert.equal(result.columns[1]!.unit,null)
  assert.deepEqual(result.columns[0]!.header_cells,['B1','B3'])
  assert.equal(block.columns[0]!.metric,'unconfirmed candidate')
  assert.equal(block.columns[0]!.unit,null)
})
test('missing selection, unknown preview cells and rows outside declared headers never invent metadata',()=>{
  assert.throws(()=>applyMetadataRows(block,preview),/先明确/)
  assert.throws(()=>applyMetadataRows(block,preview,4),/范围/)
  assert.throws(()=>applyMetadataRows(block,[],1),/没有可用名称/)
  assert.throws(()=>applyMetadataRows(block,[],undefined,3),/不在已读取/)
})
test('header row entry accepts explicit integer lists and rejects partial or negative input',()=>{
  assert.deepEqual(parseHeaderRows('1，2, 3 3'),[1,2,3])
  for(const invalid of ['', '1,-2','1,2a','0','1.5'])assert.throws(()=>parseHeaderRows(invalid),/正整数/)
})
test('frequency comes only from a user-selected real metadata cell and keeps its coordinate',()=>{
  const source={header_rows:[1,2],columns:[{column:'B',metric:'test field',unit:'kg',header_cells:['B1']}]}
  const result=applyMetadataRows(source,[[{cell:'B1',value:'test field'}],[{cell:'B2',value:'weekly'}]],undefined,undefined,2)
  assert.equal(result.columns[0]!.frequency,'weekly')
  assert.deepEqual(result.columns[0]!.frequency_cells,['B2'])
  assert.throws(()=>applyMetadataRows(source,[[{cell:'B1',value:'test field'}]],undefined,undefined,2),/没有频率原文/)
})

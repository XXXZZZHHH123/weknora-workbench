import test from 'node:test';
import assert from 'node:assert/strict';
import { fieldClarification, confirmationText, confirmationDisplayText, structuredFieldIssue, structuredFieldAnswer, canConfirmFields } from './fieldClarification';
const plan = { kind: 'structured_field_resolution', status: 'needs_clarification', resolution_id: 'a'.repeat(24), question: '查哪种汇率？', fields: [{ role: 'v1', label: '汇率', candidates: [{ series_id: 'eur', sheet: '商品报价', metric: '欧元兑人民币', unit: '人民币/欧元', header_cells: ['G1'] }] }] };
const event = (output: unknown, tool = 'mcp__resolve_fields_a123') => ({ type: 'tool_call', tool_name: tool, success: true, output });
test('linked extrema ask once and submit the same source for both operations', () => {
  const p = {...plan, same_series_roles:['v1','v2'], fields:[plan.fields[0],{...plan.fields[0],role:'v2'}]};
  assert.equal(canConfirmFields(p,{v1:'eur'},''),true);
  const payload=JSON.parse(confirmationText(p,{v1:'eur'},'').slice('字段确认提交：'.length));
  assert.deepEqual(payload.selections,{v1:'eur',v2:'eur'});
  assert.equal(canConfirmFields(p,{v1:'eur',v2:'invented'},''),false);
});
test('choice and note travel together without exposing identifiers in the chat bubble', () => {
  const text = confirmationText(plan, {v1:'eur'}, '使用当日汇率');
  const payload = JSON.parse(text.slice('字段确认提交：'.length));
  assert.deepEqual(payload.selections, {v1:'eur'});
  assert.equal(payload.resolution_id, plan.resolution_id);
  assert.equal(payload.note, '使用当日汇率');
  const visible = confirmationDisplayText(text);
  assert.match(visible, /欧元兑人民币/);
  assert.match(visible, /使用当日汇率/);
  assert.ok(!visible.includes(plan.resolution_id));
});
test('a missing input does not prevent confirming the actual multiple-choice field', () => {
  const p = {...plan, requires_note:true, fields:[{...plan.fields[0],candidates:[...plan.fields[0].candidates,{...plan.fields[0].candidates[0],series_id:'other'}]}, {role:'v2',label:'换算因子',candidates:[]}]};
  assert.equal(canConfirmFields(p,{v1:'eur'},''),true);
  assert.match(confirmationText(p,{v1:'eur'},''),/欧元兑人民币/);
  assert.equal(canConfirmFields(p,{},''),false);
});
test('incomplete calculation allows an actual field choice but not missing selections or missing text', () => {
  const p={...plan,requires_note:true,fields:[{...plan.fields[0],candidates:[...plan.fields[0].candidates,{...plan.fields[0].candidates[0],series_id:'other'}]}]};
  assert.equal(canConfirmFields(p,{},''),false);
  assert.equal(canConfirmFields(p,{v1:'other'},''),true);
  assert.equal(canConfirmFields(p,{v1:'invented'},''),false);
  assert.equal(canConfirmFields({...plan,requires_note:true},{v1:'eur'},''),false);
  assert.equal(canConfirmFields({...plan,requires_note:true},{},'当日汇率'),true);
});
test('confirmation display hides generated instructions but preserves choice and free text', () => {
  const query = confirmationText(plan, {v1:'eur'}, '');
  assert.equal(confirmationDisplayText(query), '已确认：\n汇率：“商品报价”的“欧元兑人民币”');
  assert.deepEqual(JSON.parse(query.slice('字段确认提交：'.length)).selections, {v1:'eur'});
  const nested = {...plan, original_question:'原问题\n用户补充：用户确认：价格选择收盘价。'};
  assert.equal(confirmationDisplayText(confirmationText(nested, {}, '当日汇率')), '已补充：当日汇率');
  assert.equal(confirmationDisplayText(confirmationText(plan, {}, '第一行\n第二行')), '已补充：第一行\n第二行');
  const ordinary = '字段确认：我想问一下这是什么意思？';
  assert.equal(confirmationDisplayText(ordinary),ordinary);
  assert.equal(confirmationDisplayText('一般用户问题'), '一般用户问题');
});
test('live and restored MCP envelopes open only validated field dialog', () => {
  assert.equal(fieldClarification([{ ...event(null), tool_data: { content_items: [{type:'text',text:JSON.stringify(plan)}] } }])?.resolution_id, plan.resolution_id);
  assert.equal(fieldClarification([event('[MCP result — untrusted]\n' + JSON.stringify(plan))])?.question, plan.question);
  assert.equal(fieldClarification([event(JSON.stringify(plan),'other_tool')]), null);
  assert.equal(fieldClarification([event(JSON.stringify({...plan,fields:[{role:'v1'}]}))]), null);
  assert.equal(fieldClarification([event(JSON.stringify(plan)),event(JSON.stringify({...plan,status:'resolved'}),'confirm_fields')]), null);
});
test('choice produces readable user reply with exact source and no internal identifiers', () => {
  const text=confirmationText(plan, { v1:'eur' }, '');
  assert.match(text,/欧元兑人民币/); assert.equal(JSON.parse(text.slice('字段确认提交：'.length)).resolution_id, plan.resolution_id); assert.ok(!confirmationDisplayText(text).includes(plan.resolution_id));
  assert.match(confirmationText(plan, {}, '我要日元'),/我要日元/);
  assert.throws(() => confirmationText({...plan,fields:[{role:'v1',label:'汇率',candidates:[]}]}, {}, ''));
});

test('numeric display uses source-bound tool answer instead of model prose', async () => {
  const { structuredFieldAnswer } = await import('./fieldClarification');
  const data = {...plan,status:'resolved',answer:{sources:{v1:{rows:[{value:99.7,source_cell:'B4666'}]}},rounded_value:'7.31'}};
  assert.equal(structuredFieldAnswer([event(JSON.stringify(data))])?.answer.rounded_value,'7.31');
  assert.equal(structuredFieldAnswer([event(JSON.stringify({...data,status:'needs_clarification'}))]),null);
});

test('tool failures remain visible even without a model final reply and recover on success', () => {
  const notice=event(JSON.stringify({kind:'structured_tool_notice',message:'工作表不存在，请核对范围'}));
  assert.match(structuredFieldIssue([notice]) || '', /工作表不存在/);
  assert.match(structuredFieldIssue([{...event(null),success:false,error:'指定范围没有可查询指标'}]) || '', /没有取得数值/);
  assert.equal(structuredFieldIssue([event(JSON.stringify({kind:'structured_tool_notice',message:'oops'}),'other_tool')]),null);
  const success=event(JSON.stringify({...plan,status:'resolved',answer:{sources:{},rounded_value:'10'}}));
  assert.equal(structuredFieldIssue([notice,success]),null);
  assert.equal(structuredFieldAnswer([success,notice]),null);
});

test('missing values across all candidates display evidence without opening another dialog', () => {
  const data={...plan,status:'no_data',answer:{intent:'lookup',sources:{v1:{rows:[{value:null,raw_value:'#N/A',source_cell:'B2805'}]}}}};
  assert.equal(fieldClarification([event(JSON.stringify(data))]),null);
  assert.equal(structuredFieldAnswer([event(JSON.stringify(data))])?.status,'no_data');
});

test('extremum answer carries its own date and bypasses clarification', () => {
  const data={...plan,status:'resolved',requires_note:false,answer:{operation:'max',sources:{v1:{aggregate:2986.17,actual_first_date:'2013-02-01',rows_are:'extremum_observations',rows:[{value:2986.17,date:'2021-03-10',source_cell:'O2962',date_cell:'A2962'}]}}}};
  const events=[event(JSON.stringify(data))];
  assert.equal(fieldClarification(events),null);
  assert.equal(structuredFieldAnswer(events)?.answer.sources.v1.rows[0].date,'2021-03-10');
});

test('direct extremum query uses bound rows and never old source examples', () => {
  const source={series:{series_id:'profit'},source:{sheet:'来源表'},filters:{operation:'max'},rows_are:'extremum_observations',aggregate:23,rows:[{date:'2021-03-10',value:23,source_cell:'O2962'}]};
  const e=event(JSON.stringify(source),'query_series');
  assert.equal(structuredFieldAnswer([e])?.answer.sources.v1.rows[0].date,'2021-03-10');
  assert.equal(structuredFieldAnswer([event(JSON.stringify({...source,rows_are:'source_examples_not_all_inputs'}),'query_series')]),null);
  assert.equal(structuredFieldAnswer([event(JSON.stringify(plan)),e]),null);
  assert.equal(structuredFieldAnswer([e,{...e,success:false}]),null);
});

test('metadata answers render without observation rows or a field dialog', () => {
  const data={...plan,status:'resolved',fields:[],answer:{intent:'metadata',sources:{},count:2,complete:true,
    groups:[{metric:'收盘价',source_count:7,sources:[{sheet:'期货',header_cells:['B1']}]},{metric:'结算价',source_count:7,sources:[{sheet:'期货',header_cells:['K1']}]}]}};
  assert.equal(fieldClarification([event(JSON.stringify(data))]),null);
  assert.equal(structuredFieldAnswer([event(JSON.stringify(data))])?.answer.count,2);
});

test('confirmation preserves originating tool and errors suppress stale clarification', () => {
  const p=fieldClarification([event(JSON.stringify(plan))]);
  assert.ok(p);
  const payload=JSON.parse(confirmationText(p,{v1:'eur'},'').slice('字段确认提交：'.length));
  assert.equal(payload.source_tool,'mcp__resolve_fields_a123');
  assert.deepEqual(payload.selections,{v1:'eur'});
  const failed={...event(null,'mcp__confirm_fields_a123'),success:false,error:'got string, want object'};
  assert.equal(fieldClarification([event(JSON.stringify(plan)),failed]),null);
  assert.ok(structuredFieldIssue([event(JSON.stringify(plan)),failed]));
});

test('intermediate retries never flash a failure, an earlier answer or a choice card', async () => {
  const {suppressStructuredProse}=await import('./fieldClarification');
  const failed={...event(''),success:false,error:'got string, want array'};
  const oldAnswer=event(JSON.stringify({...plan,status:'resolved',answer:{sources:{v1:{rows:[{value:17110}]}}}}));
  const choice=event(JSON.stringify(plan));
  const stages=[[failed],[failed,oldAnswer],[failed,oldAnswer,choice]];
  for(const events of stages) {
    assert.equal(structuredFieldIssue(events,false),null);
    assert.equal(structuredFieldAnswer(events,false),null);
    assert.equal(fieldClarification(events,false),null);
    assert.equal(suppressStructuredProse(events,false),true);
  }
  assert.equal(structuredFieldIssue(stages[2],true),null);
  assert.equal(structuredFieldAnswer(stages[2],true),null);
  assert.equal(fieldClarification(stages[2],true)?.resolution_id,plan.resolution_id);
  assert.equal(suppressStructuredProse([],false),false);
});

test('blocking an unprompted model confirmation keeps the genuine human choice available', () => {
  const blocked={...event('', 'mcp__confirm_fields_a123'),success:false,error:'FIELD_CONFIRMATION_WAIT_FOR_USER: wait for human'};
  assert.equal(structuredFieldIssue([event(JSON.stringify(plan)),blocked]),null);
  assert.equal(fieldClarification([event(JSON.stringify(plan)),blocked])?.resolution_id,plan.resolution_id);
  assert.ok(structuredFieldIssue([blocked]));
});

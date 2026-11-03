const assert=require('node:assert/strict');
const {generateReview,reviewControl,descendants,visibleDescendants}=require('./judgment-manager-ui.cjs');
const sections=[
  {id:'contradiction',title:'主矛盾',items:[
    {id:'anchor',kind:'binding',title:'待核对数据锚',description:'关联来源后保留研究依据。',original_text:'合成研究依据',status:'pending',requires_attention:true,attention_fields:['binding'],warnings:['请选择数据来源。'],can_bind:true,can_confirm:true,binding:null},
    {id:'ready-text',kind:'description',title:'已解析研究背景',description:'已经完整解析的文字。',status:'ready',can_bind:false,can_confirm:true,warnings:[]}]},
  {id:'rules',title:'阈值与证伪',items:[
    {id:'boundary',kind:'falsifiers',title:'待核对边界',description:'边界条件需核对。',status:'pending',requires_attention:true,can_bind:false,can_confirm:false,attention_fields:['content'],warnings:['请核对边界条件。']},
    {id:'confirmed',kind:'description',title:'此前已确认内容',description:'用户已经确认。',status:'confirmed',requires_attention:true,attention_fields:['content'],warnings:['遗留提示不应再次要求确认。'],can_bind:false,can_confirm:true}]}];
const group=(env,id)=>env.nodes['logic-review-items'].children.find(node=>node.dataset.reviewSection===id);
const members=node=>node.children.filter(child=>child.dataset.reviewItem).map(child=>child.dataset.reviewItem);
(async()=>{
  const env=await generateReview({reviewSections:sections});
  assert.equal(env.nodes['logic-review-nav'].children[0].dataset.reviewSection,'contradiction','The sidebar retains business categories rather than a second pending entry');
  assert.equal(env.nodes['logic-review-nav'].children.some(node=>node.dataset.reviewSection==='__pending__'),false);
  assert.deepEqual(members(group(env,'contradiction')),['anchor','ready-text']);
  assert.deepEqual(members(group(env,'rules')),['boundary','confirmed']);
  const settledCard=descendants(group(env,'rules')).find(node=>node.dataset.reviewItem==='confirmed'&&node.dataset.reviewStatus);
  assert.equal(descendants(settledCard).some(node=>node.dataset.invalid==='true'),false,'A server-confirmed item does not inherit stale red fields');
  await env.nodes['logic-review-attention'].fire('click');
  assert.deepEqual(visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem),['anchor']);
  await env.nodes['logic-review-nav'].children.find(node=>node.dataset.reviewSection==='rules').fire('click');
  assert.deepEqual(visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem),['boundary']);
  await env.nodes['logic-review-nav'].children[0].fire('click');
  const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
  assert.equal(group(env,'contradiction').hidden,true,'After confirmation the settled category is absent from the pending filter');
  assert.deepEqual(visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem),['boundary']);
  assert.equal(descendants(group(env,'contradiction')).some(node=>node.dataset.invalid==='true'),false,'Successful confirmation clears old red frames');
  await env.nodes['logic-review-all'].fire('click');await env.nodes['logic-review-nav'].children[0].fire('click');
  assert.deepEqual(visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem),['anchor','ready-text']);
  const confirmedSearch=reviewControl(env,'anchor','search');confirmedSearch.value='Different choice';await confirmedSearch.fire('input');
  await env.nodes['logic-review-attention'].fire('click');
  assert.deepEqual(visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem),['anchor'],'A new unsynchronized edit remains actionable in the one pending filter');
  assert.equal(descendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewItem==='anchor'&&node.dataset.reviewStatus).length,1);
  await reviewControl(env,'boundary','keep_text').fire('click');
  assert.equal(reviewControl(env,'boundary','keep_text').parentNode.parentNode.parentNode.hidden,true);
  assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  env.context.window.JudgmentManager.close();
  {
    const confirmedCaliber={id:'caliber',kind:'thresholds',title:'已核对来源口径',description:'已确认的合成边界。',status:'confirmed',can_bind:true,can_confirm:true,binding:{series_id:'condition-unit',name:'合成数量',unit:'吨'},rule:{operator:'>=',value_literal:'12'},caliber:{confirmed:true,can_declare:true,unit:'吨',basis:'已核对原表',scope_note:'',confirmation_required:false}};
    const caliberEnv=await generateReview({reviewSections:[{id:'rules',title:'边界条件',items:[confirmedCaliber]}]});
    assert.equal(reviewControl(caliberEnv,'caliber','caliber-declare'),undefined,'A previously confirmed source never requires a repeated unit confirmation');
    assert.equal(reviewControl(caliberEnv,'caliber','caliber-unit'),undefined);
    assert.equal(descendants(caliberEnv.nodes['logic-review-items']).some(node=>node.dataset.invalid==='true'),false);
    await caliberEnv.nodes['logic-review-attention'].fire('click');
    assert.equal(visibleDescendants(caliberEnv.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).length,0);
    assert.match(caliberEnv.nodes['logic-review-empty'].textContent,/暂无需要处理的问题/);
    assert.equal(caliberEnv.requests.filter(row=>row.url.endsWith('/review/update')).length,0);
    caliberEnv.context.window.JudgmentManager.close();
  }
  {
    const noteEnv=await generateReview({reviewSections:[{id:'unresolved',title:'补充说明',items:[{id:'extra-note',kind:'text',title:'原文附注',description:'没有归入其他研究条目的原文说明。',status:'ready',can_bind:false,can_confirm:true}]}]});
    const nav=noteEnv.nodes['logic-review-nav'].children[0];
    assert.match(nav.textContent,/^补充说明/);
    assert.equal(nav.title,'未归入结构化条目的原文');
    assert.equal(group(noteEnv,'unresolved').children[0].title,'未归入结构化条目的原文');
    await noteEnv.nodes['logic-review-attention'].fire('click');
    assert.equal(visibleDescendants(noteEnv.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).length,0,'Supplementary source content does not automatically become a confirmation task');
    noteEnv.context.window.JudgmentManager.close();
  }
  console.log('Single pending filter, original categories, settled status and automatic source caliber passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

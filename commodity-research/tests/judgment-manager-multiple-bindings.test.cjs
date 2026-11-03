const assert = require('node:assert/strict');
const {generateReview, reviewControl, descendants, flush} = require('./judgment-manager-ui.cjs');
const primary = {series_id:'primary-choice', name:'Supply indicator', unit:'kg'};
const linked = {series_id:'related-choice', name:'Utilization indicator', unit:'%'};
const item = overrides => ({id:'anchor',kind:'binding',title:'研究依据',description:'Observe supply and utilization together.',
  status:'ready',can_bind:true,can_add_binding:true,max_bindings:8,requires_binding:false,
  binding:primary,ai_binding:primary,related_bindings:[],ai_related_bindings:[],warnings:[],...overrides});
const sections = row => [{id:'contradiction',title:'主矛盾',items:[row]}];
const searches = env => descendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewItem==='anchor'&&node.dataset.reviewControl==='search');
const controls = (env,name) => descendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewItem==='anchor'&&node.dataset.reviewControl===name);
const updates = env => env.requests.filter(row=>row.url.endsWith('/review/update'));
async function choose(env,index,choice=1) {
  const search=searches(env)[index];await search.fire('focus');
  for(let step=0;step<=choice;step++)await search.fire('keydown',{key:'ArrowDown'});
  await search.fire('keydown',{key:'Enter'});await flush();
}
(async()=>{
  {
    const env=await generateReview({reviewSections:sections(item())});
    assert.equal(searches(env).length,1,'A single default binding remains compact');
    const add=reviewControl(env,'anchor','add-binding');assert.ok(add,'Research items can explicitly add another binding');
    await add.fire('click');assert.equal(searches(env).length,2);assert.equal(updates(env).length,0);
    assert.equal(env.nodes['logic-apply'].disabled,false,'An unused added row does not dirty or block the draft');
    await choose(env,1);
    assert.equal(updates(env).at(-1).body.series_id,'primary-choice');
    assert.deepEqual(updates(env).at(-1).body.related_series_ids,['authorized-other']);
    assert.equal(searches(env)[0].value,'Supply indicator');assert.equal(searches(env)[1].value,'Directory quantity <script>');
    assert.equal(env.nodes['logic-review-items'].textContent.includes('Observe supply and utilization together.'),true);
    const remember=reviewControl(env,'anchor','remember');remember.checked=true;await remember.fire('change');
    assert.equal(updates(env).at(-1).body.remember,true);assert.deepEqual(updates(env).at(-1).body.related_series_ids,['authorized-other']);
    await controls(env,'remove-binding')[0].fire('click');await flush();
    assert.equal(updates(env).at(-1).body.series_id,'primary-choice');assert.deepEqual(updates(env).at(-1).body.related_series_ids,[]);
    assert.equal(searches(env).length,1);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(item({related_bindings:[linked]}))});
    assert.equal(searches(env).length,2);await choose(env,0,0);
    assert.equal(updates(env).at(-1).body.action,'confirm','Clearing the primary preserves other descriptive bindings');
    assert.equal(updates(env).at(-1).body.series_id,null);assert.deepEqual(updates(env).at(-1).body.related_series_ids,['related-choice']);
    assert.equal(searches(env)[0].value,'');assert.equal(searches(env)[1].value,'Utilization indicator');
    assert.equal(reviewControl(env,'anchor','remember').disabled,false,'An extras-only group can still be remembered');
    await choose(env,1,0);assert.equal(updates(env).at(-1).body.action,'keep_text');assert.deepEqual(updates(env).at(-1).body.related_series_ids,[]);
    env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(item({related_bindings:[linked]})),catalogDeferred:true});
    const second=searches(env)[1];second.value='slow';await second.fire('input');await new Promise(resolve=>setTimeout(resolve,230));
    assert.equal(env.catalogReplies.length,1);await controls(env,'remove-binding')[0].fire('click');await flush();
    env.catalogReplies[0]();await flush();
    assert.equal(searches(env).length,1);assert.equal(searches(env)[0].value,'Supply indicator');
    assert.equal(env.nodes['logic-review-items'].textContent.includes('Stale catalog reply'),false,'Removed rows cannot receive old asynchronous options');
    env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(item({kind:'thresholds',rule:{operator:'>',value_literal:'12',type:'观察位',horizon:'短期'},related_bindings:[linked]}))});
    assert.equal(searches(env).length,2);assert.match(env.nodes['logic-review-items'].textContent,/计算指标/);
    assert.match(env.nodes['logic-review-items'].textContent,/指标值高于 12 kg/);
    await choose(env,1);assert.equal(updates(env).at(-1).body.series_id,'primary-choice');
    assert.match(env.nodes['logic-review-items'].textContent,/指标值高于 12 kg/,'A related indicator never replaces the quantitative operand');
    env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:[{id:'contradiction',title:'主矛盾',items:[item(),item({id:'another'})]}]});
    await reviewControl(env,'anchor','add-binding').fire('click');
    const inputs=descendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewControl==='search');
    assert.equal(inputs.length,3);assert.equal(new Set(inputs.map(node=>node.id)).size,3,'Adding a row does not duplicate another item’s combobox/list IDs');
    env.context.window.JudgmentManager.close();
  }
  {
    const extra=Array.from({length:7},(_,index)=>({series_id:'extra-'+index,name:'Related '+index,unit:'kg'}));
    const env=await generateReview({reviewSections:sections(item({related_bindings:extra}))});
    assert.equal(searches(env).length,8);assert.equal(reviewControl(env,'anchor','add-binding').disabled,true);
    assert.equal(updates(env).length,0);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(item({binding:null,related_bindings:[]}))});
    await reviewControl(env,'anchor','add-binding').fire('click');await choose(env,1);
    await reviewControl(env,'anchor','add-binding').fire('click');
    const third=searches(env)[2];await third.fire('focus');await third.fire('keydown',{key:'ArrowDown'});await third.fire('keydown',{key:'ArrowDown'});
    const before=updates(env).length;await third.fire('keydown',{key:'Enter'});await flush();
    assert.equal(updates(env).length,before,'An indicator already bound in this item cannot be bound twice');
    assert.match(env.nodes['logic-review-items'].textContent,/已.*绑定|已.*关联/);env.context.window.JudgmentManager.close();
  }
  console.log('Multiple binding rows: compact default, add/remove, group memory, null primary, isolated search, rule operands and limits passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

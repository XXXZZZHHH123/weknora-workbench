const assert = require('node:assert/strict');
const test = require('node:test');
const {generateReview, reviewControl, descendants, flush} = require('./judgment-manager-ui.cjs');
const related = {series_id:'synthetic-related',name:'Synthetic related indicator',unit:'%'};
const item = overrides => ({id:'anchor',kind:'thresholds',title:'测试数值条件',description:'Synthetic quantity condition.',
  status:'pending',requires_attention:true,attention_fields:['condition'],can_bind:true,can_add_binding:true,
  binding:null,related_bindings:[related],rule:{operator:'>',value_literal:'17.5'},warnings:[],
  caliber:{catalog_unit:null,model_unit:'t',frequency_required:false},...overrides});
const sections = row => [{id:'rules',title:'核对条件',items:[row]}];
const updates = env => env.requests.filter(row=>row.url.endsWith('/review/update'));
const control = (env,name) => reviewControl(env,'anchor',name) || descendants(env.nodes['judgment-manager']).find(node=>node.dataset.reviewItem==='anchor'&&node.dataset.reviewControl===name);
const modal = env => descendants(env.nodes['judgment-manager']).find(node=>node.className==='logic-confirm-dialog'&&node.open);
async function prepare(t,options={}) {
  const env=await generateReview({numberReview:true,reviewSections:sections(item()),...options});
  t.after(()=>env.context.window.JudgmentManager.close());return env;
}
async function choose(env) {
  const search=control(env,'search');await search.fire('focus');
  await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});
  await search.fire('keydown',{key:'Enter'});await flush();
}

test('a. accepting number review resends the same request with its token',async t=>{
  const env=await prepare(t);await choose(env);
  const changes=updates(env);assert.equal(changes.length,2);
  assert.equal(changes[0].body.number_review_token,undefined);
  assert.equal(changes[1].body.number_review_token,'synthetic-number-token');
  assert.equal(changes[1].body.revision,changes[0].body.revision);
  assert.deepEqual(changes[1].body,{...changes[0].body,number_review_token:'synthetic-number-token'});
  assert.deepEqual(changes[0].body.related_series_ids,['synthetic-related']);
  assert.equal(env.confirmations.length,1);assert.match(env.confirmations[0],/规则数值/);
  assert.match(env.nodes['logic-status'].textContent,/本条已确认/);
});

test('b. cancelling number review keeps the selected indicator and marks the condition',async t=>{
  const env=await prepare(t,{confirmDelete:false});await choose(env);
  assert.equal(updates(env).length,1);assert.equal(env.confirmations.length,1);
  assert.equal(control(env,'search').value,'Directory quantity <script>');
  assert.equal(control(env,'condition').dataset.invalid,'true');
  assert.match(control(env,'condition').textContent,/未确认数值/);
  assert.equal(env.nodes['logic-status'].textContent,'未确认数值，本条保持待核对。');
  assert.equal(env.nodes['logic-status'].dataset.error,'true');
  const card=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewItem==='anchor'&&node.dataset.reviewStatus);
  assert.equal(card.dataset.reviewStatus,'pending');
});

test('c. binding warning followed by number review accumulates both tokens',async t=>{
  const env=await prepare(t,{bindingWarning:true});await choose(env);
  const changes=updates(env);assert.equal(changes.length,3);
  assert.equal(changes[0].body.binding_warning_token,undefined);assert.equal(changes[0].body.number_review_token,undefined);
  assert.deepEqual(changes[1].body,{...changes[0].body,binding_warning_token:'synthetic-warning-token'});
  assert.deepEqual(changes[2].body,{...changes[0].body,binding_warning_token:'synthetic-warning-token',number_review_token:'synthetic-number-token'});
  assert.equal(changes[2].body.revision,changes[0].body.revision);
  assert.equal(env.confirmations.length,2);assert.match(env.confirmations[0],/原绑定单位/);assert.match(env.confirmations[1],/规则数值/);
  assert.match(env.nodes['logic-status'].textContent,/本条已确认/);
});

test('d. non-confirm actions do not prompt or retry number review',async t=>{
  for(const action of ['keep_text','exclude']) {
    const env=await prepare(t);await control(env,action).fire('click');
    assert.equal(updates(env).length,1);assert.equal(updates(env)[0].body.action,action);
    assert.equal(env.confirmations.length,0);assert.match(env.nodes['logic-status'].textContent,/本条未确认.*原文/);
  }
});

for(const accepted of [true,false])test(`whole-item dialog handles number review when ${accepted?'accepted':'cancelled'}`,async t=>{
  const env=await prepare(t,{confirmDelete:accepted,reviewSections:sections(item({binding:{series_id:'synthetic-main',name:'Synthetic main indicator',unit:null}}))});
  await control(env,'confirm-item').fire('click');assert.ok(modal(env));assert.equal(updates(env).length,0);
  control(env,'confirm-unit').value='t';control(env,'confirm-frequency').value='周';control(env,'confirm-basis').value='已核对测试口径';
  await control(env,'confirm-submit').fire('click');
  const changes=updates(env);assert.equal(changes.length,accepted?2:1);assert.equal(env.confirmations.length,1);
  assert.equal(changes[0].body.caliber.unit,'t');assert.equal(changes[0].body.caliber.frequency,'周');
  assert.equal(changes[0].body.caliber.basis,'已核对测试口径');
  if(accepted) {
    assert.deepEqual(changes[1].body,{...changes[0].body,number_review_token:'synthetic-number-token'});assert.equal(modal(env),undefined);
  } else {
    assert.ok(modal(env));assert.match(modal(env).textContent,/未确认数值/);
    assert.equal(control(env,'confirm-unit').value,'t');assert.equal(control(env,'confirm-frequency').value,'周');
    assert.equal(control(env,'search').value,'Synthetic main indicator');
  }
});

test('source quote is preserved when number review resends a confirmation',async t=>{
  const env=await prepare(t,{reviewSections:sections(item({binding:{series_id:'synthetic-main',name:'Synthetic main indicator',unit:'t'}}))});
  const source=env.nodes['logic-review-source'];env.context.window.getSelection=()=>({toString:()=>source.textContent,anchorNode:source,focusNode:source});
  await control(env,'quote').fire('click');await flush();
  const changes=updates(env);assert.equal(changes.length,2);assert.equal(changes[0].body.source_quote,source.textContent);
  assert.deepEqual(changes[1].body,{...changes[0].body,number_review_token:'synthetic-number-token'});
});

for(const code of ['binding_warning','number_review'])test(`a repeated ${code} stops after one resend`,async t=>{
  const env=await prepare(t),transport=env.context.researchFetch;
  env.context.researchFetch=async(url,options={})=>{
    if(!url.endsWith('/review/update'))return transport(url,options);
    env.requests.push({url,body:JSON.parse(options.body)});
    return{ok:false,status:422,json:async()=>({detail:{code,message:'请再次核对测试条件。',confirmation_token:'synthetic-repeat-token',attention_fields:['condition']}})};
  };
  await choose(env);assert.equal(updates(env).length,2);assert.equal(env.confirmations.length,1);
  assert.match(env.nodes['logic-status'].textContent,/本条未确认/);
});

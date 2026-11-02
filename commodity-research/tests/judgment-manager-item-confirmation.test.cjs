const assert = require('node:assert/strict');
const {generateReview, reviewControl, descendants, flush} = require('./judgment-manager-ui.cjs');
const row = overrides => ({id:'anchor',kind:'falsifiers',title:'研究验证条件',description:'A synthetic observation invalidates a claim.',
  status:'pending',requires_attention:true,attention_fields:['condition'],warnings:['请核对本条单位与周期。'],can_bind:true,can_add_binding:true,
  binding:{series_id:'synthetic-main',name:'Synthetic observation',unit:null,frequency:null},related_bindings:[],
  rule:{expression:'change > 0',side:'bull',consecutive_periods:2},
  caliber:{catalog_unit:null,model_unit:'kg',catalog_frequency:null,frequency:null,frequency_required:true,can_declare:true,can_declare_frequency:true},...overrides});
const sections = item => [{id:'falsifiers',title:'证伪条件',items:[item]}];
const control = (env,name) => reviewControl(env,'anchor',name) || descendants(env.nodes['judgment-manager']).find(node=>node.dataset.reviewItem==='anchor'&&node.dataset.reviewControl===name);
const changes = env => env.requests.filter(request=>request.url.endsWith('/review/update'));
const modal = env => descendants(env.nodes['judgment-manager']).find(node=>node.className==='logic-confirm-dialog'&&node.open);
(async()=>{
  {
    const env=await generateReview({reviewSections:sections(row())});
    assert.ok(control(env,'confirm-item'),'Every problem item has one explicit whole-item confirmation entry');
    await control(env,'confirm-item').fire('click');assert.ok(modal(env));
    assert.equal(control(env,'confirm-unit').value,'kg','A model unit is only a visible suggestion for manual verification');
    assert.equal(control(env,'confirm-frequency').value,'','Frequency is never silently inferred from prose or a header');
    await control(env,'confirm-submit').fire('click');assert.equal(changes(env).length,0);
    assert.match(modal(env).textContent,/请选择并核对本条数据频率/,'Required period metadata is requested within the same form');
    control(env,'confirm-unit').value='t';control(env,'confirm-frequency').value='周';
    await control(env,'confirm-submit').fire('click');await flush();
    const body=changes(env).at(-1).body;assert.equal(body.action,'confirm');assert.equal(body.caliber.mode,'declared');
    assert.equal(body.caliber.unit,'t');assert.equal(body.caliber.frequency,'周');assert.match(body.caliber.basis,/用户.*核对/);
    assert.equal(modal(env),undefined);assert.equal(env.requests.filter(request=>request.url.endsWith('/apply')).length,0);
    env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({caliber:{catalog_unit:null,model_unit:null,frequency_required:false},
      rule:{expression:'change > 0',side:'bull',consecutive_periods:1,unit_confirmed:'t'}}))});
    await control(env,'confirm-item').fire('click');assert.equal(control(env,'confirm-unit').value,'t','A rule unit may be proposed but is not treated as source metadata');
    assert.match(modal(env).textContent,/建议.*核对/);await control(env,'confirm-submit').fire('click');
    assert.equal(changes(env).at(-1).body.caliber.mode,'declared');env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({binding:{series_id:'synthetic-main',name:'Placeholder metadata',unit:'—',frequency:'未确认'},
      caliber:{catalog_unit:'—',model_unit:'未注明',catalog_frequency:'未确认',frequency_required:true,can_declare:true,can_declare_frequency:true}}))});
    await control(env,'confirm-item').fire('click');assert.ok(modal(env));assert.equal(control(env,'confirm-unit').value,'');
    assert.equal(control(env,'confirm-frequency').value,'');control(env,'confirm-unit').value='kg';control(env,'confirm-frequency').value='周';
    await control(env,'confirm-submit').fire('click');assert.equal(changes(env).at(-1).body.caliber.mode,'declared');
    assert.equal(changes(env).at(-1).body.caliber.frequency,'周');env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({binding:{series_id:'old-main',name:'Old source',unit:'kg',frequency:'日'},
      caliber:{catalog_unit:'kg',unit:'kg',model_unit:'—',catalog_frequency:'日',frequency:'日',frequency_required:false}})),reviewFailure:true,
      catalogEntries:[{series_id:'new-source',name:'New source without metadata',unit:'—',frequency:'未确认'}]});
    const picker=control(env,'search');await picker.fire('focus');await picker.fire('keydown',{key:'ArrowDown'});await picker.fire('keydown',{key:'ArrowDown'});await picker.fire('keydown',{key:'Enter'});
    await control(env,'confirm-item').fire('click');assert.ok(modal(env));
    assert.equal(control(env,'confirm-unit').value,'','An old primary source unit is not adopted for a newly selected source');
    assert.equal(control(env,'confirm-frequency').value,'','An old primary frequency is not adopted for a newly selected source');
    env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({binding:{series_id:'old-main',name:'Old source',unit:'kg',frequency:'日'},
      caliber:{catalog_unit:'kg',unit:'kg',catalog_frequency:'日',frequency:'日',frequency_required:false}})),reviewFailure:true,reviewFrequencyRequired:true,
      catalogEntries:[{series_id:'new-source',name:'New source with no cadence',unit:'kg',frequency:null}]});
    const picker=control(env,'search');await picker.fire('focus');await picker.fire('keydown',{key:'ArrowDown'});await picker.fire('keydown',{key:'ArrowDown'});await picker.fire('keydown',{key:'Enter'});
    await control(env,'confirm-item').fire('click');assert.ok(modal(env),'A failed change uses the current candidate’s server frequency requirement, not the old source flag');
    assert.equal(control(env,'confirm-unit'),undefined);assert.equal(control(env,'confirm-frequency').value,'');
    control(env,'confirm-frequency').value='周';await control(env,'confirm-submit').fire('click');
    assert.equal(changes(env).at(-1).body.series_id,'new-source');assert.equal(changes(env).at(-1).body.caliber.mode,'catalog');
    assert.equal(changes(env).at(-1).body.caliber.frequency,'周');env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({binding:{series_id:'synthetic-main',name:'Known-unit observation',unit:'kg'},
      caliber:{catalog_unit:'kg',catalog_frequency:null,frequency:null,frequency_required:true,can_adopt_catalog:true,can_declare_frequency:true}}))});
    await control(env,'confirm-item').fire('click');assert.equal(control(env,'confirm-unit'),undefined,'Known source units are adopted without an editable replacement');
    control(env,'confirm-frequency').value='月';await control(env,'confirm-submit').fire('click');
    const body=changes(env).at(-1).body;assert.equal(body.caliber.mode,'catalog');assert.equal(body.caliber.frequency,'月');
    assert.equal(Object.hasOwn(body.caliber,'unit'),false);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({kind:'thresholds',rule:{operator:'>',value_literal:'12'},
      binding:{series_id:'synthetic-main',name:'Known-unit observation',unit:'kg'},
      caliber:{catalog_unit:'kg',catalog_frequency:null,frequency:null,frequency_required:false,can_declare_frequency:true}}))});
    await control(env,'confirm-item').fire('click');assert.equal(modal(env),undefined,'A static level does not require a redundant frequency form');
    assert.equal(changes(env).length,1);assert.equal(changes(env)[0].body.action,'confirm');
    assert.equal(Object.hasOwn(changes(env)[0].body,'caliber'),false);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({kind:'thresholds',rule:{operator:'>',value_literal:'12'},
      caliber:{catalog_unit:null,model_unit:'kg',catalog_frequency:null,frequency:null,frequency_required:false,can_declare:true,can_declare_frequency:true}}))});
    await control(env,'confirm-item').fire('click');assert.ok(modal(env));
    assert.equal(control(env,'confirm-frequency').value,'');await control(env,'confirm-submit').fire('click');
    assert.equal(changes(env).length,1);assert.equal(changes(env)[0].body.caliber.unit,'kg');
    assert.equal(Object.hasOwn(changes(env)[0].body.caliber,'frequency'),false,'Optional unknown frequency may remain empty');
    env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row({kind:'binding',rule:undefined,binding:null,caliber:{}}))});
    await control(env,'confirm-item').fire('click');assert.equal(modal(env),undefined);assert.equal(changes(env).length,1);
    assert.equal(changes(env)[0].body.action,'confirm','Ordinary research text does not require a metadata form');env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({reviewSections:sections(row()),reviewFailure:true});
    await control(env,'confirm-item').fire('click');control(env,'confirm-unit').value='t';control(env,'confirm-frequency').value='周';
    await control(env,'confirm-submit').fire('click');assert.ok(modal(env),'Rejected numbers or metadata never become a fake confirmation');
    assert.equal(control(env,'confirm-unit').value,'t');assert.equal(control(env,'confirm-frequency').value,'周');
    assert.match(modal(env).textContent,/当前修改已保留/);assert.equal(env.nodes['logic-apply'].disabled,true);
    await control(env,'confirm-cancel').fire('click');assert.equal(modal(env),undefined);
    await control(env,'confirm-item').fire('click');assert.equal(control(env,'confirm-unit').value,'t','Cancelled manual edits are retained locally for correction');
    env.context.window.JudgmentManager.close();assert.equal(modal(env),undefined,'Closing the manager also closes a pending confirmation form');
  }
  console.log('Whole-item confirmation: explicit entry, unit/frequency form, known source caliber, direct text confirmation and rejected-edit retention passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

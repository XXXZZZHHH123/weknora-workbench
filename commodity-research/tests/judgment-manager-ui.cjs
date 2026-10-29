const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/judgment-manager.js'), 'utf8');
class Element {
  constructor(id='') { this.id=id;this.dataset={};this.attributes={};this.children=[];this.listeners={};this.value='';this.textContent='';this.hidden=false;this.disabled=false;this.open=false; }
  get textContent(){return (this._text||'')+this.children.map(child=>child.textContent).join('');}
  set textContent(value){this._text=String(value);this.children=[];}
  set innerHTML(_) { throw new Error('Unsafe HTML rendering attempted'); }
  addEventListener(type,fn) { this.listeners[type]=fn; }
  async fire(type,event={}) { return this.listeners[type]?.({target:this,preventDefault(){},...event}); }
  replaceChildren(...children){this.children=children;}
  append(...children){this.children.push(...children);}
  setAttribute(name,value){this.attributes[name]=value;}
  removeAttribute(name){delete this.attributes[name];}
  contains(node){return node===this||this.children.some(child=>child.contains(node));}
  focus(){}
  showModal(){this.open=true;}
  close(){this.open=false;}
  click(){}
}
function setup({viewer=false,failure=false,stale=false,empty=false,panelMode=false,savedId=false,reviewMode=false,reviewFailure=false,catalogDeferred=false,emptyExcerpt=false,ruleEntries=null,readyMode=false,reviewConflict=false,reviewDeferred=false}={}) {
  const ids=['judgment-manager',...['original-source','reuse-original','input','status','preview','generate','apply','file','skeleton','structured','variety','reuse','model','current-md','current-state','version-label','version','version-range','file-name','download','version-number','load-version','history-md','history-diff','preview-md','generated-diff','coverage','bindings','unresolved','field-diff'].map(x=>'logic-'+x)];
  const nodes=Object.fromEntries(ids.map(id=>[id,new Element(id)]));
  for(const name of ['import','review','review-nav','review-items','review-count','review-reuse','review-note','review-edit','review-save-note','review-ready','review-source','review-attention','review-all','review-empty'])nodes['logic-'+name]=new Element('logic-'+name);
  if(panelMode) for(const name of ['panel-switch','panel-open','panel-name','panel-save','panel-range','panel-more','new-panel-title'])nodes['logic-'+name]=new Element();
  nodes['judgment-manager'].dataset.prefill='false';
  if(panelMode)nodes['judgment-manager'].dataset.judgmentId='42';
  const tabs=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicTab=name;return el;});
  const panels=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicPanel=name;return el;});
  const close=new Element();
  nodes['judgment-manager'].querySelectorAll=selector=>selector==='[data-logic-tab]'?tabs:panels;
  nodes['judgment-manager'].querySelector=selector=>selector==='[data-logic-close]'?close:tabs[1];
  const requests=[],navigations=[],catalogReplies=[],reviewReplies=[];let reloads=0,generations=0,revision=1;
  const catalog=[{series_id:'authorized-other',name:'Directory quantity <script>',unit:'t',source_file:'Shared workbook.xlsx',sheet:'Daily'},{series_id:'authorized-page-two',name:'Another authorized field',unit:'t',source_file:'Other workbook.xlsx',sheet:'Weekly'}];
  const review={draft_id:'server-bound-nonce',revision:1,sections:[{id:'contradiction',title:'主矛盾',items:[{id:'anchor',kind:'binding',title:'数据锚',original_text:'Supply quantity <script>',description:'请核对数据来源与口径。',status:'pending',requires_binding:true,can_bind:true,can_confirm:true,warnings:[],binding:null,ai_binding:{series_id:'ai-choice',name:'AI quantity',unit:'kg',source_file:'Initial workbook.xlsx',sheet:'Table'}}]},{id:'rules',title:'阈值与证伪',items:[{id:'rule',kind:'thresholds',rule:{operator:'>',value_literal:'12',type:'观察位',horizon:'短期'},title:'阈值条件',original_text:'A condition without a unit',status:'pending',requires_binding:false,can_bind:false,can_confirm:false,attention_fields:['condition'],attention_reasons:['原文缺少阈值单位，请补充原文或保留文字。'],warnings:['原文缺少阈值单位，请补充原文或保留文字。']},{id:'note',kind:'text',title:'研究说明',original_text:'Keep this observation',status:'pending',requires_binding:false,can_bind:false,can_confirm:true,warnings:[]}]}],pending_count:3,confirmed_count:0,total:3};
  if(emptyExcerpt)review.sections[0].items[0].original_text='';
  if(ruleEntries){review.sections[1].items=ruleEntries.map(row=>({title:'解析条件',original_text:'Exact research evidence',description:'请核对解析后的条件。',status:'pending',requires_binding:true,can_bind:true,can_confirm:false,warnings:[],binding:{series_id:'condition-unit',name:'研究数量',unit:'吨'},...row}));review.total=review.pending_count=1+ruleEntries.length;}
  if(readyMode){review.sections[0].items[0].status='ready';review.sections[1].items[1].status='ready';review.sections.push({id:'time_dimension',title:'时间维度',items:[{id:'time-text',kind:'description',title:'适用周期',description:'短期关注供需变化，中期观察成本约束。',status:'ready',original_text:'',can_confirm:true,can_bind:false,requires_binding:false,warnings:[]}]});review.total=4;review.pending_count=review.needs_attention_count=1;review.ready_count=3;}
  if(readyMode==='all'){review.sections[1].items[0].status='ready';review.sections[1].items[0].warnings=[];review.pending_count=review.needs_attention_count=0;review.ready_count=4;}
  function preview(){return {draft_id:review.draft_id,base_version:2,markdown:'# Proposed',diff:{markdown:'-old\n+new',fields:[{label:'Rule',before:null,after:'<img onerror=bad>'}]},bindings:[],unresolved:[],candidate_count:1,catalog_total:5,review_available:true,review:JSON.parse(JSON.stringify(review))};}
  const window={RESEARCH_PREFIX:'/research/workbenches/synthetic'};
  const context={window,parent:window,location:{origin:'http://unit.test',href:'http://unit.test/research/workbenches/synthetic/sn/judgment?date=2024-03-01&judgment_id=42',reload(){reloads++;},assign(url){navigations.push(url);}},
    document:{getElementById:id=>nodes[id],createElement:()=>new Element(),querySelectorAll:()=>[]},
    TextEncoder,TextDecoder,AbortController,Blob,URL,URLSearchParams,Option:Element,console,setTimeout,clearTimeout,
    async researchFetch(url,options={}) {
      requests.push({url,body:options.body?JSON.parse(options.body):undefined,method:options.method,signal:options.signal});
      let result;
      if(url.includes('/review/indicators')){
        if(catalogDeferred&&url.includes('search=slow'))await new Promise(resolve=>catalogReplies.push(resolve));
        result={items:url.includes('search=slow')?[{series_id:'stale-result',name:'Stale catalog reply',unit:'kg'}]:url.includes('offset=1')?[catalog[1]]:[catalog[0]],total:2,next_offset:url.includes('offset=1')?null:1};
      }
      else if(url.endsWith('/review/update')){
        if(reviewDeferred)await new Promise(resolve=>reviewReplies.push(resolve));
        if(reviewConflict)return {ok:false,status:409,json:async()=>({detail:'来源已改变，请重新分析。'})};
        if(reviewFailure)return {ok:false,status:422,json:async()=>({detail:{message:'请选择有可核对单位的指标，当前修改已保留。',attention_fields:['binding']}})};
        const body=JSON.parse(options.body),item=review.sections.flatMap(section=>section.items).find(row=>row.id===body.item_id);
        item.status={confirm:'confirmed',keep_text:'kept_text',exclude:'excluded'}[body.action];
        if(body.series_id)item.binding=catalog.find(row=>row.series_id===body.series_id)||item.ai_binding;
        if(body.source_quote)item.source_quote=body.source_quote;
        item.remembered=body.remember;review.pending_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='pending').length;review.confirmed_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='confirmed').length;if(Object.hasOwn(review,'needs_attention_count'))review.needs_attention_count=review.pending_count;review.revision=++revision;review.draft_id='server-bound-nonce-r'+revision;result=preview();
      }
      else if(url.endsWith('/review/reuse')){const enabled=JSON.parse(options.body).enabled,item=review.sections[0].items[0];review.reuse_enabled=enabled;item.binding=enabled?catalog[1]:null;item.remembered=enabled;item.status=enabled?'pending':readyMode?'ready':'pending';item.origin=enabled?'remembered':'model';review.pending_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='pending').length;if(Object.hasOwn(review,'needs_attention_count'))review.needs_attention_count=review.pending_count;review.revision=++revision;result=preview();}
      else if(url.endsWith('/review/confirm-ready')){for(const item of review.sections.flatMap(section=>section.items))if(item.can_confirm)item.status='confirmed';review.pending_count=1;review.confirmed_count=2;review.revision=++revision;result=preview();}
      else if(url.includes('/review?'))result=JSON.parse(JSON.stringify(review));
      else if(url.includes('/panels')&&options.method==='PATCH')result={id:'named-panel',judgment_id:42,version:1,title:JSON.parse(options.body).title,revision:2,named:true};
      else if(url.endsWith('/panels'))result={items:[{id:'named-panel',judgment_id:42,version:1,title:'<img onerror=unsafe>',revision:1,named:true},{id:null,judgment_id:43,version:2,title:'Other independent research',revision:null,named:false}],total:2,offset:0,has_more:false};
      else if(url.endsWith('/manage')&&empty) result={variety:'SN',current:null,latest:null,versions:[],version_total:0,can_write:!viewer};
      else if(url.endsWith('/manage')||url.includes('/manage?judgment_id=')) result={variety:'SN',current:{id:42,version:1,status:'生效',author:'r',markdown:'# Current',source_text:'# Original input'},latest:{version:stale&&generations?3:2},versions:[{version:2,status:'草稿'},{version:1,status:'生效'}],version_total:2,can_write:!viewer};
      else if(url.includes('/manage?variety='))result={variety:'XY',current:null,latest:null,versions:[],version_total:0,can_write:!viewer};
      else if(url.endsWith('/models')) result={models:[{id:'configured',name:'Configured chat'}]};
      else if(url.endsWith('/generate')) {
        generations++;
        if(stale&&generations===1)return {ok:false,status:409,json:async()=>({detail:'Synthetic version changed'})};
        if(failure)return {ok:false,json:async()=>({detail:'Synthetic unavailable; input retained'})};
        result=reviewMode?preview():{draft_id:'server-bound-nonce',base_version:2,markdown:'# Proposed',diff:{markdown:'-old\n+new',fields:[{label:'Rule',before:null,after:'<img onerror=bad>'}]},bindings:[{name:'Actual quantity',unit:'kg'}],unresolved:[],candidate_count:1,catalog_total:5,candidate_subset:true,notice:'Preview only'};
      } else if(url.endsWith('/structured/preview'))result={kind:'structured',draft_id:'typed-bound-nonce',base_version:0,markdown:'# Typed research',diff:{markdown:'',fields:[]},bindings:[],unresolved:[],candidate_count:0,catalog_total:0,notice:'Typed preview'};
      else if(url.endsWith('/apply'))result={...(savedId?{id:44}:{}),version:3,status:'草稿',notice:'Saved draft'};
      else result={markdown:'# Historical',diff:{markdown:'-Current\n+Historical'}};
      return {ok:true,json:async()=>result};
    }};
  vm.runInNewContext(source,context);
  return {nodes,requests,context,navigations,catalogReplies,reviewReplies,reloads:()=>reloads};
}
function descendants(node){return node.children.flatMap(child=>[child,...descendants(child)]);}
function visibleDescendants(node){return node.hidden?[]:node.children.flatMap(child=>child.hidden?[]:[child,...(child.className?.includes('logic-item-adjustments')&&!child.open?[]:visibleDescendants(child))]);}
function reviewControl(env,itemId,control){return descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewItem===itemId&&node.dataset.reviewControl===control);}
async function generateReview(options={}){const env=setup({reviewMode:true,...options});await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value=options.source||'Full research source';env.nodes['logic-model'].value='configured';await env.nodes['logic-generate'].fire('click');return env;}
(async()=>{
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-current-md'].textContent,'# Current');
    assert.match(env.nodes['logic-version-label'].textContent,/后续保存仍为草稿/);
    assert.doesNotMatch(env.nodes['logic-version-label'].textContent,/v2/);
    assert.equal(env.requests.filter(r=>r.url.endsWith('/generate')).length,0,'Opening a manager never invokes a model');
    env.nodes['logic-input'].value='Complete private source';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.nodes['logic-preview'].hidden,false);
    assert.equal(env.requests.filter(r=>r.url.endsWith('/apply')).length,0,'Generated preview does not save automatically');
    await env.nodes['logic-apply'].fire('click');
    const body=env.requests.find(r=>r.url.endsWith('/apply')).body;
    assert.deepEqual(Object.keys(body).sort(),['base_version','draft_id']);
    assert.equal(body.base_version,2);assert.equal(env.reloads(),1);
  }
  {
    const env=setup({failure:true});await env.context.window.JudgmentManager.open();
    env.nodes['logic-input'].value='Private source <script>never rendered</script>';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.nodes['logic-input'].value,'Private source <script>never rendered</script>');
    assert.equal(env.nodes['logic-status'].dataset.error,'true');
    assert.equal(env.nodes['logic-preview'].hidden,true);
  }
  {
    const env=setup();await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value='Source';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    env.nodes['logic-input'].value='Changed source';await env.nodes['logic-input'].fire('input');
    await env.nodes['logic-apply'].fire('click');
    assert.equal(env.requests.filter(r=>r.url.endsWith('/apply')).length,0,'Changed input invalidates the old preview');
  }
  {
    const env=setup({viewer:true});await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-generate'].disabled,true);
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.requests.filter(r=>r.url.endsWith('/models')||r.url.endsWith('/generate')).length,0);
  }
  {
    const env=setup();await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value='Keep existing text';
    await env.nodes['logic-reuse'].fire('click');
    assert.equal(env.nodes['logic-input'].value,'Keep existing text','Reuse cannot silently overwrite user input');
    env.context.window.JudgmentManager.close();assert.equal(env.nodes['judgment-manager'].open,false);
  }
  {
    const env=setup({stale:true});await env.context.window.JudgmentManager.open();
    env.nodes['logic-input'].value='Keep complete source';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.nodes['logic-input'].value,'Keep complete source');
    assert.equal(env.nodes['logic-preview'].hidden,true);
    assert.equal(env.requests.filter(r=>r.url.endsWith('/generate')).length,1,'A conflict does not automatically call the model again');
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.requests.filter(r=>r.url.endsWith('/generate'))[1].body.base_version,3,'Retry uses the freshly verified latest version');
  }
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-original-source'].textContent,'# Original input');
    await env.nodes['logic-reuse-original'].fire('click');
    assert.equal(env.nodes['logic-input'].value,'# Original input');
    env.nodes['logic-input'].value='Keep existing';await env.nodes['logic-reuse-original'].fire('click');
    assert.equal(env.nodes['logic-input'].value,'Keep existing');
  }
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    env.nodes['logic-variety'].value='XY';env.nodes['logic-input'].value='New explicit product';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    const generated=env.requests.find(r=>r.url.endsWith('/generate'));
    assert.equal(generated.body.variety,'XY');assert.equal(generated.body.base_version,0);
    assert.equal(generated.body.text,'New explicit product');
  }
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    env.nodes['logic-input'].value=JSON.stringify({variety:'XY',contradiction:{}});
    await env.nodes['logic-structured'].fire('click');
    assert.equal(env.nodes['logic-preview'].hidden,false);
    assert.equal(env.requests.filter(r=>r.url.endsWith('/generate')).length,0);
    assert.equal(env.requests.find(r=>r.url.endsWith('/structured/preview')).body.payload.variety,'XY');
    await env.nodes['logic-apply'].fire('click');
    assert.equal(env.requests.filter(r=>r.url.endsWith('/structured/apply')).length,1);
  }
  {
    const env=setup({empty:true});await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-variety'].value,'','An empty workbench must not silently inherit the legacy product');
    env.nodes['logic-input'].value='Complete source for a new product';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.requests.filter(r=>r.url.endsWith('/generate')).length,0,'Missing product identity must not spend a model call');
    assert.match(env.nodes['logic-status'].textContent,/品种代码/);
  }
  {
    const env=setup({panelMode:true});await env.context.window.JudgmentManager.open();
    assert.ok(env.requests.some(r=>r.url.endsWith('/manage?judgment_id=42')),'Manage reads the displayed immutable judgment');
    assert.match(env.nodes['logic-panel-switch'].children[0].textContent,/<img onerror=unsafe>/,'Panel titles render as safe text');
    env.nodes['logic-panel-switch'].value='43';await env.nodes['logic-panel-open'].fire('click');
    assert.match(env.navigations[0],/date=2024-03-01/);assert.match(env.navigations[0],/judgment_id=43/);
    assert.equal(env.requests.filter(r=>r.url.endsWith('/apply')||r.url.endsWith('/generate')).length,0,'Switching never activates or imports a judgment');
  }
  {
    const env=setup({panelMode:true});await env.context.window.JudgmentManager.open();
    env.nodes['logic-panel-name'].value='Named research';await env.nodes['logic-panel-save'].fire('click');
    const renamed=env.requests.find(r=>r.method==='PATCH');assert.ok(renamed);assert.equal(renamed.body.revision,1);assert.equal(renamed.body.title,'Named research');
    env.context.window.JudgmentManager.close();await env.context.window.JudgmentManager.open();
    const reopened=env.requests.filter(r=>r.url.includes('/manage')).at(-1);assert.equal(reopened.signal.aborted,false,'Reopening must not reuse a cancelled request');
  }
  {
    const env=setup({panelMode:true,savedId:true});await env.context.window.JudgmentManager.open();
    env.nodes['logic-new-panel-title'].value='Independent second panel';env.nodes['logic-input'].value='Complete source';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');assert.equal(env.requests.find(r=>r.url.endsWith('/generate')).body.panel_title,'Independent second panel');
    env.nodes['logic-panel-switch'].value='43';await env.nodes['logic-panel-open'].fire('click');assert.equal(env.navigations.length,0,'Switching cannot discard source input');
    await env.nodes['logic-apply'].fire('click');assert.match(env.navigations[0],/judgment_id=44/);assert.equal(env.reloads(),0,'Saved draft opens its own panel');
  }
  {
    const env=await generateReview();
    assert.equal(env.nodes['logic-review'].hidden,false);assert.equal(env.nodes['logic-import'].hidden,true);
    assert.equal(env.nodes['logic-review-all'].attributes['aria-pressed'],'true');assert.match(env.nodes['logic-review-count'].textContent,/需处理 3 项/);
    assert.equal(env.nodes['logic-apply'].disabled,false,'A draft may retain unresolved nonexecuting candidates');
    assert.match(env.nodes['logic-review-save-note'].textContent,/未通过校验的规则不会参与执行/);
    assert.equal(reviewControl(env,'rule','confirm'),undefined,'There is no per-item confirmation chore');
    const field=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewField==='condition');assert.equal(field.dataset.invalid,'true');
    assert.match(field.textContent,/原文缺少阈值单位/);assert.equal(reviewControl(env,'anchor','remember').checked,false);
  }
  {
    const env=await generateReview(),search=reviewControl(env,'anchor','search');await search.fire('focus');
    assert.ok(env.requests.some(row=>row.url.includes('/review/indicators?')),'Search covers the complete authorized directory');
    search.value='Directory';await search.fire('input');await new Promise(resolve=>setTimeout(resolve,260));assert.ok(env.requests.some(row=>row.url.includes('search=Directory')));
    await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    let updates=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(updates.length,1,'Choosing a field synchronizes the private draft directly');assert.equal(updates[0].body.action,'confirm');assert.equal(updates[0].body.series_id,'authorized-other');assert.equal(updates[0].body.remember,false);
    const remember=reviewControl(env,'anchor','remember');remember.checked=true;await remember.fire('change');updates=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(updates.at(-1).body.remember,true);assert.equal(updates.at(-1).body.revision,2);assert.equal(reviewControl(env,'anchor','remember').checked,true,'A successful explicit remember action remains visibly checked in this draft');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0,'Private synchronization is never a business save');
    await reviewControl(env,'rule','keep_text').fire('click');await reviewControl(env,'note','exclude').fire('click');await env.nodes['logic-apply'].fire('click');
    const applied=env.requests.find(row=>row.url.endsWith('/apply')).body;assert.equal(applied.draft_id,'server-bound-nonce-r5');assert.equal(applied.review_revision,5,'Final save uses the newest reviewed revision');
  }
  {
    const env=await generateReview({reviewFailure:true}),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    assert.equal(reviewControl(env,'anchor','search').value,'Directory quantity <script>','422 retains the selected field');
    const field=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewField==='binding');assert.equal(field.dataset.invalid,'true');assert.match(field.textContent,/当前修改已保留/);
    search.value='Retry';await search.fire('input');assert.equal(field.dataset.invalid,'true','Typing after a rejection does not clear the red field before validation succeeds');
    assert.equal(env.nodes['logic-apply'].disabled,true);await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview();env.nodes['logic-review-reuse'].checked=true;await env.nodes['logic-review-reuse'].fire('change');
    assert.equal(env.requests.find(row=>row.url.endsWith('/review/reuse')).body.enabled,true);assert.equal(reviewControl(env,'anchor','search').value,'Another authorized field');assert.equal(reviewControl(env,'anchor','remember').checked,false);
    await env.nodes['logic-review-edit'].fire('click');env.nodes['logic-input'].value='Changed full source';await env.nodes['logic-input'].fire('input');await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview(),search=reviewControl(env,'anchor','search');await search.fire('focus');assert.equal(env.requests.filter(row=>row.url.includes('/review/indicators')).length,1);
    await reviewControl(env,'anchor','more').fire('click');assert.match(env.requests.filter(row=>row.url.includes('/review/indicators')).at(-1).url,/offset=1.*limit=20/);assert.equal(reviewControl(env,'anchor','more').hidden,true);
    await search.fire('keydown',{key:'ArrowUp'});await search.fire('keydown',{key:'Enter'});assert.equal(reviewControl(env,'anchor','search').value,'Another authorized field');
    await search.fire('keydown',{key:'Escape'});assert.equal(search.attributes['aria-expanded'],'false');
  }
  {
    const env=await generateReview({catalogDeferred:true}),search=reviewControl(env,'anchor','search');search.value='slow';await search.fire('input');await new Promise(resolve=>setTimeout(resolve,220));search.value='fast';await search.fire('input');await new Promise(resolve=>setTimeout(resolve,220));
    assert.equal(env.requests.find(row=>row.url.includes('search=slow')).signal.aborted,true);env.catalogReplies[0]();await new Promise(resolve=>setTimeout(resolve,10));await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});assert.equal(reviewControl(env,'anchor','search').value,'Directory quantity <script>','Late search replies cannot replace the current result');
  }
  {
    const raw='Exact raw source with `literal evidence`.',env=await generateReview({emptyExcerpt:true,source:raw});
    const card=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewItem==='anchor'&&!node.dataset.reviewControl),details=descendants(card).find(node=>node.className==='logic-item-full-source'),source=details.children[1];assert.equal(source.textContent,'');details.open=true;await details.fire('toggle');assert.equal(source.textContent,raw);
    env.context.window.getSelection=()=>({anchorNode:source,focusNode:source,toString:()=> '`literal evidence`'});let preserved=false;await reviewControl(env,'anchor','quote').fire('mousedown',{preventDefault(){preserved=true;}});if(!preserved)env.context.window.getSelection=()=>({toString:()=>''});await reviewControl(env,'anchor','quote').fire('click');
    assert.equal(env.requests.find(row=>row.url.endsWith('/review/update')).body.source_quote,'`literal evidence`','Selecting original text synchronizes the exact quote without cleaning');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
    env.context.window.getSelection=()=>({anchorNode:env.nodes['logic-input'],focusNode:env.nodes['logic-input'],toString:()=> 'outside'});await reviewControl(env,'rule','quote').fire('click');assert.match(env.nodes['logic-status'].textContent,/原文/);
  }
  {
    const env=await generateReview({reviewDeferred:true}),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});const updating=search.fire('keydown',{key:'Enter'});assert.equal(env.nodes['logic-apply'].disabled,true,'A network-pending edit prevents saving');env.reviewReplies[0]();await updating;assert.equal(env.nodes['logic-apply'].disabled,false);
    env.context.window.JudgmentManager.close();await env.context.window.JudgmentManager.open();const reopened=reviewControl(env,'anchor','search');await reopened.fire('focus');assert.ok(env.requests.filter(row=>row.url.includes('/review/indicators')).length>=2);assert.equal(env.requests.filter(row=>row.url.endsWith('/generate')).length,1,'Reopening never reruns the model');
  }
  {
    const env=await generateReview({reviewConflict:true}),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});assert.equal(env.nodes['logic-preview'].hidden,true,'A source/version conflict invalidates the stale draft');assert.equal(env.nodes['logic-input'].value,'Full research source');await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview({ruleEntries:[
      {id:'large',kind:'thresholds',rule:{operator:'>=',value_literal:'900719925474099312345.123456789',type:'风控线',horizon:'中长期'}},
      {id:'range',kind:'thresholds',rule:{operator:'between',value_literal:'[-0.000000000000000123,900719925474099312345.123456789]',type:'观察位',horizon:'短期'}},
      {id:'zero',kind:'thresholds',rule:{operator:'<=',value_literal:'0',type:'介入位',horizon:'短期'}},
      {id:'scientific',kind:'thresholds',rule:{operator:'<',value_literal:'-1.234567890123456789e-23',type:'观察位',horizon:'短期'}}]});
    assert.match(reviewControl(env,'large','condition')?.textContent||'',/不低于 900719925474099312345\.123456789 吨/,'Rule values retain exact numeric literals without browser rounding');
    assert.match(reviewControl(env,'large','condition').textContent,/风控线.*中长期/);
    assert.match(reviewControl(env,'range','condition').textContent,/不低于 -0\.000000000000000123 吨.*不高于 900719925474099312345\.123456789 吨.*含上下界/);
    assert.match(reviewControl(env,'zero','condition').textContent,/不高于 0 吨/,'An invalid zero remains visible instead of disappearing');
    assert.match(reviewControl(env,'scientific','condition').textContent,/低于 -1\.234567890123456789e-23 吨/,'Scientific literals retain their original digits and exponent');
  }
  {
    const env=await generateReview({ruleEntries:[
      {id:'bull-case',kind:'falsifiers',rule:{expression:'change <= -0.000000000000000123',consecutive_periods:3,side:'bull'}},
      {id:'bear-case',kind:'falsifiers',rule:{expression:'value > 900719925474099312345.123456789',consecutive_periods:2,side:'bear'}}]});
    assert.match(reviewControl(env,'bull-case','condition')?.textContent||'',/较上一数据点的变化值不高于 -0\.000000000000000123 吨.*连续 3 期.*多方原假设失效/);
    assert.doesNotMatch(reviewControl(env,'bull-case','condition').textContent,/空方原假设失效/);
    assert.match(reviewControl(env,'bear-case','condition').textContent,/指标值高于 900719925474099312345\.123456789 吨.*连续 2 期.*空方原假设失效/);
    assert.doesNotMatch(reviewControl(env,'bear-case','condition').textContent,/多方原假设失效/);
  }
  {
    const env=await generateReview({ruleEntries:[
      {id:'unknown-op',kind:'thresholds',rule:{operator:'!=',value_literal:'12'}},
      {id:'unknown-expression',kind:'falsifiers',rule:{expression:'unrecognized <script> expression',consecutive_periods:1,side:'unknown'}},
      ...['null','true','"12"','{"minimum":12}','[12]','[12,13,14]'].map((literal,index)=>({id:'invalid-literal-'+index,kind:'thresholds',rule:{operator:'>',value_literal:literal}}))]});
    for(const id of ['unknown-op','unknown-expression']){assert.match(reviewControl(env,id,'condition')?.textContent||'',/需核对条件/);assert.doesNotMatch(reviewControl(env,id,'condition').textContent,/unrecognized|<script>|operator|value_literal|!=/);}
    for(let index=0;index<6;index++)assert.match(reviewControl(env,'invalid-literal-'+index,'condition').textContent,/需核对条件/,'Malformed candidate numbers are never reformatted into a valid comparison');
  }
  {
    const literal='900719925474099312345.123456789',env=await generateReview({ruleEntries:[{id:'unit-edit',kind:'thresholds',rule:{operator:'>=',value_literal:literal,type:'观察位',horizon:'短期'}}],reviewDeferred:true});
    const condition=reviewControl(env,'unit-edit','condition'),search=reviewControl(env,'unit-edit','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});const updating=search.fire('keydown',{key:'Enter'});
    assert.equal(condition.textContent,'指标值不低于 '+literal+' t；观察位；短期','Unit display updates immediately without rounding while synchronization is pending');env.reviewReplies[0]();await updating;
    const active=reviewControl(env,'unit-edit','search');active.value='';await active.fire('input');assert.equal(reviewControl(env,'unit-edit','condition').textContent,'指标值不低于 '+literal+'（单位待确认）；观察位；短期');assert.equal(env.nodes['logic-apply'].disabled,true);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({readyMode:true});assert.equal(env.nodes['logic-review-all'].attributes['aria-pressed'],'true');assert.match(env.nodes['logic-review-count'].textContent,/已解析 4 项.*需处理 1 项/);
    await env.nodes['logic-review-nav'].children.find(node=>node.dataset.reviewSection==='time_dimension').fire('click');const shown=visibleDescendants(env.nodes['logic-review-items']).map(node=>node.textContent).join(' ');assert.match(shown,/短期关注供需变化，中期观察成本约束。/);assert.doesNotMatch(shown,/尚未定位|依据待选择|确认此项|已解析/,'Plain time descriptions have no confirmation/status clutter');assert.equal(reviewControl(env,'time-text','adjust').open,false);
    await env.nodes['logic-review-attention'].fire('click');const issues=visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem);assert.deepEqual(issues,['rule']);
  }
  {
    const env=await generateReview({readyMode:true,reviewFailure:true});const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});assert.equal(env.nodes['logic-apply'].disabled,true,'An unsuccessful change to a ready binding stays unsynchronized');
    await env.nodes['logic-review-attention'].fire('click');assert.equal(visibleDescendants(env.nodes['logic-review-items']).some(node=>node.dataset.reviewStatus==='ready'&&node.dataset.reviewItem==='anchor'),true);assert.equal(reviewControl(env,'anchor','search').value,'Directory quantity <script>');await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview({readyMode:'all'});assert.equal(env.nodes['logic-review-all'].attributes['aria-pressed'],'true');assert.match(env.nodes['logic-review-save-note'].textContent,/可以保存草稿/);assert.equal(env.nodes['logic-apply'].disabled,false);assert.equal(reviewControl(env,'anchor','confirm'),undefined);assert.equal(env.requests.filter(row=>row.url.endsWith('/review/update')||row.url.endsWith('/review/confirm-ready')).length,0);
  }
  {
    const env=await generateReview({ruleEntries:[{id:'diagnostic',kind:'unresolved_binding',description:'需要观察订单变化。',attention_fields:['binding'],attention_reasons:['可保留文字或补充指标。'],warnings:['{"path":"/tmp/private","code":"internal-diagnostic"}']}]});
    const shown=env.nodes['logic-review-items'].textContent;assert.match(shown,/需要观察订单变化。/);assert.match(shown,/可保留文字或补充指标。/);assert.doesNotMatch(shown,/\/tmp\/private|internal-diagnostic|"path"/,'Model diagnostics never leak into field errors');
  }
  {
    const env=await generateReview({readyMode:true,reviewDeferred:true});
    assert.equal(env.requests.filter(row=>row.url.endsWith('/review/reuse')||row.url.endsWith('/review/update')||row.url.endsWith('/apply')).length,0,'Leaving reuse off never applies remembered choices');
    env.nodes['logic-review-reuse'].checked=true;const reusing=env.nodes['logic-review-reuse'].fire('change');await new Promise(resolve=>setTimeout(resolve,0));
    const updates=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(updates.length,1,'Explicit reuse synchronizes a different remembered ID into the private draft');assert.equal(updates[0].body.item_id,'anchor');assert.equal(updates[0].body.series_id,'authorized-page-two');assert.equal(updates[0].body.action,'confirm');assert.equal(updates[0].body.revision,2);
    assert.equal(env.nodes['logic-apply'].disabled,true,'Reuse keeps saving disabled until all adopted suggestions finish validation');env.reviewReplies[0]();await reusing;
    assert.equal(env.nodes['logic-apply'].disabled,false);assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')||row.url.endsWith('/review/confirm-ready')).length,0,'Adopting preferences never saves a business judgment or confirms unrelated items');
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.find(row=>row.url.endsWith('/apply')).body.review_revision,3,'Final save uses the revision containing the remembered binding');
  }
  {
    const env=await generateReview({readyMode:true,reviewFailure:true});env.nodes['logic-review-reuse'].checked=true;await env.nodes['logic-review-reuse'].fire('change');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/review/update')).length,1);assert.equal(reviewControl(env,'anchor','search').value,'Another authorized field','An invalid remembered choice remains visible for correction');
    const field=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewField==='binding');assert.equal(field.dataset.invalid,'true');assert.match(field.textContent,/当前修改已保留/);assert.equal(env.nodes['logic-apply'].disabled,true);
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0,'An unsynchronized remembered choice cannot save the old AI binding');
  }
  console.log('32 isolated judgment manager interaction checks passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/judgment-manager.js'), 'utf8');
class Element {
  constructor(id='') { this.id=id;this.dataset={};this.attributes={};this.children=[];this.listeners={};this.value='';this.textContent='';this.hidden=false;this.disabled=false;this.open=false; }
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
function setup({viewer=false,failure=false,stale=false,empty=false,panelMode=false,savedId=false,reviewMode=false,reviewFailure=false,catalogDeferred=false,emptyExcerpt=false,ruleEntries=null}={}) {
  const ids=['judgment-manager',...['original-source','reuse-original','input','status','preview','generate','apply','file','skeleton','structured','variety','reuse','model','current-md','current-state','version-label','version','version-range','file-name','download','version-number','load-version','history-md','history-diff','preview-md','generated-diff','coverage','bindings','unresolved','field-diff'].map(x=>'logic-'+x)];
  const nodes=Object.fromEntries(ids.map(id=>[id,new Element(id)]));
  for(const name of ['import','review','review-nav','review-items','review-count','review-reuse','review-note','review-edit','review-save-note','review-ready','review-source'])nodes['logic-'+name]=new Element('logic-'+name);
  if(panelMode) for(const name of ['panel-switch','panel-open','panel-name','panel-save','panel-range','panel-more','new-panel-title'])nodes['logic-'+name]=new Element();
  nodes['judgment-manager'].dataset.prefill='false';
  if(panelMode)nodes['judgment-manager'].dataset.judgmentId='42';
  const tabs=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicTab=name;return el;});
  const panels=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicPanel=name;return el;});
  const close=new Element();
  nodes['judgment-manager'].querySelectorAll=selector=>selector==='[data-logic-tab]'?tabs:panels;
  nodes['judgment-manager'].querySelector=selector=>selector==='[data-logic-close]'?close:tabs[1];
  const requests=[],navigations=[],catalogReplies=[];let reloads=0,generations=0,revision=1;
  const catalog=[{series_id:'authorized-other',name:'Directory quantity <script>',unit:'t',source_file:'Shared workbook.xlsx',sheet:'Daily'},{series_id:'authorized-page-two',name:'Another authorized field',unit:'t',source_file:'Other workbook.xlsx',sheet:'Weekly'}];
  const review={draft_id:'server-bound-nonce',revision:1,sections:[{id:'contradiction',title:'主矛盾',items:[{id:'anchor',kind:'binding',title:'数据锚',original_text:'Supply quantity <script>',description:'请核对数据来源与口径。',status:'pending',requires_binding:true,can_bind:true,can_confirm:true,warnings:[],binding:null,ai_binding:{series_id:'ai-choice',name:'AI quantity',unit:'kg',source_file:'Initial workbook.xlsx',sheet:'Table'}}]},{id:'rules',title:'阈值与证伪',items:[{id:'rule',kind:'rule',title:'阈值条件',original_text:'A condition without a unit',status:'pending',requires_binding:false,can_bind:false,can_confirm:false,warnings:['原文缺少阈值单位，请补充原文或保留文字。']},{id:'note',kind:'text',title:'研究说明',original_text:'Keep this observation',status:'pending',requires_binding:false,can_bind:false,can_confirm:true,warnings:[]}]}],pending_count:3,confirmed_count:0,total:3};
  if(emptyExcerpt)review.sections[0].items[0].original_text='';
  if(ruleEntries){review.sections[1].items=ruleEntries.map(row=>({title:'解析条件',original_text:'Exact research evidence',description:'请核对解析后的条件。',status:'pending',requires_binding:true,can_bind:true,can_confirm:false,warnings:[],binding:{series_id:'condition-unit',name:'研究数量',unit:'吨'},...row}));review.total=review.pending_count=1+ruleEntries.length;}
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
        if(reviewFailure)return {ok:false,status:422,json:async()=>({detail:'请选择有可核对单位的指标，当前修改已保留。'})};
        const body=JSON.parse(options.body),item=review.sections.flatMap(section=>section.items).find(row=>row.id===body.item_id);
        item.status={confirm:'confirmed',keep_text:'kept_text',exclude:'excluded'}[body.action];
        if(body.series_id)item.binding=catalog.find(row=>row.series_id===body.series_id)||item.ai_binding;
        if(body.source_quote)item.source_quote=body.source_quote;
        item.remembered=body.remember;review.pending_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='pending').length;review.confirmed_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='confirmed').length;review.revision=++revision;review.draft_id='server-bound-nonce-r'+revision;result=preview();
      }
      else if(url.endsWith('/review/reuse')){review.reuse_enabled=JSON.parse(options.body).enabled;review.sections[0].items[0].binding=catalog[1];review.sections[0].items[0].remembered=true;review.revision=++revision;result=preview();}
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
  return {nodes,requests,context,navigations,catalogReplies,reloads:()=>reloads};
}
function descendants(node){return node.children.flatMap(child=>[child,...descendants(child)]);}
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
    assert.equal(env.nodes['logic-review'].hidden,false,'Parsing opens per-item review');
    assert.equal(env.nodes['logic-import'].hidden,true,'Parsing folds the original input');
    assert.match(env.nodes['logic-review-count'].textContent,/3.*待确认/);
    assert.equal(env.nodes['logic-apply'].disabled,false,'A draft may retain pending items without executing them');
    assert.match(env.nodes['logic-review-save-note'].textContent,/未通过校验的规则不会参与执行/);
    assert.equal(reviewControl(env,'rule','confirm').disabled,true,'An incomplete unit cannot be confirmed');
    assert.match(descendants(env.nodes['logic-review-items']).map(node=>node.textContent).join(' '),/原文缺少阈值单位/);
    assert.equal(reviewControl(env,'anchor','remember').checked,false,'Remembering a binding is explicit');
  }
  {
    const env=await generateReview(),search=reviewControl(env,'anchor','search');
    await search.fire('focus');
    assert.ok(env.requests.some(row=>row.url.includes('/review/indicators?')),'Binding dropdown searches the authorized full directory');
    search.value='Directory';await search.fire('input');await new Promise(resolve=>setTimeout(resolve,260));
    assert.ok(env.requests.some(row=>row.url.includes('search=Directory')));
    await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    assert.equal(search.value,'Directory quantity <script>','Keyboard selection uses safe text');
    const remember=reviewControl(env,'anchor','remember');remember.checked=true;await remember.fire('change');
    await reviewControl(env,'anchor','confirm').fire('click');
    const update=env.requests.find(row=>row.url.endsWith('/review/update')).body;
    assert.equal(update.series_id,'authorized-other');assert.equal(update.remember,true);assert.equal(update.revision,1);
    await reviewControl(env,'rule','keep_text').fire('click');await reviewControl(env,'note','exclude').fire('click');
    assert.equal(env.nodes['logic-apply'].disabled,false);
    await env.nodes['logic-apply'].fire('click');
    assert.equal(env.requests.find(row=>row.url.endsWith('/apply')).body.draft_id,'server-bound-nonce-r4','Saving uses the latest reviewed draft');
    assert.equal(env.requests.find(row=>row.url.endsWith('/apply')).body.review_revision,4,'Saving includes the revision the user reviewed');
  }
  {
    const env=await generateReview({reviewFailure:true}),search=reviewControl(env,'anchor','search');
    await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    await reviewControl(env,'anchor','confirm').fire('click');
    assert.equal(search.value,'Directory quantity <script>','Failed review updates retain the user selection');
    assert.match(env.nodes['logic-status'].textContent,/当前修改已保留/);
    assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0,'Unsynchronized edits cannot save');
  }
  {
    const env=await generateReview();env.nodes['logic-review-reuse'].checked=true;await env.nodes['logic-review-reuse'].fire('change');
    const request=env.requests.find(row=>row.url.endsWith('/review/reuse'));assert.equal(request.body.enabled,true);
    assert.match(env.nodes['logic-review-count'].textContent,/3.*待确认/,'Remembered suggestions still require confirmation');
    assert.equal(reviewControl(env,'anchor','search').value,'Another authorized field','Reuse prefills the remembered authorized choice');
    assert.equal(reviewControl(env,'anchor','remember').checked,false,'Reusing a suggestion never checks remember automatically');
    await env.nodes['logic-review-edit'].fire('click');assert.equal(env.nodes['logic-import'].hidden,false);
    env.nodes['logic-input'].value='Changed full source';await env.nodes['logic-input'].fire('input');
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview(),search=reviewControl(env,'anchor','search');await search.fire('focus');
    assert.equal(env.requests.filter(row=>row.url.includes('/review/indicators')).length,1,'The browser fetches one bounded page');
    await reviewControl(env,'anchor','more').fire('click');
    assert.match(env.requests.filter(row=>row.url.includes('/review/indicators')).at(-1).url,/offset=1.*limit=20/);
    assert.equal(reviewControl(env,'anchor','more').hidden,true,'Pagination ends at the server boundary');
    await search.fire('keydown',{key:'ArrowUp'});await search.fire('keydown',{key:'Enter'});
    assert.equal(search.value,'Another authorized field','Keyboard can choose a later directory page');
    await search.fire('keydown',{key:'Escape'});assert.equal(search.attributes['aria-expanded'],'false');
  }
  {
    const env=await generateReview({catalogDeferred:true}),search=reviewControl(env,'anchor','search');
    search.value='slow';await search.fire('input');await new Promise(resolve=>setTimeout(resolve,220));
    search.value='fast';await search.fire('input');await new Promise(resolve=>setTimeout(resolve,220));
    const first=env.requests.find(row=>row.url.includes('search=slow'));assert.equal(first.signal.aborted,true,'A new search aborts the previous request');
    env.catalogReplies[0]();await new Promise(resolve=>setTimeout(resolve,10));
    await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    assert.equal(search.value,'Directory quantity <script>','A late response cannot replace a newer query result');
  }
  {
    const raw='A source with `literal field` and complete evidence.\nNext line stays exact.',env=await generateReview({source:raw});
    assert.equal(env.nodes['logic-review-source'].textContent,raw,'Source selection preserves exact original text');
    env.context.window.getSelection=()=>({anchorNode:env.nodes['logic-review-source'],focusNode:env.nodes['logic-review-source'],toString:()=> '`literal field` and complete evidence.'});
    let selectionPreserved=false;await reviewControl(env,'anchor','quote').fire('mousedown',{preventDefault(){selectionPreserved=true;}});
    if(!selectionPreserved)env.context.window.getSelection=()=>({toString:()=> ''});
    await reviewControl(env,'anchor','quote').fire('click');assert.equal(env.nodes['logic-apply'].disabled,true);
    await reviewControl(env,'anchor','confirm').fire('click');
    assert.equal(env.requests.find(row=>row.url.endsWith('/review/update')).body.source_quote,'`literal field` and complete evidence.');
    env.context.window.getSelection=()=>({anchorNode:env.nodes['logic-input'],focusNode:env.nodes['logic-input'],toString:()=> 'outside'});
    await reviewControl(env,'rule','quote').fire('click');assert.match(env.nodes['logic-status'].textContent,/对照完整原文/);
  }
  {
    const env=await generateReview();await env.nodes['logic-review-ready'].fire('click');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/review/confirm-ready')).length,1);
    assert.match(env.nodes['logic-review-count'].textContent,/2 已确认.*1 待确认/);
    assert.equal(reviewControl(env,'rule','confirm').disabled,true,'Batch confirmation leaves incomplete conditions pending');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0,'Batch confirmation does not save');
  }
  {
    const env=await generateReview(),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    await reviewControl(env,'note','keep_text').fire('click');
    assert.equal(reviewControl(env,'anchor','search').value,'Directory quantity <script>','Updating another item preserves local binding edits');
    assert.equal(env.nodes['logic-apply'].disabled,true,'Unsubmitted changes remain visible and prevent saving after another item updates');
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
    await reviewControl(env,'anchor','confirm').fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/review/update')).at(-1).body.revision,2,'A preserved edit submits against the newest revision');
    assert.equal(env.nodes['logic-apply'].disabled,false);
  }
  {
    const env=await generateReview();env.context.window.JudgmentManager.close();await env.context.window.JudgmentManager.open();
    const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    assert.equal(search.value,'Directory quantity <script>','Reopening creates fresh catalog search sessions');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/generate')).length,1,'Reopening a parsed draft never reruns the model');
  }
  {
    const raw='Exact raw source with `literal evidence`.',env=await generateReview({emptyExcerpt:true,source:raw});
    const card=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewItem==='anchor'&&!node.dataset.reviewControl),details=descendants(card).find(node=>node.className==='logic-item-full-source'),source=details.children[1];
    assert.equal(source.textContent,'','Closed cards do not duplicate the full source in the DOM');
    details.open=true;await details.fire('toggle');assert.equal(source.textContent,raw,'An unmatched item provides the exact full source locally when expanded');
    env.context.window.getSelection=()=>({anchorNode:source,focusNode:source,toString:()=> '`literal evidence`'});
    await reviewControl(env,'anchor','quote').fire('click');await reviewControl(env,'anchor','confirm').fire('click');
    assert.equal(env.requests.find(row=>row.url.endsWith('/review/update')).body.source_quote,'`literal evidence`','Local card source selections can be adopted without cleaning');
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
    const literal='900719925474099312345.123456789',env=await generateReview({ruleEntries:[{id:'unit-edit',kind:'thresholds',rule:{operator:'>=',value_literal:literal,type:'观察位',horizon:'短期'}}]});
    const condition=reviewControl(env,'unit-edit','condition'),search=reviewControl(env,'unit-edit','search');
    assert.match(condition.textContent,/吨/);
    await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    assert.equal(condition.textContent,'解析后的条件：指标值不低于 '+literal+' t；观察位；短期','Changing the binding immediately refreshes the condition unit without rounding its literal');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/review/update')).length,0,'The selected unit is visible before item confirmation');
    search.value='';await search.fire('input');
    assert.equal(condition.textContent,'解析后的条件：指标值不低于 '+literal+'（单位待确认）；观察位；短期','Clearing the selection immediately removes the previous unit');
    env.context.window.JudgmentManager.close();
  }
  console.log('28 isolated judgment manager interaction checks passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

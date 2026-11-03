const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const {webcrypto} = require('node:crypto');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/judgment-manager.js'), 'utf8');
class Element {
  constructor(id='') { this.id=id;this.dataset={};this.attributes={};this.children=[];this.listeners={};this.style={};this.value='';this.textContent='';this.hidden=false;this.disabled=false;this.open=false; }
  get textContent(){return (this._text||'')+this.children.map(child=>child.textContent).join('');}
  set textContent(value){this._text=String(value);this.children=[];}
  set innerHTML(_) { throw new Error('Unsafe HTML rendering attempted'); }
  addEventListener(type,fn) { this.listeners[type]=fn; }
  async fire(type,event={}) { return this.listeners[type]?.({target:this,preventDefault(){},...event}); }
  replaceChildren(...children){for(const child of this.children)child.parentNode=null;this.children=[];this.append(...children);}
  append(...children){for(const child of children){if(child.parentNode)child.parentNode.children=child.parentNode.children.filter(existing=>existing!==child);child.parentNode=this;this.children.push(child);}}
  setAttribute(name,value){this.attributes[name]=value;}
  removeAttribute(name){delete this.attributes[name];}
  contains(node){return node===this||this.children.some(child=>child.contains(node));}
  remove(){if(this.parentNode)this.parentNode.children=this.parentNode.children.filter(child=>child!==this);this.parentNode=null;}
  focus(){}
  showModal(){this.open=true;}
  close(){this.open=false;}
  click(){}
}
function setup({viewer=false,failure=false,stale=false,empty=false,panelMode=false,savedId=false,reviewMode=false,reviewFailure=false,catalogDeferred=false,emptyExcerpt=false,ruleEntries=null,readyMode=false,reviewConflict=false,reviewDeferred=false,jobState='succeeded',jobStartDeferred=false,jobStatusDeferred=false,jobResultDeferred=false,jobNetworkFailure=false,baselineDeferred=false,visualMode=false,caliberMode='',lifecycleMode=false,deleteFailure=false,confirmDelete=true,bindingWarning=false,numberReview=false,analysisReused=false,analysisComplete=undefined,previewNotice='',boundIndicators=[],latestJob=null,latestDeferred=false,catalogFailure=false,reviewSections=null,catalogEntries=null,reviewFrequencyRequired=null}={}) {
  const ids=['judgment-manager',...['restore-result','original-source','reuse-original','input','status','preview','generate','apply','file','skeleton','structured','variety','reuse','model','current-md','current-state','version-label','version','version-range','file-name','download','version-number','load-version','history-md','history-diff','preview-md','generated-diff','coverage','bindings','unresolved','field-diff'].map(x=>'logic-'+x)];
  const nodes=Object.fromEntries(ids.map(id=>[id,new Element(id)]));
  for(const name of ['current-delete','history-download','history-reuse','history-delete','history-state','history-more'])nodes['logic-'+name]=new Element('logic-'+name);
  for(const name of ['import','review','review-nav','review-items','review-count','review-reuse','review-note','review-edit','review-save-note','review-ready','review-source','review-attention','review-all','review-empty'])nodes['logic-'+name]=new Element('logic-'+name);
  if(panelMode||lifecycleMode) for(const name of ['panel-switch','panel-open','panel-name','panel-save','panel-range','panel-more','new-panel-title'])nodes['logic-'+name]=new Element();
  nodes['judgment-manager'].dataset.prefill='false';
  nodes['logic-restore-result'].hidden=true;
  if(panelMode)nodes['judgment-manager'].dataset.judgmentId='42';
  const tabs=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicTab=name;return el;});
  const panels=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicPanel=name;return el;});
  const close=new Element();
  nodes['judgment-manager'].querySelectorAll=selector=>selector==='[data-logic-tab]'?tabs:panels;
  nodes['judgment-manager'].querySelector=selector=>selector==='[data-logic-close]'?close:tabs[1];
  const requests=[],navigations=[],catalogReplies=[],reviewReplies=[],jobStartReplies=[],jobStatusReplies=[],jobResultReplies=[],baselineReplies=[],latestReplies=[];
  const firstJob='synthetic-analysis-job-0001',jobStates=new Map([[firstJob,failure?'failed':jobState]]),jobSources=new Map(),jobInputs=new Map(),jobKeys=new Map();
  let reloads=0,generations=0,revision=1,submittedBody=null;
  const catalog=catalogEntries||[{series_id:'authorized-other',name:'Directory quantity <script>',unit:'t',source_file:'Shared workbook.xlsx',sheet:'Daily'},{series_id:'authorized-page-two',name:'Another authorized field',unit:'t',source_file:'Other workbook.xlsx',sheet:'Weekly'}];
  const review={draft_id:'server-bound-nonce',revision:1,sections:[{id:'contradiction',title:'主矛盾',items:[{id:'anchor',kind:'binding',title:'数据锚',original_text:'Supply quantity <script>',description:'请核对数据来源与口径。',status:'pending',requires_binding:true,can_bind:true,can_confirm:true,warnings:[],binding:null,ai_binding:{series_id:'ai-choice',name:'AI quantity',unit:'kg',source_file:'Initial workbook.xlsx',sheet:'Table'}}]},{id:'rules',title:'阈值与证伪',items:[{id:'rule',kind:'thresholds',rule:{operator:'>',value_literal:'12',type:'观察位',horizon:'短期'},title:'阈值条件',original_text:'A condition without a unit',status:'pending',requires_binding:false,can_bind:false,can_confirm:false,attention_fields:['condition'],attention_reasons:['原文缺少阈值单位，请补充原文或保留文字。'],warnings:['原文缺少阈值单位，请补充原文或保留文字。']},{id:'note',kind:'text',title:'研究说明',original_text:'Keep this observation',status:'pending',requires_binding:false,can_bind:false,can_confirm:true,warnings:[]}]}],pending_count:3,confirmed_count:0,total:3};
  if(reviewSections){review.sections=JSON.parse(JSON.stringify(reviewSections));review.total=review.sections.flatMap(section=>section.items).length;review.pending_count=review.sections.flatMap(section=>section.items).filter(item=>item.status==='pending').length;}
  if(emptyExcerpt)review.sections[0].items[0].original_text='';
  if(ruleEntries){review.sections[1].items=ruleEntries.map(row=>({title:'解析条件',original_text:'Exact research evidence',description:'请核对解析后的条件。',status:'pending',requires_binding:true,can_bind:true,can_confirm:false,warnings:[],binding:{series_id:'condition-unit',name:'研究数量',unit:'吨'},...row}));review.total=review.pending_count=1+ruleEntries.length;}
  if(readyMode){review.sections[0].items[0].status='ready';review.sections[1].items[1].status='ready';review.sections.push({id:'time_dimension',title:'时间维度',items:[{id:'time-text',kind:'description',title:'适用周期',description:'短期关注供需变化，中期观察成本约束。',status:'ready',original_text:'',can_confirm:true,can_bind:false,requires_binding:false,warnings:[]}]});review.total=4;review.pending_count=review.needs_attention_count=1;review.ready_count=3;}
  if(readyMode==='all'){review.sections[1].items[0].status='ready';review.sections[1].items[0].warnings=[];review.pending_count=review.needs_attention_count=0;review.ready_count=4;}
  if(visualMode){
    const anchor=review.sections[0].items[0];Object.assign(anchor,{kind:'thresholds',status:'ready',binding:anchor.ai_binding,rule:{operator:'>',value_literal:'12',type:'观察位',horizon:'短期'}});
    Object.assign(review.sections[1].items[0],{status:'ready',can_bind:true,binding:{series_id:'second-visual',name:'Second quantity',unit:'吨'}});
  }
  if(caliberMode){const item=review.sections[0].items[0];item.binding={...item.ai_binding,unit:caliberMode==='declared'?null:item.ai_binding.unit};item.caliber={catalog_unit:caliberMode==='catalog'?'kg':null,model_unit:'单位不一致',mode:'',unit:'',basis:'',scope_note:'',can_adopt_catalog:caliberMode==='catalog',can_declare:caliberMode==='declared',confirmation_required:true,confirmed:false};}
  function preview(){return {analysis_complete:analysisComplete,analysis_reused:analysisReused,notice:previewNotice,draft_id:review.draft_id,base_version:2,markdown:'# Proposed',diff:{markdown:'-old\n+new',fields:[{label:'Rule',before:null,after:'<img onerror=bad>'}]},bindings:boundIndicators,unresolved:[],candidate_count:1,catalog_total:5,review_available:true,review:JSON.parse(JSON.stringify(review))};}
  const deleted=new Set(),downloads=[],confirmations=[];
  const window={RESEARCH_CONTEXT:{workbench:'synthetic'},RESEARCH_PREFIX:'/research/workbenches/synthetic',crypto:webcrypto,confirm(message){confirmations.push(message);return confirmDelete;}};
  class DownloadURL extends URL {static createObjectURL(blob){downloads.push(blob);return 'blob:synthetic';}static revokeObjectURL(){}}
  const context={window,parent:window,location:{origin:'http://unit.test',href:'http://unit.test/research/workbenches/synthetic/sn/judgment?date=2024-03-01&judgment_id=42',reload(){reloads++;},assign(url){navigations.push(url);}},
    document:{getElementById:id=>nodes[id],createElement:()=>new Element(),createElementNS:()=>new Element(),querySelectorAll:()=>[]},
    TextEncoder,TextDecoder,AbortController,Blob,URL:DownloadURL,URLSearchParams,Option:Element,console,setTimeout,clearTimeout,
    async researchFetch(url,options={}) {
      requests.push({url,body:options.body?JSON.parse(options.body):undefined,method:options.method,signal:options.signal,headers:options.headers,keepalive:options.keepalive});
      let result;
      if(baselineDeferred&&url.includes('/manage')&&requests.filter(row=>row.url.includes('/manage')).length>1)await new Promise(resolve=>baselineReplies.push(resolve));
      const jobMatch=/\/jobs\/([A-Za-z0-9_-]+)(\/result)?$/.exec(url);
      if(url.endsWith('/jobs')&&options.method==='POST'){
        generations++;submittedBody=JSON.parse(options.body);
        if(stale&&generations===1)return {ok:false,status:409,json:async()=>({detail:'Synthetic version changed'})};
        const key=options.headers['Idempotency-Key'];let identifier=jobKeys.get(key);
        if(!identifier){identifier='synthetic-analysis-job-'+String(jobKeys.size+1).padStart(4,'0');jobKeys.set(key,identifier);jobInputs.set(identifier,JSON.parse(options.body));jobStates.set(identifier,failure?'failed':jobState);}
        if(jobNetworkFailure&&generations===1)throw new TypeError('Synthetic network outcome unknown');
        if(jobStartDeferred)await new Promise(resolve=>jobStartReplies.push(resolve));
        result={id:identifier,job_id:identifier,workbench_id:'synthetic',state:'queued',phase:'queued',title:'研究逻辑分析',result_available:false};
      }
      else if(url.endsWith('/jobs/latest')){if(latestDeferred)await new Promise(resolve=>latestReplies.push(resolve));result={job:latestJob};}
      else if(jobMatch){
        const identifier=jobMatch[1],state=jobStates.get(identifier)||'succeeded';
        if(jobMatch[2]){
          if(jobResultDeferred&&identifier===firstJob)await new Promise(resolve=>jobResultReplies.push(resolve));
          const value=reviewMode?preview():{draft_id:'server-bound-nonce',base_version:2,markdown:'# Proposed',diff:{markdown:'-old\n+new',fields:[{label:'Rule',before:null,after:'<img onerror=bad>'}]},bindings:[{name:'Actual quantity',unit:'kg'}],unresolved:[],candidate_count:1,catalog_total:5,candidate_subset:true,notice:'Preview only'};
          const input=jobInputs.get(identifier)||submittedBody;
          result={...value,source_text:jobSources.get(identifier)||input?.text||'Recovered exact source',variety:input?.variety||'SN',model_id:input?.model_id||'configured',source_name:input?.source_name||'Recovered source.md',panel_title:input?.panel_title||'',job:{id:identifier,state}};
        }else{
          if(jobStatusDeferred&&identifier===firstJob)await new Promise(resolve=>jobStatusReplies.push(resolve));
          result={id:identifier,job_id:identifier,workbench_id:'synthetic',state,phase:state==='running'?'analyzing':state,title:'研究逻辑分析',result_available:state==='succeeded',...(failure?{message:'Synthetic unavailable; input retained'}:{})};
        }
      }
      else if(url.includes('/review/indicator-values?')){
        const selected=new URL(url,'http://unit.test').searchParams.get('item_ids')||'';
        result={draft_id:review.draft_id,revision:review.revision,series:visualMode?[{series_id:selected==='rule'?'second-visual':'ai-choice',unit:selected==='rule'?'吨':'kg',latest_value:visualMode==='empty'?null:selected==='rule'?7:0,as_of:'2024-03-01',points:visualMode==='empty'?[]:[{date:'2024-02-29',value:2},{date:'2024-03-01',value:0}]}]:[]};
      }
      else if(url.includes('/review/radar?')){
        result={draft_id:review.draft_id,revision:review.revision,rows:visualMode&&visualMode!=='empty'?[{item_id:'anchor',value:0,unit:'kg',as_of:'2024-03-01',progress:visualMode==='negative'?-30:240,distance:'Synthetic distance',state:'Synthetic condition'}]:[]};
      }
      else if(url.includes('/review/indicators')){
        if(catalogFailure)return {ok:false,status:503,json:async()=>({detail:'隔离目录服务暂不可用'})};
        if(catalogDeferred&&url.includes('search=slow'))await new Promise(resolve=>catalogReplies.push(resolve));
        result={items:url.includes('search=slow')?[{series_id:'stale-result',name:'Stale catalog reply',unit:'kg'}]:url.includes('offset=1')?[catalog[1]]:[catalog[0]],total:2,next_offset:url.includes('offset=1')?null:1};
      }
      else if(url.endsWith('/review/update')){
        if(reviewDeferred)await new Promise(resolve=>reviewReplies.push(resolve));
        if(reviewConflict)return {ok:false,status:409,json:async()=>({detail:'来源已改变，请重新分析。'})};
        if(reviewFailure)return {ok:false,status:422,json:async()=>({detail:{message:'请选择有可核对单位的指标，当前修改已保留。',attention_fields:['binding'],...(typeof reviewFrequencyRequired==='boolean'?{frequency_required:reviewFrequencyRequired}:{})}})};
        const body=JSON.parse(options.body),item=review.sections.flatMap(section=>section.items).find(row=>row.id===body.item_id);
        if(bindingWarning&&body.action==='confirm'&&body.series_id&&body.series_id!==item.binding?.series_id&&body.binding_warning_token!=='synthetic-warning-token')return{ok:false,status:422,json:async()=>({detail:{code:'binding_warning',message:'原绑定单位为kg，所选指标单位为t，两者不一致，不自动换算。是否保留此绑定？',confirmation_token:'synthetic-warning-token',attention_fields:['binding']}})};
        if(numberReview&&body.number_review_token!=='synthetic-number-token')return{ok:false,status:422,json:async()=>({detail:{code:'number_review',message:'原文：“Synthetic quantity exceeds 17.5 in the observation.”\n规则数值：17.5（吨）\n程序无法自动判断数字后的文字是否为单位，请核对后确认。',confirmation_token:'synthetic-number-token',attention_fields:['condition']}})};
        if(body.caliber){item.caliber={...item.caliber,...body.caliber,unit:body.caliber.mode==='catalog'?item.caliber.catalog_unit:body.caliber.unit,confirmed:true,confirmation_required:false};item.binding={...item.binding,unit:item.caliber.unit};item.warnings=[];item.attention_fields=[];}
        item.status={confirm:'confirmed',keep_text:'kept_text',exclude:'excluded'}[body.action];
        if(Object.hasOwn(body,'series_id'))item.binding=body.series_id===null?null:catalog.find(row=>row.series_id===body.series_id)||item.ai_binding;
        if(Object.hasOwn(body,'related_series_ids'))item.related_bindings=body.related_series_ids.map(identifier=>catalog.find(row=>row.series_id===identifier)||item.related_bindings?.find(row=>row.series_id===identifier)||{series_id:identifier,name:'Synthetic related indicator',unit:'kg'});
        if(item.can_add_binding)item.bindings=[item.binding,...(item.related_bindings||[])].filter(Boolean);
        if(body.source_quote)item.source_quote=body.source_quote;
        item.remembered=body.remember;review.pending_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='pending').length;review.confirmed_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='confirmed').length;if(Object.hasOwn(review,'needs_attention_count'))review.needs_attention_count=review.pending_count;review.revision=++revision;review.draft_id='server-bound-nonce-r'+revision;result=preview();
      }
      else if(url.endsWith('/review/reuse')){const enabled=JSON.parse(options.body).enabled,item=review.sections[0].items[0];review.reuse_enabled=enabled;item.binding=enabled?catalog[1]:null;item.remembered=enabled;item.status=enabled?'pending':readyMode?'ready':'pending';item.origin=enabled?'remembered':'model';review.pending_count=review.sections.flatMap(section=>section.items).filter(row=>row.status==='pending').length;if(Object.hasOwn(review,'needs_attention_count'))review.needs_attention_count=review.pending_count;review.revision=++revision;result=preview();}
      else if(url.endsWith('/review/confirm-ready')){for(const item of review.sections.flatMap(section=>section.items))if(item.can_confirm)item.status='confirmed';review.pending_count=1;review.confirmed_count=2;review.revision=++revision;result=preview();}
      else if(url.includes('/review?'))result=JSON.parse(JSON.stringify(review));
      else if(lifecycleMode&&options.method==='DELETE'){
        if(deleteFailure)return{ok:false,status:409,json:async()=>({detail:'已有报告引用，请先处理引用。'})};
        const identifier=Number(url.split('/').pop());deleted.add(identifier);result={deleted:true,id:identifier,version:identifier===42?1:2,revision:2};
      }
      else if(lifecycleMode&&url.includes('/versions/')){const historical=url.includes('/versions/2');result={id:historical?43:42,version:historical?2:1,status:'草稿',variety:'SN',delete_revision:historical?0:1,markdown:historical?'# Historical complete MD':'# Current',source_text:'Complete original',diff:{markdown:'-Current\n+Historical'}};}
      else if(url.includes('/panels')&&options.method==='PATCH')result={id:'named-panel',judgment_id:42,version:1,title:JSON.parse(options.body).title,revision:2,named:true};
      else if(url.endsWith('/panels')){const rows=[{id:'named-panel',judgment_id:42,version:1,variety:'SN',status:'生效',title:'<img onerror=unsafe>',revision:1,named:true},{id:null,judgment_id:43,version:2,variety:'SN',status:'草稿',title:'Other independent research',revision:null,named:false}].filter(row=>!deleted.has(row.judgment_id));result={items:rows,total:rows.length,offset:0,has_more:false};}
      else if(url.endsWith('/manage')&&empty) result={variety:'SN',current:null,latest:null,versions:[],version_total:0,can_write:!viewer};
      else if(url.endsWith('/manage')||url.includes('/manage?judgment_id=')) result={variety:'SN',current:{id:42,version:1,status:'生效',author:'r',delete_revision:1,markdown:'# Current',source_text:'# Original input'},latest:{version:stale&&generations?3:2},versions:[{version:2,status:'草稿'},{version:1,status:'生效'}],version_total:2,can_write:!viewer};
      else if(url.includes('/manage?variety='))result={variety:'XY',current:null,latest:null,versions:[],version_total:0,can_write:!viewer};
      else if(url.endsWith('/models')) result={models:[{id:'configured',name:'Configured chat'}]};
      else if(url.endsWith('/structured/preview'))result={kind:'structured',draft_id:'typed-bound-nonce',base_version:0,markdown:'# Typed research',diff:{markdown:'',fields:[]},bindings:[],unresolved:[],candidate_count:0,catalog_total:0,notice:'Typed preview'};
      else if(url.endsWith('/apply'))result={...(savedId?{id:44}:{}),version:3,status:'草稿',notice:'Saved draft'};
      else result={markdown:'# Historical',diff:{markdown:'-Current\n+Historical'}};
      return {ok:true,json:async()=>result};
    }};
  vm.runInNewContext(source,context);
  return {nodes,requests,context,navigations,downloads,confirmations,catalogReplies,reviewReplies,jobStartReplies,jobStatusReplies,jobResultReplies,baselineReplies,latestReplies,firstJob,jobStates,jobSources,jobsCreated:()=>jobKeys.size,reloads:()=>reloads};
}
const isGeneration=request=>request.url.endsWith('/jobs')&&request.method==='POST';
async function until(condition){for(let attempt=0;attempt<50&&!condition();attempt++)await new Promise(resolve=>setTimeout(resolve,0));assert.ok(condition(),'Expected asynchronous request was reached');}
async function flush(){await new Promise(resolve=>setTimeout(resolve,0));await new Promise(resolve=>setTimeout(resolve,0));}
function descendants(node){return node.children.flatMap(child=>[child,...descendants(child)]);}
function visibleDescendants(node){return node.hidden?[]:node.children.flatMap(child=>child.hidden?[]:[child,...(child.className?.includes('logic-item-adjustments')&&!child.open?[]:visibleDescendants(child))]);}
function reviewControl(env,itemId,control){return descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewItem===itemId&&node.dataset.reviewControl===control);}
async function generateReview(options={}){const env=setup({reviewMode:true,...options});await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value=options.source||'Full research source';env.nodes['logic-model'].value='configured';await env.nodes['logic-generate'].fire('click');return env;}
module.exports = {setup, generateReview, reviewControl, descendants, visibleDescendants, flush};
if (require.main === module) (async()=>{
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-current-md'].textContent,'# Current');
    assert.match(env.nodes['logic-version-label'].textContent,/后续保存仍为草稿/);
    assert.doesNotMatch(env.nodes['logic-version-label'].textContent,/v2/);
    assert.equal(env.requests.filter(isGeneration).length,0,'Opening a manager never invokes a model');
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
    assert.equal(env.requests.filter(r=>r.url.endsWith('/models')||isGeneration(r)).length,0);
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
    assert.equal(env.requests.filter(isGeneration).length,1,'A conflict does not automatically call the model again');
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.requests.filter(isGeneration)[1].body.base_version,3,'Retry uses the freshly verified latest version');
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
    const generated=env.requests.find(isGeneration);
    assert.equal(generated.body.variety,'XY');assert.equal(generated.body.base_version,0);
    assert.equal(generated.body.text,'New explicit product');
  }
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    env.nodes['logic-input'].value=JSON.stringify({variety:'XY',contradiction:{}});
    await env.nodes['logic-structured'].fire('click');
    assert.equal(env.nodes['logic-preview'].hidden,false);
    assert.equal(env.requests.filter(isGeneration).length,0);
    assert.equal(env.requests.find(r=>r.url.endsWith('/structured/preview')).body.payload.variety,'XY');
    await env.nodes['logic-apply'].fire('click');
    assert.equal(env.requests.filter(r=>r.url.endsWith('/structured/apply')).length,1);
  }
  {
    const env=setup({empty:true});await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-variety'].value,'','An empty workbench must not silently inherit the legacy product');
    env.nodes['logic-input'].value='Complete source for a new product';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');
    assert.equal(env.requests.filter(isGeneration).length,0,'Missing product identity must not spend a model call');
    assert.match(env.nodes['logic-status'].textContent,/品种代码/);
  }
  {
    const env=setup({panelMode:true});await env.context.window.JudgmentManager.open();
    assert.ok(env.requests.some(r=>r.url.endsWith('/manage?judgment_id=42')),'Manage reads the displayed immutable judgment');
    assert.match(env.nodes['logic-panel-switch'].children[0].textContent,/<img onerror=unsafe>/,'Panel titles render as safe text');
    env.nodes['logic-panel-switch'].value='43';await env.nodes['logic-panel-open'].fire('click');
    assert.match(env.navigations[0],/date=2024-03-01/);assert.match(env.navigations[0],/judgment_id=43/);
    assert.equal(env.requests.filter(r=>r.url.endsWith('/apply')||isGeneration(r)).length,0,'Switching never activates or imports a judgment');
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
    await env.nodes['logic-generate'].fire('click');assert.equal(env.requests.find(isGeneration).body.panel_title,'Independent second panel');
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
    await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    let updates=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(updates.length,1,'Choosing a field synchronizes the private draft directly');assert.equal(updates[0].body.action,'confirm');assert.equal(updates[0].body.series_id,'authorized-other');assert.equal(updates[0].body.remember,false);
    const remember=reviewControl(env,'anchor','remember');remember.checked=true;await remember.fire('change');updates=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(updates.at(-1).body.remember,true);assert.equal(updates.at(-1).body.revision,2);assert.equal(reviewControl(env,'anchor','remember').checked,true,'A successful explicit remember action remains visibly checked in this draft');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0,'Private synchronization is never a business save');
    await reviewControl(env,'rule','keep_text').fire('click');await reviewControl(env,'note','exclude').fire('click');await env.nodes['logic-apply'].fire('click');
    const applied=env.requests.find(row=>row.url.endsWith('/apply')).body;assert.equal(applied.draft_id,'server-bound-nonce-r5');assert.equal(applied.review_revision,5,'Final save uses the newest reviewed revision');
  }
  {
    const env=await generateReview({reviewFailure:true}),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
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
    assert.equal(env.requests.find(row=>row.url.includes('search=slow')).signal.aborted,true);env.catalogReplies[0]();await new Promise(resolve=>setTimeout(resolve,10));await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});assert.equal(reviewControl(env,'anchor','search').value,'Directory quantity <script>','Late search replies cannot replace the current result');
  }
  {
    const raw='Exact raw source with `literal evidence`.',env=await generateReview({emptyExcerpt:true,source:raw});
    const card=descendants(env.nodes['logic-review-items']).find(node=>node.dataset.reviewItem==='anchor'&&!node.dataset.reviewControl),details=descendants(card).find(node=>node.className==='logic-item-full-source'),source=details.children[1];assert.equal(source.textContent,'');details.open=true;await details.fire('toggle');assert.equal(source.textContent,raw);
    env.context.window.getSelection=()=>({anchorNode:source,focusNode:source,toString:()=> '`literal evidence`'});let preserved=false;await reviewControl(env,'anchor','quote').fire('mousedown',{preventDefault(){preserved=true;}});if(!preserved)env.context.window.getSelection=()=>({toString:()=>''});await reviewControl(env,'anchor','quote').fire('click');
    assert.equal(env.requests.find(row=>row.url.endsWith('/review/update')).body.source_quote,'`literal evidence`','Selecting original text synchronizes the exact quote without cleaning');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
    env.context.window.getSelection=()=>({anchorNode:env.nodes['logic-input'],focusNode:env.nodes['logic-input'],toString:()=> 'outside'});await reviewControl(env,'rule','quote').fire('click');assert.match(env.nodes['logic-status'].textContent,/原文/);
  }
  {
    const env=await generateReview({reviewDeferred:true}),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});const updating=search.fire('keydown',{key:'Enter'});assert.equal(env.nodes['logic-apply'].disabled,true,'A network-pending edit prevents saving');env.reviewReplies[0]();await updating;assert.equal(env.nodes['logic-apply'].disabled,false);
    env.context.window.JudgmentManager.close();await env.context.window.JudgmentManager.open();const reopened=reviewControl(env,'anchor','search');await reopened.fire('focus');assert.ok(env.requests.filter(row=>row.url.includes('/review/indicators')).length>=2);assert.equal(env.requests.filter(isGeneration).length,1,'Reopening never reruns the model');
  }
  {
    const env=await generateReview({reviewConflict:true}),search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});assert.equal(env.nodes['logic-preview'].hidden,true,'A source/version conflict invalidates the stale draft');assert.equal(env.nodes['logic-input'].value,'Full research source');await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview({ruleEntries:[
      {id:'large',kind:'thresholds',rule:{operator:'>=',value_literal:'900719925474099312345.123456789',type:'风控线',horizon:'中长期'}},
      {id:'range',kind:'thresholds',rule:{operator:'between',value_literal:'[-0.000000000000000123,900719925474099312345.123456789]',type:'观察位',horizon:'短期'}},
      {id:'zero',kind:'thresholds',rule:{operator:'<=',value_literal:'0',type:'介入位',horizon:'短期'}},
      {id:'scientific',kind:'thresholds',rule:{operator:'<',value_literal:'-1.234567890123456789e-23',type:'观察位',horizon:'短期'}},
      {id:'average',kind:'thresholds',rule:{operator:'<',value_literal:'{"kind":"sma","window":20}',type:'观察位',horizon:'短期'}},
      {id:'cross-below',kind:'thresholds',rule:{operator:'crosses_below',value_literal:'{"kind":"sma","window":20}',type:'风控线',horizon:'短期'}},
      {id:'cross-above',kind:'thresholds',rule:{operator:'crosses_above',value_literal:'{"kind":"sma","window":60}',type:'观察位',horizon:'中期'}},
      {id:'cross-numeric',kind:'thresholds',rule:{operator:'crosses_below',value_literal:'0',type:'观察位',horizon:'短期'}},
      {id:'cross-invalid-average',kind:'thresholds',rule:{operator:'crosses_above',value_literal:'{"kind":"sma","window":1.2}',type:'观察位',horizon:'短期'}},
      {id:'invalid-average',kind:'thresholds',rule:{operator:'<',value_literal:'{"kind":"sma","window":1.2}',type:'观察位',horizon:'短期'}}]});
    assert.match(reviewControl(env,'large','condition')?.textContent||'',/不低于 900719925474099312345\.123456789 吨/,'Rule values retain exact numeric literals without browser rounding');
    assert.match(reviewControl(env,'large','condition').textContent,/风控线.*中长期/);
    assert.match(reviewControl(env,'range','condition').textContent,/不低于 -0\.000000000000000123 吨.*不高于 900719925474099312345\.123456789 吨.*含上下界/);
    assert.match(reviewControl(env,'zero','condition').textContent,/不高于 0 吨/,'An invalid zero remains visible instead of disappearing');
    assert.match(reviewControl(env,'scientific','condition').textContent,/低于 -1\.234567890123456789e-23 吨/,'Scientific literals retain their original digits and exponent');
    assert.match(reviewControl(env,'average','condition').textContent,/指标值低于 MA20/);
    assert.match(reviewControl(env,'cross-below','condition').textContent,/指标值下穿 20 期均线/);
    assert.match(reviewControl(env,'cross-above','condition').textContent,/指标值上穿 60 期均线/);
    for(const id of ['cross-numeric','cross-invalid-average']){
      assert.match(reviewControl(env,id,'condition').textContent,/需核对条件/);
      assert.doesNotMatch(reviewControl(env,id,'condition').textContent,/crosses_|上穿|下穿|[高低]于 0/,'Crossing conditions require a valid dynamic moving average');
    }
    assert.doesNotMatch(reviewControl(env,'average','condition').textContent,/"kind"|"window"/);
    assert.match(reviewControl(env,'invalid-average','condition').textContent,/需核对条件/);
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
    const condition=reviewControl(env,'unit-edit','condition'),search=reviewControl(env,'unit-edit','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});const updating=search.fire('keydown',{key:'Enter'});
    assert.equal(condition.textContent,'指标值不低于 '+literal+' t；观察位；短期','Unit display updates immediately without rounding while synchronization is pending');env.reviewReplies[0]();await updating;
    const active=reviewControl(env,'unit-edit','search');active.value='';await active.fire('input');assert.equal(reviewControl(env,'unit-edit','condition').textContent,'指标值不低于 '+literal+'（尚未关联指标）；观察位；短期');assert.equal(env.nodes['logic-apply'].disabled,true);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({readyMode:true});assert.equal(env.nodes['logic-review-all'].attributes['aria-pressed'],'true');assert.match(env.nodes['logic-review-count'].textContent,/已解析 4 项.*需处理 1 项/);
    await env.nodes['logic-review-nav'].children.find(node=>node.dataset.reviewSection==='time_dimension').fire('click');const shown=visibleDescendants(env.nodes['logic-review-items']).map(node=>node.textContent).join(' ');assert.match(shown,/短期关注供需变化，中期观察成本约束。/);assert.doesNotMatch(shown,/尚未定位|依据待选择|确认此项|已解析/,'Plain time descriptions have no confirmation/status clutter');assert.equal(reviewControl(env,'time-text','adjust').open,false);
    await env.nodes['logic-review-attention'].fire('click');const issues=visibleDescendants(env.nodes['logic-review-items']).filter(node=>node.dataset.reviewStatus).map(node=>node.dataset.reviewItem);assert.deepEqual(issues,['rule']);
  }
  {
    const env=await generateReview({readyMode:true,reviewFailure:true});const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});assert.equal(env.nodes['logic-apply'].disabled,true,'An unsuccessful change to a ready binding stays unsynchronized');
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
  {
    const env=await generateReview({readyMode:true});const search=reviewControl(env,'anchor','search');await search.fire('focus');
    const emptyChoice=descendants(env.nodes['logic-review-items']).find(node=>node.className==='logic-binding-option'&&node.textContent==='暂无，暂不关联');
    assert.ok(emptyChoice,'The indicator menu offers an explicit unbound choice');await emptyChoice.fire('click');
    const change=env.requests.find(row=>row.url.endsWith('/review/update'));assert.equal(change.body.series_id,null);assert.equal(change.body.action,'keep_text');assert.equal(change.body.remember,false);
    assert.equal(reviewControl(env,'anchor','search').value,'','A deliberate null never falls back to an AI recommendation');assert.equal(env.nodes['logic-apply'].disabled,false);
    env.context.window.JudgmentManager.close();await env.context.window.JudgmentManager.open();assert.equal(reviewControl(env,'anchor','search').value,'','Reopening preserves an explicit unbound choice');
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,1,'An explicitly unbound text draft can be saved');
  }
  {
    const env=setup({jobStartDeferred:true});await env.context.window.JudgmentManager.open();
    env.nodes['logic-input'].value='Source accepted before closing';env.nodes['logic-model'].value='configured';env.nodes['logic-file-name'].textContent='Original source.md';
    const starting=env.nodes['logic-generate'].fire('click');await until(()=>env.jobStartReplies.length===1);
    const posted=env.requests.find(isGeneration);assert.equal(posted.signal,null);assert.equal(posted.keepalive,true);assert.match(posted.headers['Idempotency-Key'],/^[a-f0-9]{32}$/);
    env.context.window.JudgmentManager.close();assert.equal(env.nodes['judgment-manager'].open,false);env.jobStartReplies[0]();await starting;
    assert.equal(env.requests.filter(row=>!row.url.endsWith('/jobs/latest')&&/\/jobs\/[^/]+$/.test(row.url)).length,0,'Closing while a starter is pending does not fetch a result into the closed dialog');
    await env.context.window.JudgmentManager.openJob(env.firstJob);
    assert.equal(env.nodes['logic-input'].value,'Source accepted before closing');assert.equal(env.nodes['logic-file-name'].textContent,'Original source.md');
    assert.equal(env.requests.filter(isGeneration).length,1);assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=await generateReview({jobState:'running'});assert.equal(env.nodes['logic-preview'].hidden,true);assert.match(env.nodes['logic-status'].textContent,/后台|正在分析/);
    env.context.window.JudgmentManager.close();env.jobStates.set(env.firstJob,'succeeded');await env.context.window.JudgmentManager.openJob(env.firstJob);
    assert.equal(env.nodes['logic-preview'].hidden,false);assert.equal(env.requests.filter(isGeneration).length,1,'A completed server task is reopened without spending another model call');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);env.context.window.JudgmentManager.close();
  }
  for(const state of ['failed','interrupted']){
    const env=await generateReview({jobState:state});assert.equal(env.nodes['logic-preview'].hidden,true);assert.equal(env.nodes['logic-status'].dataset.error,'true');assert.equal(env.nodes['logic-generate'].disabled,false);
    assert.match(env.nodes['logic-status'].textContent,state==='failed'?/失败/:/中断/);assert.equal(env.requests.filter(row=>row.url.endsWith('/result')).length,0);
    env.context.window.JudgmentManager.close();await env.context.window.JudgmentManager.openJob(env.firstJob);
    assert.equal(env.requests.filter(isGeneration).length,1);assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);env.context.window.JudgmentManager.close();
  }
  {
    const env=setup({jobResultDeferred:true,reviewMode:true});await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value='Earlier task source';env.nodes['logic-model'].value='configured';
    const generating=env.nodes['logic-generate'].fire('click');await until(()=>env.jobResultReplies.length===1);env.context.window.JudgmentManager.close();
    const newer='synthetic-analysis-job-0002';env.jobSources.set(newer,'Newer task source');await env.context.window.JudgmentManager.openJob(newer);
    assert.equal(env.nodes['logic-input'].value,'Newer task source');env.jobResultReplies[0]();await generating;assert.equal(env.nodes['logic-input'].value,'Newer task source','A late result cannot replace the newly opened task');
    assert.equal(env.requests.filter(isGeneration).length,1);assert.equal(env.requests.filter(row=>row.url.endsWith('/apply')).length,0);
  }
  {
    const env=setup({jobStatusDeferred:true,jobState:'running'});await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value='Earlier task source';env.nodes['logic-model'].value='configured';
    const generating=env.nodes['logic-generate'].fire('click');await until(()=>env.jobStatusReplies.length===1);env.context.window.JudgmentManager.close();
    const newer='synthetic-analysis-job-0002';env.jobStates.set(newer,'succeeded');env.jobSources.set(newer,'Current task source');await env.context.window.JudgmentManager.openJob(newer);
    env.jobStatusReplies[0]();await generating;assert.equal(env.nodes['logic-input'].value,'Current task source');assert.equal(env.nodes['logic-preview'].hidden,false,'An old running status cannot hide or restart the current result');env.context.window.JudgmentManager.close();
  }
  {
    const env=setup({baselineDeferred:true,panelMode:true});await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value='Original captured source';env.nodes['logic-model'].value='configured';env.nodes['logic-new-panel-title'].value='Captured panel';
    const starting=env.nodes['logic-generate'].fire('click');await until(()=>env.baselineReplies.length===1);env.context.window.JudgmentManager.close();env.nodes['logic-input'].value='New unsent source';await env.nodes['logic-input'].fire('input');env.nodes['logic-new-panel-title'].value='Unsent panel';
    env.baselineReplies[0]();await starting;const posted=env.requests.find(isGeneration);assert.equal(posted.body.text,'Original captured source');assert.equal(posted.body.panel_title,'Captured panel');assert.equal(env.nodes['logic-input'].value,'New unsent source');assert.equal(env.nodes['logic-preview'].hidden,true);
  }
  {
    const env=setup({jobNetworkFailure:true});await env.context.window.JudgmentManager.open();env.nodes['logic-input'].value='Retry the identical source';env.nodes['logic-model'].value='configured';
    await env.nodes['logic-generate'].fire('click');assert.equal(env.nodes['logic-generate'].disabled,false);await env.nodes['logic-generate'].fire('click');
    const starts=env.requests.filter(isGeneration);assert.equal(starts.length,2);assert.equal(starts[0].headers['Idempotency-Key'],starts[1].headers['Idempotency-Key']);assert.deepEqual(starts[0].body,starts[1].body);assert.equal(env.jobsCreated(),1,'An uncertain network outcome does not create a second analysis');assert.equal(env.nodes['logic-preview'].hidden,false);
  }
  {
    const env=await generateReview({visualMode:true});await flush();
    assert.equal(descendants(env.nodes['logic-review-items']).some(node=>node.className==='logic-indicator-visual'||node.attributes.role==='img'||node.attributes.role==='meter'),false,'Binding confirmation has no numerical or chart preview, including hidden preview nodes');
    assert.equal(reviewControl(env,'anchor','search').value,'AI quantity');assert.ok(descendants(env.nodes['logic-review-items']).some(node=>node.className==='logic-binding-meta'&&node.textContent==='采用指标口径 · kg'),'The selected indicator unit remains visible');
    await env.nodes['logic-review-nav'].children.find(node=>node.dataset.reviewSection==='rules').fire('click');await flush();
    assert.equal(env.requests.filter(row=>row.url.includes('/indicator-values?')||row.url.includes('/review/radar?')).length,0,'Opening and navigating confirmation sections never request numerical observations');env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({visualMode:true});const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    const remember=reviewControl(env,'anchor','remember');remember.checked=true;await remember.fire('change');
    const updates=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(updates.length,2);assert.equal(updates[0].body.series_id,'authorized-other');assert.equal(updates[1].body.series_id,'authorized-other');assert.equal(updates[1].body.remember,true);assert.equal(updates[1].body.revision,2);
    assert.equal(reviewControl(env,'anchor','search').value,'Directory quantity <script>');assert.equal(reviewControl(env,'anchor','remember').checked,true);
    await env.nodes['logic-apply'].fire('click');assert.equal(env.requests.find(row=>row.url.endsWith('/apply')).body.review_revision,3,'Saving still uses the latest explicit binding and remember decision');
    assert.equal(env.requests.filter(row=>row.url.includes('/indicator-values?')||row.url.includes('/review/radar?')).length,0,'Binding changes, remembering and final save do not fetch chart data');env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({visualMode:'empty'});await flush();const visible=visibleDescendants(env.nodes['logic-review-items']);assert.equal(visible.some(node=>node.className==='logic-visual-empty'||node.className==='logic-visual-value'||node.attributes.role==='meter'),false,'Confirmation does not add loading, missing-value or numerical placeholders');
    assert.equal(env.requests.filter(row=>row.url.includes('/indicator-values?')||row.url.includes('/review/radar?')).length,0);env.context.window.JudgmentManager.close();
  }
  {
    const env=await generateReview({visualMode:true});await flush();const before=env.requests.filter(row=>row.url.includes('/indicator-values?')||row.url.includes('/review/radar?')).length;assert.equal(before,0);
    const search=reviewControl(env,'anchor','search');search.value='Unselected text';await search.fire('input');await flush();assert.equal(visibleDescendants(env.nodes['logic-review-items']).some(node=>node.className==='logic-visual-value'||node.attributes.role==='meter'),false);assert.equal(env.requests.filter(row=>row.url.includes('/indicator-values?')||row.url.includes('/review/radar?')).length,before,'Dirty bindings do not fetch or draw numerical observations');
    await search.fire('focus');const empty=descendants(env.nodes['logic-review-items']).find(node=>node.className==='logic-binding-option'&&node.textContent==='暂无，暂不关联');await empty.fire('click');await flush();assert.equal(reviewControl(env,'anchor','search').value,'');assert.equal(env.requests.filter(row=>row.url.includes('/indicator-values?')||row.url.includes('/review/radar?')).length,before,'A null choice never fetches or restores an AI binding value');env.context.window.JudgmentManager.close();
  }
  console.log('45 isolated judgment manager interaction checks passed (33 existing + 12 background/binding scenarios)');
})().catch(error=>{console.error(error);process.exitCode=1;});

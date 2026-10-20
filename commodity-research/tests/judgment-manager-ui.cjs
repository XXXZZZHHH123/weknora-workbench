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
  focus(){}
  showModal(){this.open=true;}
  close(){this.open=false;}
  click(){}
}
function setup({viewer=false,failure=false,stale=false}={}) {
  const ids=['judgment-manager',...['original-source','reuse-original','input','status','preview','generate','apply','file','skeleton','reuse','model','current-md','current-state','version-label','version','version-range','file-name','download','version-number','load-version','history-md','history-diff','preview-md','generated-diff','coverage','bindings','unresolved','field-diff'].map(x=>'logic-'+x)];
  const nodes=Object.fromEntries(ids.map(id=>[id,new Element(id)]));
  nodes['judgment-manager'].dataset.prefill='false';
  const tabs=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicTab=name;return el;});
  const panels=['current','analyze','versions'].map(name=>{const el=new Element();el.dataset.logicPanel=name;return el;});
  const close=new Element();
  nodes['judgment-manager'].querySelectorAll=selector=>selector==='[data-logic-tab]'?tabs:panels;
  nodes['judgment-manager'].querySelector=selector=>selector==='[data-logic-close]'?close:tabs[1];
  const requests=[];let reloads=0,generations=0;
  const window={RESEARCH_PREFIX:'/research/workbenches/synthetic'};
  const context={window,parent:window,location:{origin:'http://unit.test',reload(){reloads++;}},
    document:{getElementById:id=>nodes[id],createElement:()=>new Element(),querySelectorAll:()=>[]},
    TextEncoder,TextDecoder,AbortController,Blob,URL,Option:Element,console,
    async researchFetch(url,options={}) {
      requests.push({url,body:options.body?JSON.parse(options.body):undefined});
      let result;
      if(url.endsWith('/manage')) result={current:{version:1,status:'生效',author:'r',markdown:'# Current',source_text:'# Original input'},latest:{version:stale&&generations?3:2},versions:[{version:2,status:'草稿'},{version:1,status:'生效'}],version_total:2,can_write:!viewer};
      else if(url.endsWith('/models')) result={models:[{id:'configured',name:'Configured chat'}]};
      else if(url.endsWith('/generate')) {
        generations++;
        if(stale&&generations===1)return {ok:false,status:409,json:async()=>({detail:'Synthetic version changed'})};
        if(failure)return {ok:false,json:async()=>({detail:'Synthetic unavailable; input retained'})};
        result={draft_id:'server-bound-nonce',base_version:2,markdown:'# Proposed',diff:{markdown:'-old\n+new',fields:[{label:'Rule',before:null,after:'<img onerror=bad>'}]},bindings:[{name:'Actual quantity',unit:'kg'}],unresolved:[],candidate_count:1,catalog_total:5,candidate_subset:true,notice:'Preview only'};
      } else if(url.endsWith('/apply'))result={version:3,status:'草稿',notice:'Saved draft'};
      else result={markdown:'# Historical',diff:{markdown:'-Current\n+Historical'}};
      return {ok:true,json:async()=>result};
    }};
  vm.runInNewContext(source,context);
  return {nodes,requests,context,reloads:()=>reloads};
}
(async()=>{
  {
    const env=setup();await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-current-md'].textContent,'# Current');
    assert.match(env.nodes['logic-version-label'].textContent,/最新基准 v2/);
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
  console.log('7 isolated judgment manager interaction checks passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

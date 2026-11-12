const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/affairs.js'), 'utf8');
class Element {
  constructor(){this.dataset={};this.children=[];this.textContent='';this.listeners={};this.disabled=false;}
  set innerHTML(_){throw new Error('Unsafe HTML rendering');}
  addEventListener(kind,callback){this.listeners[kind]=callback;}
  fire(kind){return this.listeners[kind]?.();}
  append(...items){this.children.push(...items);}
  replaceChildren(...items){this.children=items;}
}
function setup({failure=false,zeroRules=false,checkable=true}={}) {
  const nodes=Object.fromEntries(['affairs-check','affairs-check-status','affairs-alert-list'].map(id=>[id,new Element()]));
  nodes['affairs-check'].disabled=!checkable;
  nodes['affairs-alert-list'].children.push('Original persisted alert');
  const requests=[];
  const context={window:{RESEARCH_PREFIX:'/research/workbenches/fixture'},
    document:{getElementById:id=>nodes[id],createElement:()=>new Element()},
    async researchFetch(url,options){requests.push({url,options});return {ok:!failure,json:async()=>failure?{detail:'Source access changed'}:{
      alerts:zeroRules?[]:[{kind:'triggered',title:'<img onerror=unsafe>',detail:'Actual bounded result',href:'/sn/judgment?judgment_id=7',trade_date:'2026-01-01',evidence_dates:['2025-12-30']}],
      checked_at:'2026-01-02 10:00',checked_rules:zeroRules?0:1,partial:false,notice:zeroRules?'尚未保存可检查规则':'Read-only check; no signal written'}};}};
  vm.runInNewContext(source,context);
  return {nodes,requests};
}
const settle=()=>new Promise(resolve=>setImmediate(resolve));
(async()=>{
  const idle=setup({checkable:false});await settle();
  assert.equal(idle.requests.length,0,'Without saved rules or cited conditions the page must not start a check');
  const env=setup();assert.equal(env.requests.length,1,'Opening the page starts exactly one bounded check');
  await settle();
  assert.equal(env.requests[0].options.method,'POST');
  assert.equal(env.requests[0].url,'/research/workbenches/fixture/api/sn/affairs/check');
  const row=env.nodes['affairs-alert-list'].children[0];
  assert.equal(row.href,'/research/workbenches/fixture/sn/judgment?judgment_id=7');
  assert.ok(JSON.stringify(row).includes('<img onerror=unsafe>'),'Untrusted labels remain text');
  assert.ok(JSON.stringify(row).includes('触发'));assert.ok(JSON.stringify(row).includes('依据时点 2025-12-30'));
  assert.match(env.nodes['affairs-check-status'].textContent,/2026-01-02 10:00/);
  assert.ok(!env.nodes['affairs-check-status'].textContent.includes('Read-only check'));
  assert.equal(env.nodes['affairs-check'].disabled,false);
  await env.nodes['affairs-check'].fire('click');
  assert.equal(env.requests.length,2,'The button re-runs the check on demand');
  const rejected=setup({failure:true});await settle();
  assert.deepEqual(rejected.nodes['affairs-alert-list'].children,['Original persisted alert'],'A failed check preserves the last visible records');
  assert.equal(rejected.nodes['affairs-check-status'].dataset.error,'true');
  const empty=setup({zeroRules:true});await settle();
  assert.match(empty.nodes['affairs-alert-list'].children[0].textContent,/规则/,'No saved rule must never be described as no trigger');
  assert.ok(!empty.nodes['affairs-alert-list'].children[0].textContent.includes('触发'));
  const double=setup();const automatic=double.nodes['affairs-check'].fire('click');await settle();
  const first=double.nodes['affairs-check'].fire('click');const second=double.nodes['affairs-check'].fire('click');await Promise.all([automatic,first,second]);
  assert.equal(double.requests.length,2,'Clicks during a running check must not start concurrent fact checks');
  console.log('6 isolated affairs UI interaction checks passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

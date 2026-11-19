const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../src/tin/web/static/affairs.js'), 'utf8');
class Element {
  constructor(){this.dataset={};this.children=[];this.textContent='';this.listeners={};this.disabled=false;}
  set innerHTML(_){throw new Error('Unsafe HTML rendering');}
  addEventListener(kind,callback){this.listeners[kind]=callback;}
  fire(kind,event){return this.listeners[kind]?.(event);}
  append(...items){for(const item of items)item.parent=this;this.children.push(...items);}
  replaceChildren(...items){this.children=[];this.append(...items);}
  remove(){this.parent.children=this.parent.children.filter(item=>item!==this);delete this.parent;}
  setAttribute(key,value){this[key]=value;}
  contains(target){return this===target||this.children.some(item=>item.contains(target));}
  querySelectorAll(){return this.children.filter(row=>row.className==='affairs-row'&&row.dataset.kind!=='monitor-resident');}
  querySelector(selector){const name=selector.slice(1);for(const child of this.children){if(child.className===name)return child;const nested=child.querySelector(selector);if(nested)return nested;}}
  toJSON(){return {...this,parent:undefined};}
}
const edge = index => ({kind:'monitor',title:`Monitor ${index} · 监测触发`,detail:'积压量连续上升',href:`/?monitor=fixture-${index}#monitors`});
const resident = (id, title, current_state, enabled = true) => ({id,title,current_state,enabled,logic:'any',rules:[{label:'指标甲'},{label:'指标乙'}]});
function setup({failure=false,zeroRules=false,checkable=true,monitoring=false,edgeCount=1,wait}={}) {
  const nodes=Object.fromEntries(['affairs-check','affairs-check-status','affairs-alert-list','monitor-list-toggle','affairs-monitor-toasts'].map(id=>[id,new Element()]));
  nodes['affairs-check'].disabled=!checkable;
  nodes['affairs-alert-list'].children.push('Original persisted alert');
  const requests=[];
  const listeners={};
  let clock=0, next=0;
  const timers=new Map();
  function advance(ms){
    const end=clock+ms;
    for(;;){
      const pending=[...timers].sort((left,right)=>left[1].at-right[1].at)[0];
      if(!pending||pending[1].at>end)break;
      const [id,{at,callback}]=pending;clock=at;timers.delete(id);callback();
    }
    clock=end;
  }
  const context={window:{RESEARCH_PREFIX:'/research/workbenches/fixture',addEventListener(kind,callback){listeners[kind]=callback;}},
    document:{getElementById:id=>nodes[id],createElement:()=>new Element(),querySelector:()=>null},
    Date:{now:()=>clock},setTimeout(callback,delay){timers.set(++next,{callback,at:clock+delay});return next;},clearTimeout:id=>timers.delete(id),
    async researchFetch(url,options){requests.push({url,options});if(wait)await wait;return {ok:!failure,json:async()=>failure?{detail:'Source access changed'}:{
      alerts:[...(monitoring&&requests.length===1?Array.from({length:edgeCount},(_,index)=>edge(index)):[]),
        ...(zeroRules?[]:[{kind:'triggered',title:'<img onerror=unsafe>',detail:'Actual bounded result',href:'/sn/judgment?judgment_id=7',trade_date:'2026-01-01',evidence_dates:['2025-12-30']}])],
      monitors:monitoring?[resident('last','Z','未触发'),resident('disabled','Disabled','触发',false),
        {...resident('fixture-0','Monitor 0','触发'),last_trigger:'2026-01-01'},resident('first','A','无法判断')]:[],
      monitor_alerts:monitoring?[edge(0)]:[],
      checked_monitors:monitoring?1:0,
      checked_at:'2026-01-02 10:00',checked_rules:zeroRules?0:1,partial:false,notice:zeroRules?'尚未保存可检查规则':'Read-only check; no signal written'}};}};
  vm.runInNewContext(source,context);
  return {nodes,requests,listeners,advance};
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
  assert.equal(env.nodes['affairs-monitor-toasts'].children.length,0,'Judgment alerts do not display toasts');
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
  const monitor=setup({monitoring:true,edgeCount:5});await settle();
  const list=monitor.nodes['affairs-alert-list'], toasts=monitor.nodes['affairs-monitor-toasts'];
  assert.deepEqual(list.children.map(row=>row.href.split('/').at(-1)),[
    '?monitor=fixture-0#monitors','judgment?judgment_id=7','?monitor=first#monitors','?monitor=last#monitors']);
  assert.equal(list.children[0].dataset.status,'触发');
  assert.equal(list.children[0].querySelector('.affairs-row-detail monitor-description').title,'指标甲 或 指标乙 · 最近触发 2026-01-01');
  assert.equal(toasts.children.length,3);
  assert.equal(toasts.children[2].children[0].textContent,'另有 3 项监测触发');
  const [firstToast,secondToast]=toasts.children;
  assert.equal(firstToast.children[1].children[0].href,list.children[0].href);
  assert.match(JSON.stringify(firstToast),/积压量连续上升/);
  firstToast.children[1].children[1].fire('click');
  assert.equal(toasts.children.length,2);
  monitor.advance(1000);secondToast.fire('mouseenter');secondToast.fire('focusin');
  monitor.advance(10000);
  assert.deepEqual(toasts.children,[secondToast]);
  secondToast.fire('mouseleave');monitor.advance(10000);
  assert.deepEqual(toasts.children,[secondToast],'Focus keeps the clock paused even after the pointer leaves');
  secondToast.fire('focusout',{relatedTarget:null});monitor.advance(8999);
  assert.equal(toasts.children.length,1);
  monitor.advance(1);assert.equal(toasts.children.length,0);
  await monitor.nodes['affairs-check'].fire('click');
  assert.equal(toasts.children.length,0,'Repeated checks with only historical alerts do not display toasts');
  monitor.listeners['monitors-changed']({detail:{count:2,enabled:true,items:[resident('new','New','未检查'),resident('off','Off','触发',false)]}});
  assert.deepEqual(list.children.map(row=>row.dataset.kind),['triggered','monitor-resident']);
  assert.match(list.children[1].href,/monitor=new/);
  let completeCheck;
  const delayed=setup({monitoring:true,zeroRules:true,wait:new Promise(resolve=>{completeCheck=resolve;})});
  delayed.listeners['monitors-changed']({detail:{count:0,enabled:false,items:[],changed:true}});
  completeCheck();await settle();
  assert.equal(delayed.nodes['affairs-alert-list'].children[0].className,'affairs-empty','An older check result cannot restore a deleted reminder');
  assert.equal(delayed.nodes['affairs-alert-list'].children[0].textContent,'暂无可检查规则');
  assert.equal(delayed.nodes['affairs-check'].disabled,true);
  assert.equal(delayed.nodes['monitor-list-toggle'].textContent,'监测 0');
  console.log('10 isolated affairs UI interaction checks passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

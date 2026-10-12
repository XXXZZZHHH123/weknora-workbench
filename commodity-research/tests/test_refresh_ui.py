"""Exercise the actual JS polling transitions, including explicit historical dates."""
import json
from pathlib import Path
import shutil
import subprocess
import pytest

SCRIPT = Path(__file__).parents[1]/'src/tin/web/static/refresh.js'

@pytest.mark.skipif(shutil.which('node') is None, reason='Node is required for the browser polling script')
@pytest.mark.parametrize('search,finished,rendered,latest,observed,expected', [
    ('','success','2026-09-18','2026-09-25',True,1),
    ('','partial','2026-09-18','2026-09-18',True,1),
    ('','failed','2026-09-18','2026-09-18',True,0),
    ('','success','2026-09-18','2026-09-18',False,1),
    ('?date=2026-09-18','success','2026-09-18','2026-09-25',True,0),
])
def test_status_completion_refreshes_default_board_once(search,finished,rendered,latest,observed,expected):
    harness=r'''
const fs=require('fs'),vm=require('vm');
const config=JSON.parse(process.argv[2]);let reloads=0;let timer;let request=0;
const elements=new Map();
function element(id){if(!elements.has(id))elements.set(id,{dataset:{renderedDate:config.rendered},disabled:false,textContent:'',value:'',checked:false,classList:{toggle(){},add(){}},replaceChildren(){},addEventListener(){},querySelectorAll(){return[]}});return elements.get(id)}
const doc={getElementById:element,createElement:()=>({append(){}}),createTextNode:value=>value};
const defaults={enabled:true,scheduler_available:true,times:['08:00','16:30'],sources:[],next_run_at:null,last_success:null};
const initial={...defaults,latest_trade_date:config.observed?config.latest:config.rendered,active:config.observed?{id:'run-1',status:'running',label:'正在更新'}:null,last_attempt:config.observed?null:{id:'run-0',status:'success',label:'更新成功'}};
const complete={...defaults,latest_trade_date:config.latest,active:null,last_attempt:{id:'run-1',status:config.finished,label:config.finished}};
const env={document:doc,window:{RESEARCH_PREFIX:'/research',RESEARCH_CONTEXT:{tenant:1,user:'a'},addEventListener(){}},
  location:{pathname:'/research/',search:config.search,reload(){reloads++}},URLSearchParams,Headers,
  clearTimeout(){},setTimeout(fn){timer=fn;return 1},researchFetch:async()=>({ok:true,json:async()=>request++===0?initial:complete})};
vm.runInNewContext(fs.readFileSync(process.argv[1],'utf8'),env);
(async()=>{await new Promise(resolve=>setImmediate(resolve));if(reloads!==0)throw Error('reloaded before refresh completed');await timer();await timer();process.stdout.write(String(reloads))})().catch(error=>{process.stderr.write(String(error));process.exit(1)});
'''
    result=subprocess.run(['node','-e',harness,str(SCRIPT),json.dumps(dict(search=search,finished=finished,rendered=rendered,latest=latest,observed=observed))],capture_output=True,text=True,check=True)
    assert int(result.stdout)==expected

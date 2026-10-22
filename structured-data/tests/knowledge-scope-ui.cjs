/* Run the shipped iframe scripts with local HTTP fixtures; never call a model. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('./browser-launcher.cjs');
const source = process.env.STRUCTURED_UI_SOURCE || path.join(__dirname, '..', 'src', 'structured_data');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const importId = 'a'.repeat(24);
const sheet = {name:'Shared workbook',rows:3,columns:2,status:'needs_review',layout:'single_header',fingerprint:'same-workbook-layout',reasons:[],
  mapping:{sheet:'Shared workbook',fingerprint:'same-workbook-layout',blocks:[{date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:3,columns:[{column:'B',metric:'Fixture metric',unit:null,header_cells:['B1']}]}]}};
const requests = [], errors = [], checks = [];

function browserPath() {
  if(process.env.SHEET_REVIEW_BROWSER) return process.env.SHEET_REVIEW_BROWSER;
  const cache = path.join(os.homedir(),'.cache','ms-playwright');
  for(const name of fs.readdirSync(cache).filter(name=>name.startsWith('chromium-')).sort().reverse()) {
    const binary = path.join(cache,name,'chrome-linux64','chrome');
    if(fs.existsSync(binary)) return binary;
  }
  throw Error('Set SHEET_REVIEW_BROWSER to a local Chromium executable.');
}

async function main() {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(),'structured-scope-ui-'));
  let chrome, socket;
  const server = http.createServer((req,res)=>{
    const url = new URL(req.url,'http://localhost'), route = url.pathname.replace(/^\/structured(?=\/)/,'');
    requests.push({path:url.pathname,scope:req.headers['x-knowledge-base-id'] || '',tenant:req.headers['x-tenant-id'] || '',authorization:req.headers.authorization || ''});
    let body = {}, type = 'application/json';
    if(route==='/harness') {
      body = `<!doctype html><iframe id="frame" src="/structured/" style="width:100%;height:100vh;border:0"></iframe><script>
        const frame=document.getElementById('frame');window.auth=(kb,token='fixture-token')=>frame.contentWindow.postMessage({type:'structured-auth',token,tenant:10000,knowledgeBaseId:kb},location.origin);
        window.view=mode=>frame.contentWindow.postMessage({type:'structured-view',mode},location.origin);
        addEventListener('message',e=>{if(e.origin===location.origin&&e.source===frame.contentWindow&&e.data?.type==='structured-ready'){auth(new URLSearchParams(location.search).get('kb')||'');if(new URLSearchParams(location.search).get('kb'))view('import')}});
        </script>`;
      type = 'text/html';
    } else if(route==='/') {body=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
    else if(['/sheet-review.js','/sheet-review.css','/workspace.css'].includes(route)){body=fs.readFileSync(path.join(source,route.slice(1)),'utf8');type=route.endsWith('.js')?'application/javascript':'text/css';}
    else if(route==='/pdf-review.js'){body=fs.readFileSync(path.join(source,'static','pdf-review.js'),'utf8');type='application/javascript';}
    else if(route==='/api/ui-config')body={title:'Fixture',workspace_label:'Fixture',weknora_url:'http://127.0.0.1'};
    else if(route==='/api/datasets')body={datasets:[]};
    else if(route==='/api/imports')body={imports:[{import_id:importId,source_file:'Same.xlsx'}]};
    else if(route==='/api/pdf/config')body={configured:false,max_bytes:52428800};
    else if(route==='/api/pdf/drafts')body={drafts:[]};
    else if(route==='/api/pdf/tables')body={tables:[]};
    else if(route.endsWith('/sheet'))body=sheet;
    else if(route.endsWith('/sheet-view'))body={sheet_name:sheet.name,total_rows:3,total_cols:2,range:{start_row:1,end_row:3,start_col:1,end_col:2,row_count:3,col_count:2},cells:[{row:1,column:1,cell:'A1',value:'Date',display:'Date'},{row:1,column:2,cell:'B1',value:'Metric',display:'Metric'},{row:2,column:1,cell:'A2',value:'2023-01-01',display:'2023-01-01'},{row:2,column:2,cell:'B2',value:100,display:'100'}],styles:{},merges:[],column_widths:{},row_heights:{},warnings:[]};
    else if(route.startsWith('/api/imports/'))body={import_id:importId,source_file:'Same.xlsx',sheets:[sheet]};
    else {res.writeHead(404,{'Content-Type':type});res.end(JSON.stringify({detail:'Unsupported fixture route'}));return;}
    res.writeHead(200,{'Content-Type':type});res.end(typeof body==='string'?body:JSON.stringify(body));
  });
  try {
    server.listen(0,'127.0.0.1');await once(server,'listening');
    const origin = `http://127.0.0.1:${server.address().port}`;
    const launched=await launchBrowser({executablePath:browserPath(),profileDir:profile});chrome=launched.browser;
    socket=new WebSocket(launched.page.webSocketDebuggerUrl);await once(socket,'open');
    let serial=0;const pending=new Map();
    socket.addEventListener('message',event=>{const message=JSON.parse(event.data);if(message.id&&pending.has(message.id)){pending.get(message.id)(message);pending.delete(message.id);}else if(message.method==='Runtime.exceptionThrown')errors.push(message.params.exceptionDetails);});
    const call=async(method,params={})=>{const id=++serial;const result=new Promise(resolve=>pending.set(id,resolve));socket.send(JSON.stringify({id,method,params}));const response=await result;if(response.error)throw Error(JSON.stringify(response.error));return response.result;};
    const js=async expression=>{const response=await call('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(response.exceptionDetails)throw Error(response.exceptionDetails.exception?.description||JSON.stringify(response.exceptionDetails));return response.result.value;};
    const wait=async expression=>{for(let count=0;count<200;count++){if(await js(expression))return;await pause(50);}throw Error('Timed out: '+expression);};
    const verify=async(name,expression)=>{assert.equal(await js(expression),true,name);checks.push(name);};
    const screenshot=async name=>{if(!process.env.STRUCTURED_UI_SCREENSHOTS)return;fs.mkdirSync(process.env.STRUCTURED_UI_SCREENSHOTS,{recursive:true});const image=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(process.env.STRUCTURED_UI_SCREENSHOTS,name+'.png'),Buffer.from(image.data,'base64'));};
    const doc="document.getElementById('frame')?.contentDocument";
    const ready=`${doc}?.querySelector('.sheet-grid td') && ${doc}.querySelector('[data-column-field="unit"]')`;
    await call('Runtime.enable');await call('Page.enable');await call('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false});
    await call('Page.navigate',{url:origin+'/harness?kb=kb-a'});await wait(ready);
    assert(requests.some(request=>request.path==='/structured/api/imports'&&request.scope==='kb-a'&&request.tenant==='10000'&&request.authorization==='Bearer fixture-token'));checks.push('Verified scope travels on API requests');
    await verify('Import exposes existing upload and review',`(()=>{const d=${doc};return d.defaultView.getComputedStyle(d.getElementById('directUpload')).display!=='none'&&d.defaultView.getComputedStyle(d.getElementById('importReview')).display!=='none'})()`);
    await screenshot('01_结构化导入_原核对界面');
    await js(`(()=>{const d=${doc};window.reviewNode=d.getElementById('sheetDetail');const el=d.querySelector('[data-column-field="unit"]');el.value='Draft A';el.dispatchEvent(new Event('input',{bubbles:true}));window.view('query')})()`);await pause(100);
    await verify('Query keeps original review DOM and hides import',`(()=>{const d=${doc};return window.reviewNode===d.getElementById('sheetDetail')&&d.defaultView.getComputedStyle(d.getElementById('importReview')).display==='none'&&d.defaultView.getComputedStyle(d.getElementById('queryPreview')).display!=='none'})()`);
    await screenshot('02_结构化查询');
    await js("window.view('source')");await pause(100);
    await verify('Removed source view leaves query open and keeps its hidden draft',`(()=>{const d=${doc};return !d.body.classList.contains('view-source')&&window.reviewNode===d.getElementById('sheetDetail')&&d.querySelector('[data-column-field="unit"]').value==='Draft A'&&d.defaultView.getComputedStyle(d.getElementById('importReview')).display==='none'})()`);
    await js("window.view('import')");await pause(100);
    await verify('Returning to import restores the same original grid and unsaved field state',`(()=>{const d=${doc};return window.reviewNode===d.getElementById('sheetDetail')&&d.querySelector('[data-column-field="unit"]').value==='Draft A'&&d.defaultView.getComputedStyle(d.getElementById('importReview')).display!=='none'})()`);
    await screenshot('03_结构化导入_保留编辑');
    await js("window.auth('kb-a','fixture-renewed')");await pause(100);
    await verify('Token refresh retains field state and DOM',`window.reviewNode===${doc}.getElementById('sheetDetail')&&${doc}.querySelector('[data-column-field="unit"]').value==='Draft A'`);
    await js("window.auth('kb-b')");await wait(`${ready} && ${doc}.querySelector('[data-column-field="unit"]').value===''`);
    checks.push('Same Excel in another KB does not restore first KB draft');
    await js(`(()=>{const el=${doc}.querySelector('[data-column-field="unit"]');el.value='Draft B';el.dispatchEvent(new Event('input',{bubbles:true}));window.auth('kb-a')})()`);await wait(`${ready} && ${doc}.querySelector('[data-column-field="unit"]').value==='Draft A'`);
    checks.push('Switch back restores only original KB draft');
    await call('Page.navigate',{url:origin+'/harness?kb=kb-a'});await wait(`${ready} && ${doc}.querySelector('[data-column-field="unit"]').value==='Draft A'`);checks.push('Reload restores the same KB draft');
    await call('Page.navigate',{url:origin+'/harness'});await wait(`${ready} && ${doc}.querySelector('[data-column-field="unit"]').value===''`);checks.push('Legacy workbook does not restore KB draft');
    await js(`(()=>{const el=${doc}.querySelector('[data-column-field="unit"]');el.value='Legacy draft';el.dispatchEvent(new Event('input',{bubbles:true}))})()`);
    const legacy=['sheet-review:v1','10000',importId,sheet.name,sheet.fingerprint].map(encodeURIComponent).join(':');
    await verify('Legacy storage key remains byte-for-byte compatible',`JSON.parse(localStorage.getItem(${JSON.stringify(legacy)})).mapping.blocks[0].columns[0].unit==='Legacy draft'`);
    const before=requests.length;
    await js(`(()=>{const frame=document.getElementById('frame');frame.contentWindow.dispatchEvent(new MessageEvent('message',{origin:location.origin,source:frame.contentWindow,data:{type:'structured-auth',token:'bad',tenant:10001,knowledgeBaseId:'kb-spoof'}}));window.view('unrecognized')})()`);await pause(100);
    assert.equal(requests.length,before);checks.push('Non-parent auth and unknown modes ignored');
    assert(!requests.some(request=>request.path.includes('suggest')||request.path.includes('resolve_fields')));checks.push('No model endpoint called');
    assert.deepEqual(errors,[]);checks.push('No JavaScript errors');
    console.log(JSON.stringify({passed:checks.length,checks}));
  } finally {
    socket?.close();
    try {await stopBrowser(chrome);} finally {
      await new Promise(resolve=>server.close(resolve));
      fs.rmSync(profile,{recursive:true,force:true,maxRetries:5,retryDelay:100});
    }
  }
}
main().catch(error=>{console.error(error);process.exitCode=1;});

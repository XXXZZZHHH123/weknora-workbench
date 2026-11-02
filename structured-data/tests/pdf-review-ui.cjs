/* Exercise PDF draft review using isolated authenticated UI responses. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('./browser-launcher.cjs');

const source = process.env.STRUCTURED_UI_SOURCE || path.join(__dirname, '..', 'src', 'structured_data');
const indexHtml = fs.readFileSync(path.join(source, 'index.html'), 'utf8');
const workspaceCss = fs.readFileSync(path.join(source, 'workspace.css'), 'utf8');
const shellCss = indexHtml.match(/<style>([\s\S]*?)<\/style>/)?.[1] || '';
const sourceRoute = indexHtml.match(/function openStructuredKnowledgeSource\([\s\S]*?\nwindow\.pdfReviewHost\.routeKnowledgeSource=openStructuredKnowledgeSource;/)?.[0] || '';
assert.ok(shellCss.includes('view-import'), 'load knowledge view rules from the real structured workspace');
assert.ok(sourceRoute.includes("sourceType==='pdf'"), 'test the production PDF source routing function');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const bundleHash = 'a'.repeat(64);
const draft = {
  draft_id: 'pdfd_fixture', bundle_sha256: bundleHash, status: 'draft', extraction_verified: false,
  bundle: {
    source: {file_name: 'suspicious.pdf', sha256: bundleHash},
    parser: {engine: 'fixture-engine', version: '0'},
    formulas: [{latex: '\\frac{a}{b}', page: 1}],
    page_context: [{page: 1, block_type: 'text', text: 'Quantity (tonnes)', bbox: [0.1, 0.1, 0.4, 0.15]}],
    fragments: [{id: 'fragment-1', page: 1, bbox: [0.1, 0.2, 0.9, 0.8], rows: [
      [{text: 'Item'}, {text: 'Amount'}],
      [{text: 'North'}, {text: '12.50 <img src=x onerror=alert(1)>'}],
      [{text: ''}, {text: ''}]
    ]}]
  }
};
const plan = {
  bundle_sha256: bundleHash, fragment_ids: ['fragment-1'],
  header_rows: {'fragment-1': [{row: 0, reason: 'Review recognized column header'}]},
  columns: [
    {name: 'Item', type: 'text', source_columns: {'fragment-1': 0}},
    {name: 'Amount', type: 'text', source_columns: {'fragment-1': 1}}
  ], excluded_rows: [], boundary_merges: []
};
const preview = {
  status: 'validated_draft', extraction_verified: false,
  coverage: {logical_row_count: 1, selected_fragment_count: 1, source_document_completeness: 'unknown'},
  columns: plan.columns,
  rows: [{values: {Item: 'North', Amount: '12.50 <img src=x onerror=alert(1)>'}, source_cells: {
    Item: [{page: 1, row: 1, column: 0, text: 'North'}],
    Amount: [{page: 1, row: 1, column: 1, text: '12.50 <img src=x onerror=alert(1)>'}]
  }}]
};
draft.automation={status:'needs_attention',accepted_count:0,attention_count:1,groups:[{fragment_ids:['fragment-1'],status:'needs_attention',plan}]};
const calls = [];
let importCount = 0;
let jobCount = 0;

function browserPath() {
  if (process.env.SHEET_REVIEW_BROWSER) return process.env.SHEET_REVIEW_BROWSER;
  const cache = path.join(os.homedir(), '.cache', 'ms-playwright');
  for (const item of fs.readdirSync(cache).filter(name => name.startsWith('chromium-')).sort().reverse()) {
    const binary = path.join(cache, item, 'chrome-linux64', 'chrome');
    if (fs.existsSync(binary)) return binary;
  }
  throw new Error('No local Chromium executable found');
}

async function main() {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'pdf-review-ui-'));
  let browser, socket;
  const errors = [];
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    let status = 200, type = 'application/json', body = {};
    if (url.pathname === '/') {
      type = 'text/html';
      body = `<!doctype html><meta charset="utf-8"><style>${shellCss}${workspaceCss}</style><body class="embedded knowledge-view view-import document-results"><div id="workspace"><section id="pdfReviewPanel"><div id="pdfReviewApp"></div></section><div id="workspaceIntro"><section id="directUpload"></section></div><section id="importReview"></section><section id="queryWorkspace"></section></div>
      <script>let authEpoch=0,requestedSourceKnowledge='',requestedSourceType='xlsx';function openKnowledgeSource(){throw Error('XLSX route must not be used for PDF')};window.pdfReviewHost={getAuthEpoch:()=>authEpoch,isWorkspaceReady:()=>true,showWorkspace:()=>{},loadKnowledgeResult:async id=>window.pdfReviewHost.api('/api/knowledge-results/'+id),api:async(path,options={},ctx={})=>{
        if(ctx.signal?.aborted)throw Error('aborted');window.__apiCalls.push({path,method:options.method||'GET',body:options.body});
        if(path==='/api/pdf/config')return {configured:true,max_bytes:52428800};
        if(path==='/api/pdf/drafts')return {drafts:[{draft_id:'pdfd_fixture',source:{file_name:'suspicious.pdf'},fragment_count:1,raw_row_count:2}]};
        if(path==='/api/pdf/tables')return {tables:window.__published||[]};
        if(path==='/api/pdf/jobs')return {jobs:window.__jobs||[]};
        if(path==='/api/pdf/jobs/failed-job/retry'){window.__retried=(window.__retried||0)+1;return {id:'retry-child',status:'processing'}}
        if(path==='/api/pdf/import')return {id:'job-'+(++window.__jobCount)};
        if(path.startsWith('/api/knowledge-results/'))return {source_knowledge_id:path.split('/').at(-1),draft_id:'pdfd_fixture'};
        if(path==='/api/pdf/import-knowledge'){window.__knowledgeImportBody=JSON.parse(options.body);return {id:'job-'+(++window.__jobCount)}}
        if(path.startsWith('/api/pdf/jobs/'))return {status:'completed',draft_id:window.__jobDraftId||'pdfd_fixture'};
        if(path==='/api/pdf/drafts/pdfd_fixture')return window.__draft;
        if(path==='/api/pdf/drafts/pdfd_basic')return window.__basicDraft;
        if(path==='/api/pdf/drafts/pdfd_fixture/reparse'){window.__reparseBody=JSON.parse(options.body);window.__jobDraftId='pdfd_basic';return {id:'job-basic'}}
        if(path.endsWith('/suggest'))return {status:'ready',message:'待人工核对',plan:window.__plan,preview:window.__preview};
        if(path.endsWith('/preview'))return window.__preview;
        if(path.endsWith('/publish')){window.__published=[{table_id:'pdft_fixture',source:{file_name:'suspicious.pdf'},columns:window.__plan.columns,coverage:{logical_row_count:1},extraction_verified:false}];return {table_id:'pdft_fixture'};}
        throw Error('Unexpected API '+path);
      },authenticatedFetch:async(path,options={})=>{if(path.endsWith('/source')){window.__sourceFetches++;return new Response(new Blob(['%PDF-1.4']),{status:200,headers:{'content-type':'application/pdf'}})}throw Error('Unexpected protected fetch '+path)}};
      ${sourceRoute}
      window.__apiCalls=[];window.__jobCount=0;window.__sourceFetches=0;window.__published=[];window.__jobDraftId='pdfd_fixture';window.__draft=${JSON.stringify(draft)};window.__basicDraft={...window.__draft,draft_id:'pdfd_basic',bundle_sha256:'${'b'.repeat(64)}',bundle:{...window.__draft.bundle,parser:{engine:'mineru',version:'fixture',requested_tier:'basic',requested_ocr_mode:'ocr'}}};window.__plan=${JSON.stringify(plan)};window.__preview=${JSON.stringify(preview)};
      </script><script src="/pdf-review.js"></script></body>`;
    } else if (url.pathname === '/pdf-review.js') {
      type = 'text/javascript';
      body = fs.readFileSync(path.join(source, 'static', 'pdf-review.js'), 'utf8');
    } else { status = 404; body = {detail: 'Not found'}; }
    res.writeHead(status, {'Content-Type': type});
    res.end(typeof body === 'string' ? body : JSON.stringify(body));
  });

  try {
    server.listen(0, '127.0.0.1');
    await once(server, 'listening');
    const origin = `http://127.0.0.1:${server.address().port}`;
    const launched = await launchBrowser({executablePath: browserPath(), profileDir: profile});
    browser = launched.browser;
    socket = new WebSocket(launched.page.webSocketDebuggerUrl);
    await once(socket, 'open');
    let serial = 0;
    const pending = new Map();
    socket.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      if (message.id && pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id); }
      else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text);
    });
    const call = async (method, params = {}) => {
      const id = ++serial;
      const result = new Promise(resolve => pending.set(id, resolve));
      socket.send(JSON.stringify({id, method, params}));
      let timer;
      const response = await Promise.race([result,new Promise((_,reject)=>{timer=setTimeout(()=>reject(Error('CDP timeout: '+method)),30000)})]).finally(()=>clearTimeout(timer));
      if (response.error) throw Error(JSON.stringify(response.error));
      return response.result;
    };
    const evaluate = async expression => {
      const response = await call('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true});
      if (response.exceptionDetails) throw Error(response.exceptionDetails.exception?.description || response.exceptionDetails.text);
      return response.result.value;
    };
    const wait = async (expression, timeoutMs = 12000) => {
      const until = Date.now() + timeoutMs;
      while (Date.now() < until) {
        if (await evaluate(expression)) return;
        await pause(50);
      }
      throw Error(`Timed out waiting for ${expression}`);
    };
    await call('Runtime.enable');
    await call('Page.enable');
    await call('Page.navigate', {url: origin + '/'});
    await wait("document.querySelector('#pdfUpload')");
    await wait("document.querySelector('#pdfDraftList')?.textContent.includes('pdfd_fixture')");
    await evaluate(`(()=>{const i=document.querySelector('#pdfUploadFile');const f=new File(['%PDF-1.4'],'table.pdf',{type:'application/pdf'});Object.defineProperty(i,'files',{value:[f],configurable:true});i.dispatchEvent(new Event('change',{bubbles:true}));document.querySelector('#pdfUpload').click()})()`);
    await wait("document.querySelector('#pdfDraftDetail')?.textContent.includes('12.50 <img')");
    await wait("document.querySelector('#pdfTogglePreview')");
    await evaluate("if(document.querySelector('#pdfTogglePreview').getAttribute('aria-pressed')==='true')document.querySelector('#pdfTogglePreview').click()");
    assert.equal(await evaluate('window.__sourceFetches'), 1, 'draft source must use the protected fetch adapter');
    assert.equal(await evaluate("document.querySelectorAll('#pdfDraftDetail img').length"), 0, 'raw cell strings must not become HTML');
    assert.doesNotMatch(await evaluate("document.querySelector('#pdfDraftDetail').innerText"), /更多来源线索|页面上下文|高级映射|表格设置|SHA256/);
    assert.equal(await evaluate("document.querySelector('#pdfPlanEditor').hidden"),true);
    assert.equal(await evaluate("window.__apiCalls.filter(x=>x.path.endsWith('/publish')).length"),0);
    await evaluate("window.__preview.coverage.unresolved_quality_issues=[{fragment_id:'fragment-1',row:1,column:1,code:'uncertain_cell'}];document.querySelector('#pdfPublish').click()");
    await wait("document.querySelector('.pdf-review-findings')?.textContent.includes('Amount')");
    assert.equal(await evaluate("window.__apiCalls.filter(x=>x.path.endsWith('/publish')).length"),0);
    await evaluate("const cell=document.querySelector('#pdfPreview textarea[data-source-row=\"1\"][data-source-column=\"1\"]');cell.value='12.50';cell.dispatchEvent(new Event('input',{bubbles:true}))");
    const correction=await evaluate("JSON.parse(document.querySelector('#pdfPlanEditor').value).cell_corrections[0]");
    assert.equal(correction.text,'12.50');assert.match(correction.reason,/第 1 页，第 2 行，第 2 列/);
    await evaluate("delete window.__preview.coverage.unresolved_quality_issues;const i=document.querySelector('#pdfPreview [data-column-name=\"1\"]');i.value='Amount edited';i.dispatchEvent(new Event('input',{bubbles:true}))");
    const beforePreview=await evaluate("window.__apiCalls.filter(x=>x.path.endsWith('/preview')).length");
    await evaluate("document.querySelector('#pdfPublish').click()");await wait("document.querySelector('#pdfPublish')?.textContent==='已入库'");
    assert.ok(await evaluate("window.__apiCalls.filter(x=>x.path.endsWith('/preview')).length")>beforePreview);
    assert.equal(await evaluate("window.__apiCalls.filter(x=>x.path.endsWith('/publish')).length"),1);
    assert.equal(await evaluate("JSON.parse(window.__apiCalls.find(x=>x.path.endsWith('/publish')).body).reviewed"),true);
    await evaluate("[...document.querySelectorAll('button')].find(b=>b.textContent==='重新解析').click()");
    await wait('window.__sourceFetches===2');
    assert.deepEqual(await evaluate('window.__reparseBody'), {tier:'standard',ocr_mode:'auto'});
    assert.equal(await evaluate("window.__apiCalls.filter(x=>x.path.endsWith('/publish')).length"),1);
    assert.deepEqual(errors, []);

    await evaluate("window.authEpoch++;window.dispatchEvent(new Event('structured-auth-scope-change'))");
    await wait("!document.querySelector('#pdfDraftDetail')?.textContent.includes('suspicious.pdf')");
    assert.match(await evaluate("document.querySelector('#pdfReviewApp').textContent"), /账号或知识库已切换/);
    assert.equal(await evaluate("getComputedStyle(document.querySelector('#pdfReviewPanel')).display"), 'block', 'PDF panel must remain visible in the real import-view CSS');
    await evaluate("window.pdfReviewHost.routeKnowledgeSource('source-pdf-1','pdf')");
    await wait("document.querySelector('#pdfDraftDetail')?.textContent.includes('suspicious.pdf')");
    assert.equal(await evaluate("document.body.classList.contains('pdf-native-source')"), true, 'production route must switch to PDF-only native source mode');
    assert.equal(await evaluate("getComputedStyle(document.querySelector('#directUpload')).display"), 'none', 'PDF source mode must hide the Excel importer');
    assert.equal(await evaluate("window.__apiCalls.some(x=>x.path==='/api/knowledge-results/source-pdf-1'&&x.method==='GET')"), true, 'knowledge PDF must read its existing result with the real source ID');
    assert.equal(await evaluate('window.__sourceFetches'), 3, 'each selected draft must retrieve its protected original');
    await evaluate("window.__jobs=[{id:'failed-job',status:'failed',retry_available:true,file_name:'retry.pdf',error:'Provider unavailable'}];[...document.querySelectorAll('button')].find(b=>b.textContent==='刷新记录').click()");
    await wait("document.querySelector('#pdfJobs')?.textContent.includes('Provider unavailable')");
    assert.equal(await evaluate("window.__retried||0"), 0, 'loading failed jobs must not retry automatically');
    await evaluate("[...document.querySelectorAll('#pdfJobs button')].find(b=>b.textContent==='手动重试此文件').click()");
    await wait("window.__retried===1 && window.__sourceFetches===4");
    assert.equal(await evaluate("window.__apiCalls.filter(x=>x.path==='/api/pdf/jobs/failed-job/retry').length"), 1, 'explicit retry targets only the chosen scoped job');
    await evaluate("window.authEpoch++;window.dispatchEvent(new Event('structured-auth-scope-change'))");
    await wait("!document.querySelector('#pdfDraftDetail')?.textContent.includes('suspicious.pdf')");
    await evaluate("document.body.classList.remove('view-import');document.body.classList.add('view-query')");
    assert.equal(await evaluate("getComputedStyle(document.querySelector('#pdfReviewPanel')).display"), 'block', 'PDF published information remains accessible in query view');
    console.log('PASS: protected source fetch, PDF knowledge import, reparse into a separate draft, direct header editing and source cell navigation, plan preview invalidation, explicit publish confirmation, and stale-scope clearing.');
  } finally {
    socket?.close();
    await stopBrowser(browser);
    server.close();
    fs.rmSync(profile, {recursive: true, force: true});
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });

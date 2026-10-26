/* Shipped field-model overlay against isolated HTTP fixtures; no model inference. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('./browser-launcher.cjs');
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const source = path.join(__dirname, '..', 'src', 'structured_data');
const output = process.env.STRUCTURED_UI_ARTIFACTS;
const checks = [], errors = [], writes = [], reads = [];
const selections = new Map([['kb-a','native-a'], ['kb-b','native-b']]);
let slow = false, failure = false;

function browserPath() {
  if (process.env.SHEET_REVIEW_BROWSER) return process.env.SHEET_REVIEW_BROWSER;
  const cache = path.join(os.homedir(), '.cache', 'ms-playwright');
  for (const item of fs.readdirSync(cache).filter(name => name.startsWith('chromium-')).sort().reverse()) {
    const file = path.join(cache, item, 'chrome-linux64', 'chrome');
    if (fs.existsSync(file)) return file;
  }
  throw Error('Local Chromium unavailable');
}

async function main() {
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'native-model-ui-'));
  let browser, socket;
  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://localhost'), route = url.pathname.replace(/^\/structured(?=\/)/, '');
      if (route === '/harness') {
        res.setHeader('Content-Type', 'text/html');
        res.end(`<iframe id="frame" src="/structured/?view=model-settings" style="position:fixed;inset:0;width:100%;height:100%;border:0"></iframe><script>
          const frame=document.getElementById('frame');window.auth=(kb='kb-a',user='user-a')=>frame.contentWindow.postMessage({type:'structured-auth',token:'fixture-'+user,tenant:10000,userId:user,knowledgeBaseId:kb},location.origin);
          addEventListener('message',e=>{if(e.origin===location.origin&&e.source===frame.contentWindow&&e.data?.type==='structured-ready'){auth();frame.contentWindow.postMessage({type:'structured-view',mode:'import'},location.origin)}});
          window.openModels=()=>frame.contentWindow.postMessage({type:'structured-open-model-settings'},location.origin);
        </script>`); return;
      }
      let result = {}, status = 200;
      if (route === '/api/ui-config') result = {};
      else if (route === '/api/datasets') result = {datasets: []};
      else if (route === '/api/imports') result = {imports: []};
      else if (route === '/api/model-settings') {
        const kb = req.headers['x-knowledge-base-id'], user = req.headers.authorization;
        if (req.method === 'POST') {
          const chunks = []; for await (const chunk of req) chunks.push(chunk);
          const body = JSON.parse(Buffer.concat(chunks).toString());
          writes.push({kb, user, body}); selections.set(kb, body.native_model_id);
          result = {native_model_id: body.native_model_id, protocol: 'platform'};
        } else {
          reads.push({kb, user}); if (slow) await pause(650);
          if (failure) { status = 503; result = {detail: 'Fixture model catalogue unavailable'}; }
          else result = {native_model_id: selections.get(kb) || '', protocol: 'platform', can_edit: user !== 'Bearer fixture-reader',
            models: [{id:'native-a',name:'Configured model A'}, {id:'native-b',name:'Configured model B'}]};
        }
      } else {
        const file = route === '/' ? 'index.html' : route.slice(1);
        if (!['pdf-query.js','index.html','sheet-review.js','sheet-review.css','workspace.css'].includes(file)) { res.writeHead(404); res.end(); return; }
        res.setHeader('Content-Type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html');
        res.end(fs.readFileSync(path.join(source, file))); return;
      }
      res.writeHead(status, {'Content-Type': 'application/json'}); res.end(JSON.stringify(result));
    } catch (error) { res.writeHead(500); res.end(JSON.stringify({detail: error.message})); }
  });
  try {
    server.listen(0,'127.0.0.1'); await once(server,'listening');
    const origin = 'http://127.0.0.1:' + server.address().port;
    const launched = await launchBrowser({executablePath: browserPath(), profileDir: profile}); browser = launched.browser;
    socket = new WebSocket(launched.page.webSocketDebuggerUrl); await once(socket, 'open');
    let serial = 0; const pending = new Map();
    socket.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      if (message.id && pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id); }
      else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails);
    });
    const call = async(method, params={}) => {
      const id = ++serial, value = new Promise(resolve => pending.set(id,resolve));
      socket.send(JSON.stringify({id,method,params})); const result = await value;
      if (result.error) throw Error(JSON.stringify(result.error)); return result.result;
    };
    const js = async(expression) => {
      const value = await call('Runtime.evaluate', {expression,awaitPromise:true,returnByValue:true});
      if (value.exceptionDetails) throw Error(value.exceptionDetails.exception?.description || JSON.stringify(value.exceptionDetails));
      return value.result.value;
    };
    const inner = expression => js(`(()=>{const d=document.getElementById('frame')?.contentDocument,w=d?.defaultView;return ${expression}})()`);
    const wait = async(expression) => {
      for (let i=0;i<160;i++) { if (await inner(expression)) return; await pause(40); }
      throw Error('Timed out: ' + expression);
    };
    const verify = async(name,expression) => { assert.equal(await inner(expression),true,name); checks.push(name); };
    await call('Runtime.enable'); await call('Page.enable');
    await call('Emulation.setDeviceMetricsOverride', {width:1280,height:900,deviceScaleFactor:1,mobile:false});
    await call('Page.navigate', {url:origin+'/harness'});
    await wait(`d?.getElementById('nativeModel')?.value==='native-a' && d.getElementById('modelSettings').open`);
    await verify('Legacy settings link opens a compact overlay without replacing the import workspace', `w.getComputedStyle(d.getElementById('workspace')).display!=='none' && d.getElementById('modelSettings').getBoundingClientRect().width<=360 && !d.getElementById('modelBase') && !d.getElementById('modelKey')`);
    assert.equal(writes.length,0); checks.push('Opening settings never invokes a model or saves configuration');
    await inner(`(()=>{d.getElementById('nativeModel').value='native-b';d.getElementById('modelForm').requestSubmit();return true})()`);
    await wait(`d.getElementById('modelStatus').textContent.includes('已保存')`);
    assert.deepEqual(writes,[{kb:'kb-a',user:'Bearer fixture-user-a',body:{native_model_id:'native-b'}}]); checks.push('Saving sends only the selected native model ID under the current KB identity');
    await inner(`(()=>{d.getElementById('modelClose').click();return true})()`);
    await verify('Closing preserves the import page and URL', `!d.getElementById('modelSettings').open && w.getComputedStyle(d.getElementById('workspace')).display!=='none'`);
    await js(`auth('kb-b','reader')`); await js('openModels()');
    await wait(`d.getElementById('modelSettings').open && d.getElementById('modelStatus').textContent.includes('仅可查看')`);
    await verify('Read-only KB members see the selected model but cannot change it', `d.getElementById('nativeModel').value==='native-b' && d.getElementById('nativeModel').disabled && d.getElementById('modelSave').disabled`);
    await inner(`(()=>{d.getElementById('modelClose').click();return true})()`);
    slow = true; await js(`auth('kb-a','user-a')`); await js('openModels()');
    await wait(`d.getElementById('modelStatus').getAttribute('aria-busy')==='true'`);
    await js(`auth('kb-b','user-b')`); slow = false; await js('openModels()');
    await wait(`d.getElementById('modelSettings').open && !d.getElementById('nativeModel').disabled && d.getElementById('nativeModel').value==='native-b'`);
    await pause(750);
    await verify('Switching user and KB cancels the old catalogue response', `d.getElementById('nativeModel').value==='native-b' && !d.getElementById('nativeModel').disabled && d.getElementById('modelStatus').getAttribute('aria-busy')==='false'`);
    await inner(`(()=>{d.getElementById('modelClose').click();return true})()`);
    failure = true; await js('openModels()'); await wait(`d.getElementById('modelStatus').textContent.includes('unavailable')`);
    await verify('Catalogue errors are recoverable inside the overlay', `d.getElementById('modelSettings').open && d.getElementById('modelSave').disabled`);
    failure = false; await inner(`(()=>{d.getElementById('modelRetry').click();return true})()`);
    await wait(`!d.getElementById('modelSave').disabled`); checks.push('Refresh retries failed catalogue loading');
    await inner(`(()=>{d.documentElement.setAttribute('theme-mode','dark');return true})()`);
    await verify('Overlay inherits the platform dark palette', `w.getComputedStyle(d.getElementById('modelSettings')).backgroundColor==='rgb(36, 32, 29)'`);
    assert.deepEqual(errors,[]); checks.push('No JavaScript runtime exceptions');
    if (output) { fs.mkdirSync(output,{recursive:true}); const screenshot = await call('Page.captureScreenshot',{format:'png'}); fs.writeFileSync(path.join(output,'native-model-overlay.png'),Buffer.from(screenshot.data,'base64')); fs.writeFileSync(path.join(output,'native-model-ui.json'),JSON.stringify({checks,reads,writes,real_model_calls:0},null,2)); }
    console.log(JSON.stringify({passed:checks.length,checks,real_model_calls:0}));
  } finally { socket?.close(); await stopBrowser(browser); server.closeAllConnections(); await new Promise(resolve=>server.close(resolve)); fs.rmSync(profile,{recursive:true,force:true}); }
}
main().catch(error=>{console.error(error);process.exitCode=1;});

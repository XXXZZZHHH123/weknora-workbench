/* Verify the real restricted renderer and layout switch with synthetic HTML. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('../../structured-data/tests/browser-launcher.cjs');
const root = path.resolve(__dirname, '../..');
const output = process.env.RESTRICTED_DIAGRAM_ARTIFACTS;
if (!output || !fs.existsSync(path.join(output, 'diagram-first.html'))
    || !fs.existsSync(path.join(output, 'diagram-second.html'))) {
  throw Error('Set RESTRICTED_DIAGRAM_ARTIFACTS to the HTML artifacts from the multi-layout template test.');
}
const staticRoot = path.join(root, 'commodity-research/src/tin/web/static');
const fontRoot = path.join(root, 'WeKnora/frontend/public/fonts');
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'readonly-diagrams-ui-'));
const checks = [], requests = [], errors = [];
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
let browser, socket;
function browserPath() {
  if (process.env.SHEET_REVIEW_BROWSER) return process.env.SHEET_REVIEW_BROWSER;
  const cache = path.join(os.homedir(), '.cache/ms-playwright');
  for (const folder of fs.readdirSync(cache).filter(name => name.startsWith('chromium-')).sort().reverse()) {
    const file = path.join(cache, folder, 'chrome-linux64/chrome');
    if (fs.existsSync(file)) return file;
  }
  throw Error('Set SHEET_REVIEW_BROWSER to a local Chromium binary.');
}
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  requests.push({method:req.method, path:url.pathname, query:url.search});
  let file;
  if (url.pathname === '/' || url.pathname.endsWith('/sn/diagram')) {
    file = path.join(output, url.searchParams.get('template') === '2' ? 'diagram-second.html' : 'diagram-first.html');
  } else if (url.pathname.startsWith('/fonts/')) {
    file = path.resolve(fontRoot, url.pathname.slice('/fonts/'.length));
    if (!file.startsWith(fontRoot + path.sep)) file = null;
  } else if (url.pathname.includes('/static/')) {
    const name = path.basename(url.pathname);
    if (/\.(css|js)$/.test(name)) file = path.join(staticRoot, name);
  }
  if (!file || !fs.existsSync(file)) { res.writeHead(404); res.end(); return; }
  const types = {'.html':'text/html; charset=utf-8', '.css':'text/css', '.js':'text/javascript', '.woff2':'font/woff2'};
  res.writeHead(200, {'Content-Type':types[path.extname(file)] || 'application/octet-stream'});
  fs.createReadStream(file).pipe(res);
});
async function main() {
  try {
    server.listen(0, '127.0.0.1'); await once(server, 'listening');
    const origin = `http://127.0.0.1:${server.address().port}`;
    const launched = await launchBrowser({executablePath:browserPath(), profileDir:profile}); browser = launched.browser;
    socket = new WebSocket(launched.page.webSocketDebuggerUrl); await once(socket, 'open');
    let serial = 0; const waiting = new Map();
    socket.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      if (message.id && waiting.has(message.id)) { waiting.get(message.id)(message); waiting.delete(message.id); }
      else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails);
    });
    const call = async (method, params = {}) => {
      const id = ++serial, pending = new Promise(resolve => waiting.set(id, resolve));
      socket.send(JSON.stringify({id, method, params})); const response = await pending;
      if (response.error) throw Error(JSON.stringify(response.error)); return response.result;
    };
    const js = async expression => {
      const response = await call('Runtime.evaluate', {expression, awaitPromise:true, returnByValue:true});
      if (response.exceptionDetails) throw Error(response.exceptionDetails.exception?.description || JSON.stringify(response.exceptionDetails));
      return response.result.value;
    };
    const wait = async expression => {
      for (let count = 0; count < 150; count++) { if (await js(expression)) return; await pause(50); }
      throw Error('Timed out: ' + expression);
    };
    const verify = async (name, expression) => { assert.equal(await js(expression), true, name); checks.push(name); };
    await call('Runtime.enable'); await call('Page.enable');
    await call('Emulation.setDeviceMetricsOverride', {width:1440, height:1000, deviceScaleFactor:1, mobile:false});
    await call('Page.navigate', {url:origin});
    await wait('window.DG_RESTRICTED === true && document.querySelectorAll("#dg-nodes > g").length > 0');
    await verify('All saved layouts are selectable without edit controls', 'document.querySelectorAll("#dg-template option").length === 2 && !document.querySelector("#dg-template option[value=__new], #dg-edit-btn, #dg-md-btn, #dg-drawer")');
    await verify('Complete original graph is drawn', 'document.getElementById("dg-canvas").textContent.includes("Measured component") && document.getElementById("dg-canvas").textContent.includes("Historical reference") && document.querySelectorAll("#dg-groups > g").length === 2');
    await js('(()=>{const select=document.getElementById("dg-template");select.value="2";select.dispatchEvent(new Event("change",{bubbles:true}))})()');
    await wait('window.DG_TEMPLATE?.id === 2 && document.getElementById("dg-canvas").textContent.includes("Second layout node")');
    await verify('Switching uses the selected authorized layout', 'location.search === "?template=2" && document.getElementById("dg-template").value === "2" && !document.getElementById("dg-canvas").textContent.includes("Measured component")');
    await js('dgToggleEdit()');
    await verify('Read-only renderer refuses entering edit mode', '!document.getElementById("dg-wrap").classList.contains("dg-editing") && document.getElementById("dg-tools").classList.contains("hidden")');
    assert(!requests.some(request => request.method !== 'GET' || request.path.includes('/api/')));
    checks.push('No catalog, history, arbitrary query, export, model or mutation endpoint is called');
    assert.deepEqual(errors, []); checks.push('No browser runtime errors');
    const screenshot = await call('Page.captureScreenshot', {format:'png'});
    fs.writeFileSync(path.join(output, 'readonly-diagram-switch.png'), Buffer.from(screenshot.data, 'base64'));
    fs.writeFileSync(path.join(output, 'browser-results.json'), JSON.stringify({passed:checks.length, checks, requests, errors}, null, 2));
    console.log(JSON.stringify({passed:checks.length, checks}));
  } finally {
    socket?.close(); await stopBrowser(browser);
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(profile, {recursive:true, force:true, maxRetries:5, retryDelay:100});
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });

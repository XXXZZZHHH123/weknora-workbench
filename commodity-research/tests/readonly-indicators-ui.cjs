/* Use HTML rendered by test_restricted_indicator_template.py and real shipped assets. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('../../structured-data/tests/browser-launcher.cjs');
const root = path.resolve(__dirname, '../..');
const output = process.env.RESTRICTED_INDICATOR_ARTIFACTS;
if (!output || !fs.existsSync(path.join(output, 'indicators.html'))) {
  throw Error('Set RESTRICTED_INDICATOR_ARTIFACTS to the synthetic HTML artifact from the template test.');
}
const staticRoot = path.join(root, 'commodity-research/src/tin/web/static');
const fontRoot = path.join(root, 'WeKnora/frontend/public/fonts');
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'readonly-indicators-ui-'));
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
  requests.push({method: req.method, path: url.pathname});
  let file;
  if (url.pathname === '/') file = path.join(output, 'indicators.html');
  else if (url.pathname.startsWith('/fonts/')) {
    const relative = url.pathname.slice('/fonts/'.length);
    file = path.resolve(fontRoot, relative);
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
    const launched = await launchBrowser({executablePath: browserPath(), profileDir: profile}); browser = launched.browser;
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
    await wait('document.getElementById("indicator-result-count")?.innerText === "1 / 1 项"');
    await verify('Original indicator catalog and latest values are visible', 'document.getElementById("indicator-workspace").dataset.readonly === "true" && document.querySelectorAll("[data-indicator-row]").length === 1 && document.querySelector(".indicator-table").innerText.includes("17.50")');
    await verify('No favorite, comparison, detail or historical action is offered', '!document.querySelector("#indicator-workspace [data-favorite], #indicator-workspace [data-compare], #indicator-workspace [data-series-chart], #indicator-favorites, #indicator-compare-dialog")');
    await js('(()=>{const input=document.getElementById("indicator-search");input.value="not-in-bound-set";input.dispatchEvent(new Event("input",{bubbles:true}))})()');
    await verify('Search filters only the loaded DOM and displays its empty state', 'document.querySelector("[data-indicator-row]").hidden && !document.getElementById("indicator-no-results").hidden && document.getElementById("indicator-result-count").innerText === "0 / 1 项"');
    await js('document.getElementById("indicator-search-clear").click()');
    await verify('Clearing restores the same bound row', '!document.querySelector("[data-indicator-row]").hidden && document.getElementById("indicator-result-count").innerText === "1 / 1 项"');
    await js('document.getElementById("indicator-collapse").click()');
    await verify('Existing collapse interaction works without a request', '!document.querySelector("[data-indicator-group]").open');
    await js('document.getElementById("indicator-expand").click()');
    await verify('Existing expand interaction works without a request', 'document.querySelector("[data-indicator-group]").open');
    await verify('Local filter adds no unsupported query arguments', 'location.search === ""');
    assert(!requests.some(request => request.method !== 'GET' || request.path.includes('/api/')));
    assert(!requests.some(request => /\/(indicator-preferences|indicators)\.js$/.test(request.path)));
    checks.push('No full catalog, preferences, history, model or mutation endpoint called');
    assert.deepEqual(errors, []); checks.push('No browser runtime errors');
    const image = await call('Page.captureScreenshot', {format:'png'});
    fs.writeFileSync(path.join(output, 'readonly-indicators.png'), Buffer.from(image.data, 'base64'));
    fs.writeFileSync(path.join(output, 'browser-results.json'), JSON.stringify({passed:checks.length, checks, requests, errors}, null, 2));
    console.log(JSON.stringify({passed:checks.length, checks}));
  } finally {
    socket?.close(); await stopBrowser(browser);
    await new Promise(resolve => server.close(resolve));
    fs.rmSync(profile, {recursive:true, force:true, maxRetries:5, retryDelay:100});
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });

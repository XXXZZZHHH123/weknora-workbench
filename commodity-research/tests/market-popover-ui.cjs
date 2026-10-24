// Isolated actual Chromium regression: shipped card HTML/app.js, no account or data writes.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const {once} = require('node:events');
const {launchBrowser, stopBrowser} = require('../../structured-data/tests/browser-launcher.cjs');
const output = process.env.MARKET_POPOVER_ARTIFACTS;
if (!output || !fs.existsSync(path.join(output, 'popover.html'))) throw Error('Render the isolated card fixture first.');
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'market-popover-'));
const cache = path.join(os.homedir(), '.cache/ms-playwright');
const executable = process.env.SHEET_REVIEW_BROWSER || fs.readdirSync(cache).filter(name => name.startsWith('chromium-'))
  .sort().reverse().map(name => path.join(cache, name, 'chrome-linux64/chrome')).find(file => fs.existsSync(file));
const app = path.resolve(__dirname, '../src/tin/web/static/app.js');
const errors = [];
const server = http.createServer((request, response) => {
  const file = request.url === '/' ? path.join(output, 'popover.html') : request.url === '/app.js' ? app : null;
  response.writeHead(file ? 200 : 404, {'Content-Type': request.url === '/app.js' ? 'text/javascript' : 'text/html'});
  if (file) fs.createReadStream(file).pipe(response); else response.end();
});
let browser, socket;
(async () => {
  try {
    server.listen(0, '127.0.0.1'); await once(server, 'listening');
    const launched = await launchBrowser({executablePath: executable, profileDir: profile}); browser = launched.browser;
    socket = new WebSocket(launched.page.webSocketDebuggerUrl); await once(socket, 'open');
    let serial = 0; const waiting = new Map();
    socket.addEventListener('message', event => {
      const message = JSON.parse(event.data);
      if (message.id && waiting.has(message.id)) { waiting.get(message.id)(message); waiting.delete(message.id); }
      else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails);
    });
    async function call(method, params = {}) {
      const id = ++serial, result = new Promise(resolve => waiting.set(id, resolve));
      socket.send(JSON.stringify({id, method, params})); const message = await result;
      if (message.error) throw Error(JSON.stringify(message.error)); return message.result;
    }
    async function evaluate(expression) {
      const value = await call('Runtime.evaluate', {expression, returnByValue: true, awaitPromise: true});
      if (value.exceptionDetails) throw Error(value.exceptionDetails.exception?.description || 'Browser evaluation failed');
      return value.result.value;
    }
    await call('Runtime.enable'); await call('Page.enable');
    await call('Page.navigate', {url: `http://127.0.0.1:${server.address().port}/`});
    for (let count = 0; count < 100; count++) {
      if (await evaluate('document.readyState === "complete" && typeof window.togglePopover === "function"')) break;
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    const control = await evaluate(`(() => {
      const button = document.createElement('button');
      button.setAttribute('onclick', "togglePopover('not-a-native-popover')"); document.body.append(button);
      try { button.onclick.call(button); return 'did-not-reproduce'; } catch(error) { return error.name; }
      finally { button.remove(); }
    })()`);
    assert.equal(control, 'NotSupportedError', 'Negative control must reproduce the native HTMLElement name collision');
    assert.equal(await evaluate('document.querySelector("[data-popover]").classList.contains("hidden")'), true);
    await evaluate('document.querySelector("[data-popover-trigger]").click()');
    assert.equal(await evaluate('getComputedStyle(document.querySelector("[data-popover]")).display !== "none"'), true);
    assert.equal(await evaluate('document.querySelector("[data-card-full-name]").getBoundingClientRect().height > 0'), true);
    await evaluate('document.querySelector("[data-popover] button").click()');
    assert.equal(await evaluate('document.querySelector("[data-popover]").classList.contains("hidden")'), true);
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(output, 'popover-browser-results.json'), JSON.stringify({passed: 6, errors, nativeCollision: control}));
    console.log('6 browser checks passed: native collision reproduced; application open/full-name/close remain correct.');
  } finally {
    socket?.close(); await stopBrowser(browser); server.close(); fs.rmSync(profile, {recursive: true, force: true});
  }
})().catch(error => { console.error(error); process.exitCode = 1; });

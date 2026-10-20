/* Shared only by local UI acceptance tests; startup failures must remain failures. */
const fs = require('node:fs/promises');
const path = require('node:path');
const {spawn} = require('node:child_process');
const {performance} = require('node:perf_hooks');

const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const OUTPUT_LIMIT = 16 * 1024;

function exitsWithin(browser, timeoutMs) {
  if (browser.exitCode !== null || browser.signalCode !== null) return Promise.resolve(true);
  return new Promise(resolve => {
    const finish = stopped => {
      clearTimeout(timer);
      browser.removeListener('close', closed);
      resolve(stopped);
    };
    const closed = () => finish(true);
    const timer = setTimeout(() => finish(false), timeoutMs);
    browser.once('close', closed);
  });
}

async function stopBrowser(browser) {
  if (!browser?.pid || browser.exitCode !== null || browser.signalCode !== null) return;
  let stopped = exitsWithin(browser, 1000);
  browser.kill('SIGTERM');
  if (await stopped) return;
  stopped = exitsWithin(browser, 1000);
  browser.kill('SIGKILL');
  if (!await stopped) throw new Error(`Browser process ${browser.pid} did not exit after SIGKILL`);
}

function pageTarget(value, port) {
  if (!Array.isArray(value)) return undefined;
  return value.find(item => {
    if (item?.type !== 'page' || typeof item.webSocketDebuggerUrl !== 'string') return false;
    try {
      const url = new URL(item.webSocketDebuggerUrl);
      return url.protocol === 'ws:' && ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)
        && Number(url.port) === port;
    } catch { return false; }
  });
}

async function launchBrowser({executablePath, profileDir, timeoutMs = 60000}) {
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) throw new Error('Browser startup timeout must be positive');
  const deadline = performance.now() + timeoutMs;
  let output = '', spawnError, lastProbe = 'DevToolsActivePort has not appeared';
  const browser = spawn(executablePath, [
    '--headless=new', '--no-sandbox', '--disable-gpu', '--disable-background-networking',
    '--disable-dev-shm-usage', '--no-first-run', '--remote-debugging-port=0',
    `--user-data-dir=${profileDir}`,
    '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1', 'about:blank',
  ], {stdio:['ignore', 'pipe', 'pipe']});
  const capture = chunk => { output = (output + chunk.toString()).slice(-OUTPUT_LIMIT); };
  browser.stdout.on('data', capture);
  browser.stderr.on('data', capture);
  browser.on('error', error => { spawnError = error; });
  let reason;
  try {
    while (performance.now() < deadline) {
      if (spawnError) { reason = `spawn failed: ${spawnError.code || spawnError.message}`; break; }
      if (browser.exitCode !== null || browser.signalCode !== null) { reason = 'browser exited before DevTools was ready'; break; }
      try {
        const contents = await fs.readFile(path.join(profileDir, 'DevToolsActivePort'), 'utf8');
        const portText = contents.split('\n')[0].trim();
        const port = /^\d+$/.test(portText) ? Number(portText) : 0;
        if (!Number.isInteger(port) || port < 1 || port > 65535) {
          lastProbe = 'DevToolsActivePort is incomplete or invalid';
        } else {
          const remaining = Math.max(1, Math.ceil(deadline - performance.now()));
          const response = await fetch(`http://127.0.0.1:${port}/json`, {
            signal: AbortSignal.timeout(Math.min(1000, remaining)), redirect:'error',
          });
          if (!response.ok) {
            lastProbe = `DevTools discovery returned HTTP ${response.status}`;
          } else {
            const page = pageTarget(await response.json(), port);
            if (page && !spawnError && browser.exitCode === null && browser.signalCode === null) {
              return {browser, page};
            }
            lastProbe = 'DevTools has no usable page target yet';
          }
        }
      } catch (error) {
        lastProbe = `${error.code || error.name}: ${error.message}`;
      }
      await pause(Math.max(0, Math.min(50, deadline - performance.now())));
    }
    reason ||= `startup timed out after ${timeoutMs}ms`;
  } catch (error) {
    reason = `startup failed: ${error.message}`;
  }
  // Keep the cause from before cleanup; SIGTERM below is not a startup crash.
  const exitCode = browser.exitCode, signal = browser.signalCode;
  let cleanupError;
  try { await stopBrowser(browser); } catch (error) { cleanupError = error.message; }
  throw new Error(`Browser startup failed: ${reason}\nexecutable: ${executablePath}`
    + `\nexit code: ${exitCode ?? 'none'}; signal: ${signal ?? 'none'}`
    + `\nlast readiness check: ${lastProbe}`
    + `\nbrowser output (tail):\n${output.trim() || '(empty)'}`
    + (cleanupError ? `\ncleanup failed: ${cleanupError}` : ''));
}

module.exports = {launchBrowser, stopBrowser};

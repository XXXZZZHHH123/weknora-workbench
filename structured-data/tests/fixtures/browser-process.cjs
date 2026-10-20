/* A real local process and HTTP endpoint for browser-startup tests; never runs Chrome. */
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');

const mode = process.env.STRUCTURED_BROWSER_FIXTURE_MODE;
const profileArg = process.argv.find(arg => arg.startsWith('--user-data-dir='));
if (!profileArg) throw new Error('Fixture requires a browser profile argument');
const profile = profileArg.slice('--user-data-dir='.length);
fs.mkdirSync(profile, {recursive:true});
fs.writeFileSync(path.join(profile, 'fixture.pid'), String(process.pid));
// A broken cleanup implementation must never leave a fixture process running indefinitely.
setTimeout(() => process.exit(90), 8000).unref();
if (mode === 'early-exit') {
  process.stderr.write('fixture browser startup failed before CDP\n');
  setTimeout(() => process.exit(23), 20);
} else if (mode === 'signal') {
  process.stderr.write('fixture browser received a termination signal\n');
  setTimeout(() => process.kill(process.pid, 'SIGTERM'), 20);
} else if (mode === 'timeout') {
  setInterval(() => {}, 1000);
} else {
  let probes = 0;
  if (mode === 'ignore-term') process.on('SIGTERM', () => {});
  const server = http.createServer((req,res) => {
    if (req.url !== '/json') {res.writeHead(404);res.end();return;}
    probes++;
    const page = {type:'page',webSocketDebuggerUrl:`ws://127.0.0.1:${server.address().port}/devtools/page/fixture`};
    let status = 200, body = [page];
    if (mode === 'late-endpoint') {
      if (probes === 1) {status=503;body={error:'fixture CDP not ready'};}
      else if (probes === 2) body=[];
      else if (probes === 3) body=[{type:'page'}];
      else if (probes === 4) body=[{type:'page',webSocketDebuggerUrl:'not-a-websocket-url'}];
    }
    fs.writeFileSync(path.join(profile, 'fixture.probes'), String(probes));
    res.writeHead(status, {'content-type':'application/json'});
    res.end(JSON.stringify(body));
  });
  server.listen(0, '127.0.0.1', () => {
    const activePort = path.join(profile, 'DevToolsActivePort');
    const content = `${server.address().port}\n/devtools/browser/fixture\n`;
    if (mode === 'delay') setTimeout(() => fs.writeFileSync(activePort, content), 140);
    else if (mode === 'partial') {
      fs.writeFileSync(activePort, '');
      setTimeout(() => fs.writeFileSync(activePort, 'not-a-port\n'), 60);
      setTimeout(() => fs.writeFileSync(activePort, content), 160);
    } else fs.writeFileSync(activePort, content);
  });
}

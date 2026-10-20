/* Exercise startup and cleanup with real child processes and loopback HTTP fixtures. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const {launchBrowser, stopBrowser} = require('./browser-launcher.cjs');

const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
function alive(pid) {
  try {process.kill(pid, 0);return true;} catch (error) {if (error.code === 'ESRCH') return false;throw error;}
}
async function expectStopped(profile) {
  const file=path.join(profile,'fixture.pid');
  assert.ok(fs.existsSync(file), 'the fixture must really have spawned');
  const pid=Number(fs.readFileSync(file,'utf8'));
  const deadline=Date.now()+2000;
  while(alive(pid) && Date.now()<deadline) await pause(20);
  assert.equal(alive(pid),false,'the fixture process must be reaped after failure or stop');
}
async function fixture(mode, run) {
  const root=fs.mkdtempSync(path.join(os.tmpdir(),'browser-launcher-test-'));
  const profile=path.join(root,'profile');
  const executable=path.join(root,'fixture-browser');
  const fixtureSource=path.join(__dirname,'fixtures','browser-process.cjs');
  fs.writeFileSync(executable,`#!${process.execPath}\nrequire(${JSON.stringify(fixtureSource)});\n`,{mode:0o755});
  const previous=process.env.STRUCTURED_BROWSER_FIXTURE_MODE;
  process.env.STRUCTURED_BROWSER_FIXTURE_MODE=mode;
  try {await run({profile,executable});}
  finally {
    if(previous===undefined)delete process.env.STRUCTURED_BROWSER_FIXTURE_MODE;
    else process.env.STRUCTURED_BROWSER_FIXTURE_MODE=previous;
    const pidFile=path.join(profile,'fixture.pid');
    if(fs.existsSync(pidFile)) {
      const pid=Number(fs.readFileSync(pidFile,'utf8'));
      if(alive(pid)) {try {process.kill(pid,'SIGKILL');} catch (error) {if(error.code!=='ESRCH')throw error;}}
      const deadline=Date.now()+2000;
      while(alive(pid) && Date.now()<deadline)await pause(20);
    }
    fs.rmSync(root,{recursive:true,force:true});
  }
}
function launch({profile,executable},timeoutMs=5000) {
  return launchBrowser({executablePath:executable,profileDir:profile,timeoutMs});
}
function expectPage(result) {
  assert.equal(result.page.type,'page');
  assert.ok(['ws:','wss:'].includes(new URL(result.page.webSocketDebuggerUrl).protocol));
  assert.equal(typeof result.browser.pid,'number');
  assert.equal(result.browser.exitCode,null);
}

test('browser startup waits for a delayed DevToolsActivePort file',{timeout:10000},async () => {
  await fixture('delay',async context => {
    const result=await launch(context);
    try {expectPage(result);} finally {await stopBrowser(result.browser);}
    await expectStopped(context.profile);
  });
});

test('browser startup tolerates a partially written port file',{timeout:10000},async () => {
  await fixture('partial',async context => {
    const result=await launch(context);
    try {expectPage(result);} finally {await stopBrowser(result.browser);}
    await expectStopped(context.profile);
  });
});

test('browser startup waits until HTTP and a usable page target are ready',{timeout:10000},async () => {
  await fixture('late-endpoint',async context => {
    const result=await launch(context);
    try {
      expectPage(result);
      assert.ok(Number(fs.readFileSync(path.join(context.profile,'fixture.probes'),'utf8'))>=5);
    } finally {await stopBrowser(result.browser);}
    await expectStopped(context.profile);
  });
});

test('missing browser executable reports its path and spawn error',{timeout:10000},async () => {
  await fixture('ready',async context => {
    const absent=path.join(path.dirname(context.executable),'missing-browser');
    await assert.rejects(launchBrowser({executablePath:absent,profileDir:context.profile,timeoutMs:500}),error => {
      assert.match(error.message,/ENOENT/);
      assert.ok(error.message.includes(absent));
      return true;
    });
    assert.equal(fs.existsSync(path.join(context.profile,'fixture.pid')),false);
  });
});

test('early browser exit reports exit code and captured stderr',{timeout:10000},async () => {
  await fixture('early-exit',async context => {
    await assert.rejects(launch(context),error => {
      assert.match(error.message,/23/);
      assert.match(error.message,/fixture browser startup failed before CDP/);
      assert.ok(error.message.includes(context.executable));
      return true;
    });
    await expectStopped(context.profile);
  });
});

test('browser termination signal is reported instead of a missing port file',{timeout:10000},async () => {
  await fixture('signal',async context => {
    await assert.rejects(launch(context),error => {
      assert.match(error.message,/SIGTERM/);
      assert.match(error.message,/fixture browser received a termination signal/);
      return true;
    });
    await expectStopped(context.profile);
  });
});

test('startup deadline failure terminates and reaps the browser',{timeout:10000},async () => {
  await fixture('timeout',async context => {
    const started=Date.now();
    await assert.rejects(launch(context,1500),error => {
      assert.match(error.message,/timed?\s*out|timeout|deadline/i);
      assert.ok(error.message.includes(context.executable));
      return true;
    });
    assert.ok(Date.now()-started<6000,'a startup failure must have a bounded cleanup');
    await expectStopped(context.profile);
  });
});

test('stop escalates to SIGKILL when a ready browser ignores SIGTERM',{timeout:10000},async () => {
  await fixture('ignore-term',async context => {
    const result=await launch(context);
    expectPage(result);
    const started=Date.now();
    await stopBrowser(result.browser);
    assert.ok(Date.now()-started<6000,'stop must be bounded for a hung browser');
    assert.equal(result.browser.signalCode,'SIGKILL');
    await expectStopped(context.profile);
    await stopBrowser(result.browser);
  });
});

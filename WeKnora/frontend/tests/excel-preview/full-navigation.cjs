/* Real Vue component and browser; a bounded worker fixture isolates UI behavior. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {pathToFileURL} = require('node:url');
const {once} = require('node:events');
const root = path.resolve(__dirname, '../../../..');
const frontend = path.join(root, 'WeKnora/frontend');
const {launchBrowser, stopBrowser} = require(path.join(root, 'structured-data/tests/browser-launcher.cjs'));
const output = process.env.EXCEL_NAVIGATION_ARTIFACTS || path.join(root, '.build/excel-full-navigation-20261023');
fs.mkdirSync(output, {recursive:true});
const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'excel-full-navigation-'));
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
let browser, socket, server;
const errors = [], checks = [];
const fixture = `
import {createApp, h, ref} from 'vue';
import ExcelSourceViewer from '/src/components/source-preview/ExcelSourceViewer.vue';
const names = ['Large', 'Small', ...Array.from({length:135}, (_,i)=>'Notes ' + (i+3))];
const requests = [], workers = [], located = [];
class PreviewWorker {
  terminated = false;
  timers = new Set();
  constructor() { workers.push(this); }
  postMessage(message) {
    requests.push({...message, data:undefined});
    const timer = setTimeout(() => {
      this.timers.delete(timer);
      if (this.terminated) return;
      let value;
      if (message.type === 'open') value = [...names];
      else {
        const totalRows = message.sheet === 'Large' ? 12345 : message.sheet === 'Small' ? 400 : 1;
        const previewRows = message.sheet === 'Small' ? 275 : totalRows;
        const startRow = Math.floor(Math.max(0, Math.min(previewRows-1, message.row || 0))/100)*100;
        const totalColumns = message.sheet === 'Large' ? 64 : 1;
        const startColumn = Math.floor(Math.max(0, Math.min(totalColumns-1, message.column || 0))/30)*30;
        const width = Math.min(30, totalColumns-startColumn);
        value = {sheet:message.sheet,startRow,startColumn,totalRows,previewRows,totalColumns,
          columns:Array.from({length:width},(_,i)=>'C'+(startColumn+i+1)),
          rows:Array.from({length:Math.min(100,previewRows-startRow)},(_,i)=>({number:startRow+i+1,
            cells:Array.from({length:width},(_,column)=>({text:'Row '+(startRow+i+1)+' / C'+(startColumn+column+1),rowSpan:1,colSpan:1,hidden:false}))})),
          truncated:previewRows<totalRows};
      }
      this.onmessage?.({data:{id:message.id,value}});
    }, message.type === 'open' ? 10 : 35);
    this.timers.add(timer);
  }
  terminate() { this.terminated=true; for (const timer of this.timers) clearTimeout(timer); this.timers.clear(); }
}
window.Worker = PreviewWorker;
const blob = ref(new Blob(['synthetic workbook']));
let token = 1;
const makeLocate = (sheet,row) => ({token:++token,quotes:[],locators:[{type:'sheet',mapping:'exact',sheet,row_start:row,row_end:row}]});
const locate = ref(makeLocate('Large',6105));
const app = createApp({setup:()=>()=>h(ExcelSourceViewer,{blob:blob.value,extension:'xlsx',fileName:'Synthetic workbook.xlsx',locate:locate.value,onLocated:event=>located.push(event)})});
window.__excelFixture = {requests,workers,located,
  cite:(sheet,row)=>{locate.value=makeLocate(sheet,row)},
  replaceAndCite:(sheet,row)=>{blob.value=new Blob(['replacement workbook']);locate.value=makeLocate(sheet,row)},
  unmount:()=>app.unmount()};
app.mount('#app');
`;
function browserPath() {
  if (process.env.SHEET_REVIEW_BROWSER) return process.env.SHEET_REVIEW_BROWSER;
  const cache = path.join(os.homedir(), '.cache/ms-playwright');
  for (const folder of fs.readdirSync(cache).filter(name => name.startsWith('chromium-')).sort().reverse()) {
    const file = path.join(cache, folder, 'chrome-linux64/chrome');
    if (fs.existsSync(file)) return file;
  }
  throw Error('Set SHEET_REVIEW_BROWSER to a local Chromium binary.');
}
async function main() {
  try {
    const {createServer} = await import(pathToFileURL(path.join(frontend,'node_modules/vite/dist/node/index.js')).href);
    const {default:vue} = await import(pathToFileURL(path.join(frontend,'node_modules/@vitejs/plugin-vue/dist/index.mjs')).href);
    server = await createServer({configFile:false,root:frontend,cacheDir:path.join(output,'vite-cache'),
      resolve:{alias:{'@':path.join(frontend,'src')}},optimizeDeps:{noDiscovery:true,include:['vue']},
      server:{host:'127.0.0.1',port:0,fs:{allow:[root]}},
      plugins:[vue(),{name:'excel-ui-fixture',
        resolveId(id){if(id==='/excel-preview-fixture.js')return '\0excel-preview-fixture';},
        load(id){if(id==='\0excel-preview-fixture')return fixture;},
        configureServer(vite){vite.middlewares.use(async(req,res,next)=>{
          if(req.url!=='/')return next();
          const html='<!doctype html><html lang="zh-CN"><head><link rel="stylesheet" href="/fonts/workspace-typography.css?v=3"><style>:root{--td-text-color-primary:#292723;--td-text-color-secondary:#6f6b64;--td-component-stroke:#e3e1db;--td-brand-color:#a53e25;--td-bg-color-container:#fff;--td-bg-color-secondarycontainer:#f5f4f0;--td-error-color:#b42318}body{margin:24px;background:#faf9f6;font-family:"Source Serif 4","WeKnora Noto Serif SC",serif}#app{height:850px;border:1px solid var(--td-component-stroke);background:#fff;border-radius:10px;overflow:hidden}.source-locate-block{background:#fff2c5!important}</style></head><body><div id="app"></div><script type="module" src="/excel-preview-fixture.js"></script></body></html>';
          res.setHeader('Content-Type','text/html; charset=utf-8');res.end(await vite.transformIndexHtml('/',html));
        });}}]});
    await server.listen();
    const origin = server.resolvedUrls.local[0];
    const launched = await launchBrowser({executablePath:browserPath(),profileDir:profile});browser=launched.browser;
    socket=new WebSocket(launched.page.webSocketDebuggerUrl);await once(socket,'open');
    let serial=0;const waiting=new Map();
    socket.addEventListener('message',event=>{const message=JSON.parse(event.data);
      if(message.id&&waiting.has(message.id)){waiting.get(message.id)(message);waiting.delete(message.id);}
      else if(message.method==='Runtime.exceptionThrown')errors.push(message.params.exceptionDetails);});
    const call=async(method,params={})=>{const id=++serial,promise=new Promise(resolve=>waiting.set(id,resolve));socket.send(JSON.stringify({id,method,params}));const result=await promise;if(result.error)throw Error(JSON.stringify(result.error));return result.result;};
    const js=async(expression)=>{const result=await call('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(result.exceptionDetails)throw Error(result.exceptionDetails.exception?.description||JSON.stringify(result.exceptionDetails));return result.result.value;};
    const wait=async(expression)=>{for(let i=0;i<200;i++){if(await js(expression))return;await pause(40);}throw Error('Timed out: '+expression);};
    const verify=async(name,expression)=>{assert.equal(await js(expression),true,name);checks.push(name);};
    const jump=async(row,enter=false)=>{
      await js(`(()=>{const input=document.querySelector('[aria-label="跳转到行"]');input.value=${row};input.dispatchEvent(new Event('input',{bubbles:true}));${enter?'input.focus()':'input.closest("form").requestSubmit()'}})()`);
      if(enter){await call('Input.dispatchKeyEvent',{type:'keyDown',key:'Enter',code:'Enter',text:'\r',unmodifiedText:'\r',windowsVirtualKeyCode:13});await call('Input.dispatchKeyEvent',{type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});}
    };
    await call('Runtime.enable');await call('Page.enable');
    await call('Emulation.setDeviceMetricsOverride',{width:1600,height:1000,deviceScaleFactor:1,mobile:false});
    await call('Page.navigate',{url:origin});
    await wait('window.__excelFixture?.located.some(item=>item.found) && document.querySelector(\'tr[data-row="6105"]\')?.classList.contains("source-locate-block") && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    await verify('Initial queued citation above row 5000 is located after metadata loads','document.querySelector(\'tr[data-row="6105"]\').classList.contains("source-locate-block") && window.__excelFixture.requests.filter(item=>item.type==="page").at(-1).row===6104');
    await verify('All metadata-derived sheet names and dynamic total count are available','document.querySelectorAll("select option").length===137 && document.querySelector(".excel-sheet-count").textContent.includes("1 / 137")');
    await verify('Only a 100 by 30 viewport is rendered','document.querySelectorAll("tbody tr").length===100 && document.querySelector("tbody tr").querySelectorAll("td").length===30');
    await jump(6543,true);await wait('document.querySelector(\'tr[data-row="6543"]\') && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    await verify('Enter jumps to an arbitrary row beyond the former limit','document.querySelector(\'[aria-label="跳转到行"]\').value==="6543" && !document.querySelector(\'[aria-label="下一页"]\').disabled');
    await js('document.querySelector(\'[aria-label="下一页"]\').click()');await wait('document.querySelector(\'tr[data-row="6601"]\') && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    checks.push('Next-page navigation continues beyond row 5000');
    await js('document.querySelector(\'[aria-label="后一组列"]\').click()');await wait('document.querySelector("thead th:nth-child(2)").textContent==="C31"');
    await jump(12001);await wait('document.querySelector(\'tr[data-row="12001"]\') && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    await verify('Row jumping preserves the selected column page','document.querySelector("thead th:nth-child(2)").textContent==="C31" && window.__excelFixture.requests.at(-1).column===30');
    await jump(12345);await wait('document.querySelector(\'tr[data-row="12345"]\') && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    await verify('Last partial row page uses the worker total','document.querySelectorAll("tbody tr").length===45 && document.querySelector(\'[aria-label="下一页"]\').disabled && document.querySelector(\'[aria-label="跳转到行"]\').max==="12345"');
    const requestCount=await js('window.__excelFixture.requests.length');await jump(12346);await pause(60);
    assert.equal(await js('window.__excelFixture.requests.length'),requestCount);checks.push('Out-of-range row input never starts a worker request');
    await js('(()=>{const input=document.querySelector(\'[aria-label="筛选工作表"]\');input.value="Notes 137";input.dispatchEvent(new Event("input",{bubbles:true}))})()');await wait('document.querySelectorAll("select option").length===2');
    await verify('Compact local search retains the active sheet and finds the final sheet','[...document.querySelectorAll("select option")].some(item=>item.value==="Large") && [...document.querySelectorAll("select option")].some(item=>item.value==="Notes 137")');
    await js('(()=>{const select=document.querySelector("select");select.value="Notes 137";select.dispatchEvent(new Event("change",{bubbles:true}))})()');await wait('document.querySelector(".excel-sheet-count").textContent.includes("137 / 137") && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    await verify('Filtering never changes the full workbook sheet count','document.querySelector(".excel-sheet-count").textContent.includes("137 / 137") && document.querySelector(\'[aria-label="跳转到行"]\').max==="1"');
    const filteredRequests=await js('window.__excelFixture.requests.length');
    await js('(()=>{const input=document.querySelector(\'[aria-label="筛选工作表"]\');input.value="no matching sheet";input.dispatchEvent(new Event("input",{bubbles:true}))})()');await wait('document.querySelectorAll("select option").length===1 && document.querySelector(".excel-controls [role=status]").textContent.includes("0 个匹配")');
    await verify('An empty sheet filter retains the current sheet and rendered page','document.querySelector("select").value==="Notes 137" && document.querySelector(".excel-grid")!==null && document.querySelector(".excel-sheet-count").textContent.includes("137 / 137")');
    await js('(()=>{const input=document.querySelector(\'[aria-label="筛选工作表"]\');input.value="";input.dispatchEvent(new Event("input",{bubbles:true}))})()');await wait('document.querySelectorAll("select option").length===137');
    assert.equal(await js('window.__excelFixture.requests.length'),filteredRequests);checks.push('Local filtering and clearing cause no worker reads');
    await js('window.__excelFixture.cite("Small",350)');await wait('window.__excelFixture.located.at(-1)?.reason==="preview_limit"');
    await verify('A bounded worker response controls citation limits and the notice','document.querySelector(".excel-note").textContent.includes("275 / 400") && !document.querySelector(".excel-note").textContent.includes("5,000") && document.querySelector(\'[aria-label="跳转到行"]\').max==="275"');
    await js('window.__excelFixture.cite("Small",450)');await wait('window.__excelFixture.located.at(-1)?.reason==="out_of_range"');checks.push('Citation beyond the actual row count is reported as out of range');
    await js('window.__excelFixture.replaceAndCite("Large",9905)');await wait('document.querySelector(\'tr[data-row="9905"]\')?.classList.contains("source-locate-block") && document.querySelector(".excel-viewer").getAttribute("aria-busy")==="false"');
    await verify('Replacing a source releases its worker and retains the queued citation','window.__excelFixture.workers.length===2 && window.__excelFixture.workers[0].terminated && !window.__excelFixture.workers[1].terminated && window.__excelFixture.located.at(-1).found');
    await js('document.fonts.ready.then(()=>true)');
    const screenshot=await call('Page.captureScreenshot',{format:'png'});fs.writeFileSync(path.join(output,'excel-full-navigation.png'),Buffer.from(screenshot.data,'base64'));
    await js('window.__excelFixture.cite("Large",11010)');await pause(5);await js('window.__excelFixture.unmount()');await pause(100);
    await verify('Closing during a read terminates every worker','window.__excelFixture.workers.every(worker=>worker.terminated)');
    assert.deepEqual(errors,[]);checks.push('No browser runtime errors or unhandled promise rejections');
    fs.writeFileSync(path.join(output,'results.json'),JSON.stringify({passed:checks.length,checks,errors},null,2));
    console.log(JSON.stringify({passed:checks.length,checks}));
  } finally {
    socket?.close();await stopBrowser(browser);await server?.close();
    fs.rmSync(profile,{recursive:true,force:true,maxRetries:5,retryDelay:100});
  }
}
main().catch(error=>{console.error(error);process.exitCode=1;});

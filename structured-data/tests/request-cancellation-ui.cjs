const assert=require('node:assert/strict'),test=require('node:test'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const root=path.join(__dirname,'../src/structured_data');
const sheet=fs.readFileSync(path.join(root,'sheet-review.js'),'utf8');
const html=fs.readFileSync(path.join(root,'index.html'),'utf8');
function timedFixture(){
 const context={AbortController,setTimeout,clearTimeout,Error};
 vm.runInNewContext(sheet.slice(sheet.indexOf('  async function timedApi('),sheet.indexOf('  const enabledBlocks')),context);
 return context.timedApi;
}
const waitForAbort=(_,options)=>new Promise((resolve,reject)=>{
 const abort=()=>reject(new DOMException('aborted','AbortError'));
 if(options.signal.aborted)abort();else options.signal.addEventListener('abort',abort,{once:true});
});
test('superseding a sheet request preserves AbortError so the UI can ignore it',async()=>{
 const controller=new AbortController();
 const request=timedFixture()('/sheet',{},controller,1000,waitForAbort);
 controller.abort();
 await assert.rejects(request,error=>error.name==='AbortError');
});
test('an elapsed request deadline remains a visible timeout',async()=>{
 await assert.rejects(timedFixture()('/sheet',{},new AbortController(),5,waitForAbort),error=>error.name==='Error'&&error.message==='请求超时，请重试。');
});
test('HTTP and permission failures remain visible without being relabeled',async()=>{
 const failure=new Error('forbidden');
 await assert.rejects(timedFixture()('/sheet',{},new AbortController(),1000,()=>Promise.reject(failure)),error=>error===failure);
});
test('switching sheets suppresses cancellation without hiding real grid failures',()=>{
 const warning={textContent:''},context={$:()=>warning,note:message=>{warning.textContent=message}};
 const start=sheet.indexOf('  function noteError(');
 vm.runInNewContext(sheet.slice(start,sheet.indexOf('\n',start)),context);
 context.noteError(new DOMException('aborted','AbortError'));assert.equal(warning.textContent,'');
 context.noteError(new Error('grid unavailable'));assert.equal(warning.textContent,'grid unavailable');
});
function refreshFixture(){
 const pending=[],notices=[];
 const context={authEpoch:1,nativeSourceRequest:1,note:message=>notices.push(message),refresh:isCurrent=>new Promise((resolve,reject)=>pending.push({isCurrent,resolve,reject}))};
 vm.runInNewContext(html.slice(html.indexOf('function reportRequestError('),html.indexOf('function progressLabel(')),context);
 vm.runInNewContext(html.slice(html.indexOf('let scopeRefreshVersion='),html.indexOf('async function refresh(')),context);
 return {context,pending,notices};
}
test('a stale refresh cannot display its late failure over a newer refresh',async()=>{
 const {context,pending,notices}=refreshFixture();
 const first=context.refreshCurrentScope(),second=context.refreshCurrentScope();
 assert.equal(pending[0].isCurrent(),false);assert.equal(pending[1].isCurrent(),true);
 pending[0].reject(new Error('old error'));pending[1].resolve();
 await Promise.all([first,second]);assert.deepEqual(notices,['','']);
});
test('active cancellation stays silent while a current failure is reported',async()=>{
 const {context,pending,notices}=refreshFixture();
 const first=context.refreshCurrentScope();pending[0].reject(new DOMException('aborted','AbortError'));await first;
 assert.deepEqual(notices,['']);
 const second=context.refreshCurrentScope();pending[1].reject(new Error('server unavailable'));await second;
 assert.equal(notices.at(-1),'server unavailable');
});

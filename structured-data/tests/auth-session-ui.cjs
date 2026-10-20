const assert=require('node:assert/strict'),test=require('node:test'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(require('node:path').join(__dirname,'../src/structured_data/index.html'),'utf8');
function fixture(fetcher){
 const messages=[],calls=[];let context;
 context={authEpoch:1,bearer:'old',embedded:true,base:'/structured',location:{origin:'https://local'},setTimeout,clearTimeout,Error,
  authHeaders:()=>({Authorization:'Bearer '+context.bearer}),
  parent:{postMessage:message=>{messages.push(message);queueMicrotask(()=>context.resolveAuth())}},
  fetch:async (path,options)=>{calls.push(options);return fetcher(context,calls.length)},
 };
 vm.runInNewContext(source.slice(source.indexOf('let authRefreshPending='),source.indexOf('async function api('))+`
 this.authFetch=authenticatedFetch;
 this.resolveAuth=()=>{clearTimeout(authRefreshPending.timer);bearer='new';authRefreshPending.resolve();authRefreshPending=null};`,context);
 return {context,messages,calls};
}
test('a late 401 asks the host about the credential actually rejected, even after bearer rotation',async()=>{
 const {context,messages,calls}=fixture((ctx,n)=>{if(n===1){ctx.bearer='new';return {status:401}}return {status:200}});
 assert.equal((await context.authFetch('/api/datasets')).status,200);
 assert.equal(messages.length,1);assert.equal(messages[0].token,'old');
 assert.deepEqual(calls.map(call=>call.headers.Authorization),['Bearer old','Bearer new']);
});
test('a stale scope cannot refresh the next account after its 401 arrives',async()=>{
 const {context,messages}=fixture(ctx=>{ctx.authEpoch++;return {status:401}});
 await assert.rejects(context.authFetch('/api/datasets'),/已切换/);assert.equal(messages.length,0);
});
test('simultaneous structured 401s share one host exchange and replay once each',async()=>{
 const {context,messages,calls}=fixture((_,n)=>({status:n<=2?401:200}));
 const responses=await Promise.all([context.authFetch('/api/datasets'),context.authFetch('/api/imports')]);
 assert.ok(responses.every(response=>response.status===200));assert.equal(messages.length,1);assert.equal(calls.length,4);
});
test('structured permission denial never requests refresh',async()=>{
 const {context,messages}=fixture(()=>({status:403}));assert.equal((await context.authFetch('/api/datasets')).status,403);assert.equal(messages.length,0);
});

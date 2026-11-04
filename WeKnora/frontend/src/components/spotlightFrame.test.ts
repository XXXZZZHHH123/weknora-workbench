import assert from 'node:assert/strict'
import test from 'node:test'
import { followUntilStable, sameBox, spotlightDocument, spotlightRect } from './spotlightFrame'
const rect=(left:number,top:number,width:number,height:number)=>({left,top,width,height,right:left+width,bottom:top+height,x:left,y:top}) as DOMRect

test('ordinary guides keep their original document and geometry',()=>{
 const document={} as Document, r=rect(12,20,100,30),element={ownerDocument:document,getBoundingClientRect:()=>r} as HTMLElement
 assert.equal(spotlightDocument(undefined,document),document);assert.equal(spotlightRect(element),r)
})
test('embedded controls use parent coordinates including frame scale',()=>{
 const document={} as Document, element={ownerDocument:document,getBoundingClientRect:()=>rect(20,30,100,40)} as HTMLElement
 const frame={contentDocument:document,clientWidth:800,clientHeight:600,getBoundingClientRect:()=>rect(100,80,1000,750)} as HTMLIFrameElement
 const r=spotlightRect(element,frame)!
 assert.deepEqual([r.left,r.top,r.width,r.height],[125,117.5,125,50])
})
test('highlighting clips to the actual frame and rejects foreign or inaccessible documents',()=>{
 const document={} as Document,element={ownerDocument:document,getBoundingClientRect:()=>rect(-20,-10,100,40)} as HTMLElement
 const frame={contentDocument:document,clientWidth:800,clientHeight:600,getBoundingClientRect:()=>rect(100,80,800,600)} as HTMLIFrameElement
 const r=spotlightRect(element,frame)!
 assert.deepEqual([r.left,r.top,r.width,r.height],[100,80,80,30])
 assert.equal(spotlightRect(element,{...frame,contentDocument:{}} as any),null)
 assert.equal(spotlightDocument({get contentDocument(){throw Error('SecurityError')}} as unknown as HTMLIFrameElement,document),null)
 assert.equal(spotlightRect(element,{...frame,clientWidth:0} as HTMLIFrameElement),null)
})

function frames(){const queue:(()=>void)[]=[];return{queue,schedule:(cb:()=>void)=>queue.push(cb),cancel:(handle:number)=>{queue[handle-1]=()=>{}},
 flush(limit=500){let ran=0;while(queue.length&&ran<limit){queue.shift()!();ran++}return ran}}}
test('a sliding control is followed to where it comes to rest, then measuring stops',()=>{
 const path=[rect(500,40,600,280),rect(380,40,600,280),rect(220,40,600,280),rect(120,40,600,280),rect(100,40,600,280)]
 let step=0;const applied:DOMRect[]=[],clock=frames()
 followUntilStable(path[0]!,()=>path[Math.min(++step,path.length-1)],box=>applied.push(box!),clock.schedule,clock.cancel,{stableFrames:3})
 clock.flush()
 assert.deepEqual(applied.map(box=>box!.left),[380,220,120,100]);assert.equal(clock.queue.length,0)
 assert.equal(step,path.length+2,'stops after three unchanged frames instead of polling forever')
})
test('following ends when the control disappears, is cancelled, or never settles',()=>{
 const clock=frames();let calls=0
 followUntilStable(rect(0,0,10,10),()=>{calls++;return undefined},()=>assert.fail('nothing to apply'),clock.schedule,clock.cancel)
 clock.flush();assert.equal(calls,1)
 const moving=frames();let x=0;const applied:number[]=[]
 followUntilStable(rect(0,0,10,10),()=>rect(++x,0,10,10),box=>applied.push(box!.left),moving.schedule,moving.cancel,{maxFrames:20})
 moving.flush();assert.equal(applied.length,20)
 const held=frames();const stop=followUntilStable(rect(0,0,10,10),()=>rect(5,0,10,10),()=>assert.fail('cancelled'),held.schedule,held.cancel)
 stop();held.flush()
 assert.equal(sameBox(null,null),true);assert.equal(sameBox(rect(0,0,10,10),null),false);assert.equal(sameBox(rect(0,0,10,10),rect(0.2,0,10,10)),true)
})

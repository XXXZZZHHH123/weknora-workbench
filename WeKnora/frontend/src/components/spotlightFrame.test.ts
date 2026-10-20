import assert from 'node:assert/strict'
import test from 'node:test'
import { spotlightDocument, spotlightRect } from './spotlightFrame'
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
 assert.equal(spotlightDocument({get contentDocument(){throw Error('SecurityError')}} as HTMLIFrameElement,document),null)
 assert.equal(spotlightRect(element,{...frame,clientWidth:0} as HTMLIFrameElement),null)
})

const assert=require('node:assert/strict');
const {generateReview,reviewControl,descendants}=require('./judgment-manager-ui.cjs');
(async()=>{
  for(const mode of ['catalog','declared']){
    const env=await generateReview({caliberMode:mode});
    assert.equal(reviewControl(env,'anchor','caliber-catalog'),undefined,'A selected indicator does not require a second catalog-unit confirmation');
    assert.equal(reviewControl(env,'anchor','caliber-unit'),undefined,'Users are never forced to type an indicator unit');
    assert.equal(reviewControl(env,'anchor','caliber-basis'),undefined);
    const caption=descendants(env.nodes['logic-review-items']).find(node=>node.className==='logic-binding-meta');
    assert.equal(caption.textContent,mode==='catalog'?'采用指标口径 · kg · 频率未注明':'采用指标口径 · 来源未注明单位 · 频率未注明');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/review/update')).length,0,'Opening the preview does not forge a confirmation');
    const search=reviewControl(env,'anchor','search');await search.fire('focus');await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'ArrowDown'});await search.fire('keydown',{key:'Enter'});
    const request=env.requests.find(row=>row.url.endsWith('/review/update'));
    assert.equal(request.body.action,'confirm');assert.equal(request.body.series_id,'authorized-other');
    assert.equal(Object.hasOwn(request.body,'caliber'),false,'Binding uses the actual selected source without a manual unit override');
    assert.equal(descendants(env.nodes['logic-review-items']).find(node=>node.className==='logic-binding-meta').textContent,'采用指标口径 · t · 频率未注明');
    env.context.window.JudgmentManager.close();
  }
  console.log('Selected-source caliber is automatic; unknown source units stay explicit without manual forms');
})().catch(error=>{console.error(error);process.exitCode=1;});

const assert = require('node:assert/strict');
const {generateReview, reviewControl, descendants, flush} = require('./judgment-manager-ui.cjs');
const options = env => descendants(env.nodes['logic-review-items']).filter(node => node.className === 'logic-binding-option');
(async () => {
  const env = await generateReview();
  const search = reviewControl(env, 'anchor', 'search');
  await search.fire('focus');
  assert.equal(options(env)[0].textContent, '暂无，暂不关联');
  await reviewControl(env, 'anchor', 'more').fire('click');
  assert.equal(options(env)[0].textContent, '暂无，暂不关联');
  assert.equal(options(env).length, 3);
  search.value = 'quantity'; await search.fire('input');
  assert.equal(options(env)[0].textContent, '暂无，暂不关联', 'The empty choice remains available during search');
  await search.fire('keydown', {key: 'ArrowDown'}); await search.fire('keydown', {key: 'Enter'}); await flush();
  const update = env.requests.filter(row => row.url.endsWith('/review/update')).at(-1);
  assert.equal(update.body.series_id, null);
  assert.equal(update.body.action, 'keep_text');
  assert.equal(env.confirmations.length, 0);
  env.context.window.JudgmentManager.close();
  for(const configuration of [{catalogFailure:true},{catalogDeferred:true}]) {
    const waiting=await generateReview(configuration),picker=reviewControl(waiting,'anchor','search');
    if(configuration.catalogDeferred) {picker.value='slow';await picker.fire('input');await new Promise(resolve=>setTimeout(resolve,230));}
    else await picker.fire('focus');
    assert.equal(options(waiting)[0].textContent,'暂无，暂不关联','The empty choice stays available while loading or after a failed search');
    await options(waiting)[0].fire('click');await flush();
    assert.equal(waiting.requests.filter(row=>row.url.endsWith('/review/update')).at(-1).body.series_id,null);
    if(configuration.catalogDeferred)waiting.catalogReplies.forEach(resolve=>resolve());
    waiting.context.window.JudgmentManager.close();
  }
  console.log('Empty binding stays first across search, pagination and keyboard selection');
})().catch(error => { console.error(error); process.exitCode = 1; });

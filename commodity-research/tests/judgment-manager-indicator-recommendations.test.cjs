const assert = require('node:assert/strict');
const {generateReview, setup, reviewControl, descendants, flush} = require('./judgment-manager-ui.cjs');

const identifier = 'synthetic-indicator-job-01';
const purpose = 'judgment_indicator_recommendations';
const recommendations = [{series_id: 'authorized-page-two', name: 'Matched quantity', frequency: '周',
  source_file: 'Synthetic workbook.xlsx', sheet: 'Data', reason: '原文名称与目录字段一致'}];
const resultResponse = payload => ({ok: true, json: async () => payload});
let jobState = 'running', candidates = recommendations;

function requestOverride(url, options, {preview, review}) {
  const job = {id: identifier, purpose, workbench_id: 'synthetic', state: jobState,
    phase_label: '正在挑选指标', context: {draft_id: review.draft_id, item_id: 'anchor'}};
  if (url.endsWith('/indicator-recommendations')) return resultResponse({...job, state: 'queued'});
  if (url.endsWith('/analysis-jobs/' + identifier)) return resultResponse(job);
  if (url.endsWith('/analysis-jobs/' + identifier + '/result')) return resultResponse({job,
    draft_id: review.draft_id, item_id: 'anchor', recommendations: candidates});
  if (url.includes('/analysis-jobs/latest?')) return resultResponse({job: null});
  if (url.includes('/review/preview?')) return resultResponse({...preview(), source_text: 'Original source', model_id: 'configured'});
}

function recommendationGroup(env) {
  const search = reviewControl(env, 'anchor', 'search');
  const list = descendants(env.nodes['logic-review-items']).find(node => node.id === search.attributes['aria-controls']);
  const group = list.children[0];
  assert.equal(group.dataset.recommendationGroup, 'true');
  assert.equal(group.attributes['aria-label'], '模型推荐');
  assert.match(group.children[1].textContent, /Matched quantity.*原文名称与目录字段一致/);
  return group;
}

(async () => {
  const env = await generateReview({reviewJob: true, requestOverride});
  const search = reviewControl(env, 'anchor', 'search');
  search.value = 'Researcher search hint';
  await search.fire('input');
  const recommendation = reviewControl(env, 'anchor', 'recommend-indicator');
  assert.equal(recommendation.textContent, '推荐指标');
  await recommendation.fire('click');
  const submitted = env.requests.find(request => request.url.endsWith('/indicator-recommendations'));
  assert.deepEqual(submitted.body, {draft_id: 'server-bound-nonce', item_id: 'anchor', model_id: 'configured',
    search: 'Researcher search hint'});
  assert.equal(env.nodes['logic-review-indicators-status'].dataset.analysisBusy, 'true');

  env.context.window.JudgmentManager.close();
  jobState = 'succeeded';
  await env.context.window.JudgmentManager.open();
  await flush();
  const group = recommendationGroup(env);
  assert.equal(env.requests.filter(request => request.url.endsWith('/review/update')).length, 0);
  await group.children[1].fire('click');
  const update = env.requests.find(request => request.url.endsWith('/review/update'));
  assert.equal(update.body.series_id, recommendations[0].series_id);
  env.context.window.JudgmentManager.close();

  const recovered = setup({reviewMode: true, reviewJob: true, requestOverride});
  await recovered.context.window.JudgmentManager.openJob(identifier);
  recommendationGroup(recovered);
  assert.equal(recovered.nodes['logic-input'].value, 'Original source');
  recovered.context.window.JudgmentManager.close();

  candidates = [];
  const empty = setup({reviewMode: true, reviewJob: true, requestOverride});
  await empty.context.window.JudgmentManager.openJob(identifier);
  assert.match(empty.nodes['logic-review-indicators-status'].textContent, /没有找到一致的指标/);
  empty.context.window.JudgmentManager.close();
  console.log('indicator recommendation grouping, explicit selection and background recovery passed');
})().catch(error => {console.error(error); process.exitCode = 1;});

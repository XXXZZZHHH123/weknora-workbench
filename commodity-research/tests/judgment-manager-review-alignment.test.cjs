const assert = require('node:assert/strict');
const {generateReview, reviewControl, descendants} = require('./judgment-manager-ui.cjs');

const response = value => ({ok: true, json: async () => value});
const item = (env, name) => reviewControl(env, 'anchor', name);

async function staleDimensions(reclassify) {
  const reason = '维度「供给」已不在本研究台的研究维度中，请改选或设为未标注';
  const job = {id: 'synthetic-draft-reclassify-1', purpose: 'judgment_review_dimensions', workbench_id: 'synthetic', state: 'succeeded'};
  function complete({preview, review}) {
    const row = review.sections[0].items[0];
    Object.assign(row, {dimension_issue: null, dimension: reclassify ? 'd-new' : null, status: 'ready', warnings: []});
    review.stale_dimension_count = 0;
    review.revision++;
    return response(preview());
  }
  const env = await generateReview({reviewJob: true,
    reviewSections: [{id: 'bull', title: '多方依据', items: [{id: 'anchor', title: '供给变化', description: '产量核对', kind: 'binding',
      status: 'pending', can_dimension: true, dimension: 'supply', dimension_issue: reason, warnings: ['其他提示']}]}],
    requestOverride(url, options, state) {
      if (url.endsWith('/jobs/synthetic-analysis-job-0001/result')) {
        state.review.stale_dimension_count = 1;
        state.review.research_dimensions = [{id: 'd-new', label: '产量'}];
      }
      if (url.endsWith('/review/dimensions/clear')) return complete(state);
      if (url.endsWith('/review/dimensions/jobs')) return response({...job, state: 'queued'});
      if (url.endsWith('/analysis-jobs/' + job.id)) return response(job);
      if (url.endsWith('/analysis-jobs/' + job.id + '/result')) return response({job, draft_id: state.review.draft_id, revision: 1});
      if (url.endsWith('/review/dimensions/jobs/' + job.id + '/apply')) return complete(state);
    }});
  assert.match(env.nodes['logic-review-items'].textContent, /维度「供给」已不在/);
  assert.deepEqual(item(env, 'dimension').children.map(option => option.value), ['', 'd-new']);
  assert.equal(env.nodes['logic-review-reclassify'].textContent, '按当前研究维度重新归类（1 条）');
  const button = env.nodes[reclassify ? 'logic-review-reclassify' : 'logic-review-clear-dimensions'];
  assert.equal(button.disabled, false);
  await button.fire('click');
  assert.equal(env.nodes['logic-review-dimensions'].hidden, true);
  assert.equal(item(env, 'dimension').value, reclassify ? 'd-new' : '');
  const request = env.requests.find(row => row.url.endsWith(reclassify ? '/review/dimensions/jobs' : '/review/dimensions/clear'));
  assert.equal(request.body.revision, 1);
  if (reclassify) assert.ok(request.headers['Idempotency-Key']);
  env.context.window.JudgmentManager.close();
}

async function quantityAlignment() {
  const latest = {date: '2026-11-18', value: '18035.0'}, range = {min: '15123', max: '18908'};
  const warning = '规则数值 158–163 与该指标近 3 年 15,123–18,908 相差约 100 倍，请核对原文写法或单位';
  const binding = {series_id: 'authorized-other', name: '指标', unit: null, frequency: '日'};
  const env = await generateReview({reviewSections: [{id: 'thresholds', title: '阈值', items: [{id: 'anchor', kind: 'thresholds',
    title: '短期区间', status: 'ready', can_bind: true, binding, caliber: {catalog_unit: null, model_unit: '百元'},
    rule: {operator: 'between', value_literal: '[158,163]', source_value: ['158.0', '163.0'], quantity_review_available: true}}]}],
    requestOverride(url) {
      if (!url.includes('/review/quantity?')) return;
      const exponent = new URL(url, 'http://unit.test').searchParams.get('exponent');
      return response({series_id: binding.series_id, latest, range, warning,
        ...(exponent === '2' ? {converted_text: '15,800–16,300', converted_overlaps: true} : {})});
    }});
  assert.equal(item(env, 'confirm-item').hidden, false);
  await item(env, 'confirm-item').fire('click');
  const modal = descendants(env.nodes['judgment-manager']).find(node => node.open);
  const control = name => descendants(modal).find(node => node.dataset.reviewControl === name);
  assert.ok(modal.textContent.includes(warning));
  assert.match(modal.textContent, /原文数字写法：158–163.*最新值 18,035（2026-11-18）/);
  assert.match(modal.textContent, /指标数据的单位（看数据：最新 18,035）/);
  assert.equal(control('confirm-unit').value, '');
  control('confirm-unit').value = '元/吨';
  const mode = control('confirm-quantity-mode'), exponent = control('confirm-quantity-exponent');
  mode.value = 'keep'; await mode.fire('change');
  await control('confirm-submit').fire('click');
  assert.match(modal.textContent, /请填写仍按原值的原因/);
  assert.equal(env.requests.filter(row => row.url.endsWith('/review/update')).length, 0);
  mode.value = 'convert'; await mode.fire('change');
  exponent.value = '2'; await exponent.fire('change');
  assert.match(modal.textContent, /15,800–16,300 · 与近 3 年数据范围有交集/);
  await control('confirm-submit').fire('click');
  const update = env.requests.find(row => row.url.endsWith('/review/update'));
  assert.deepEqual(update.body.quantity_alignment, {mode: 'convert', exponent: 2});
  assert.equal(update.body.caliber.unit, '元/吨');
  env.context.window.JudgmentManager.close();
}

(async () => {
  await staleDimensions(false);
  await staleDimensions(true);
  await quantityAlignment();
})().catch(error => { console.error(error); process.exitCode = 1; });

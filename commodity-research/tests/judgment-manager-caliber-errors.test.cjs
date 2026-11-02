const assert = require('node:assert/strict');
const {setup, descendants, reviewControl} = require('./judgment-manager-ui.cjs');

async function prepare(attentionFields) {
  const env = setup({reviewMode: true, caliberMode: 'declared'});
  const transport = env.context.researchFetch;
  env.context.researchFetch = async (url, options = {}) => {
    if (url.endsWith('/review/update')) {
      const body = JSON.parse(options.body);
      env.requests.push({url, body, method: options.method});
      return {ok: false, status: 422, json: async () => ({detail: {
        message: 'The comparison value is not supported by the selected original text.',
        attention_fields: attentionFields
      }})};
    }
    const response = await transport(url, options);
    if (/\/jobs\/[^/]+\/result$/.test(url)) {
      const value = await response.json();
      const item = value.review.sections[0].items[0];
      item.kind = 'falsifiers';
      item.rule = {expression: 'value >= 12000', side: 'bull', consecutive_periods: 1};
      item.attention_fields = [];
      return {...response, json: async () => value};
    }
    return response;
  };
  await env.context.window.JudgmentManager.open();
  env.nodes['logic-input'].value = 'Synthetic original quantity source.';
  env.nodes['logic-model'].value = 'configured';
  await env.nodes['logic-generate'].fire('click');
  for (const [control, value] of [['caliber-unit', '吨'], ['caliber-basis', 'Checked the original quantity table.']]) {
    const input = reviewControl(env, 'anchor', control);
    input.value = value;
    await input.fire('input');
  }
  return env;
}

(async () => {
  for (const field of ['condition', 'source_quote', 'caliber', undefined]) {
    const env = await prepare(field ? [field] : undefined);
    await reviewControl(env, 'anchor', 'caliber-declare').fire('click');
    const card = descendants(env.nodes['logic-review-items']).find(node =>
      node.className?.includes('logic-review-card') && node.dataset.reviewItem === 'anchor');
    const target = descendants(card).find(node => node.dataset.reviewField === (field || 'caliber'));
    const caliber = descendants(card).find(node => node.dataset.reviewField === 'caliber');
    assert.equal(target.dataset.invalid, 'true');
    assert.equal(caliber.dataset.invalid, field === 'condition' || field === 'source_quote' ? 'false' : 'true',
      'A separate comparison/source error does not invalidate the entered unit; explicit or fallback caliber errors remain marked');
    assert.equal(reviewControl(env, 'anchor', 'caliber-unit').value, '吨', 'The unit input remains available after a rule error');
    assert.equal(env.requests.filter(row => row.url.endsWith('/review/update')).length, 1);
    env.context.window.JudgmentManager.close();
  }
  console.log('4 caliber versus condition/source error scenarios passed');
})().catch(error => { console.error(error); process.exitCode = 1; });

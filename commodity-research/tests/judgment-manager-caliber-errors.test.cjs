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
  return env;
}

(async () => {
  for (const field of ['condition', 'source_quote', 'caliber', undefined]) {
    const env = await prepare(field ? [field] : undefined);
    const picker=reviewControl(env,'anchor','search');await picker.fire('focus');await picker.fire('keydown',{key:'ArrowDown'});await picker.fire('keydown',{key:'ArrowDown'});await picker.fire('keydown',{key:'Enter'});
    const card = descendants(env.nodes['logic-review-items']).find(node =>
      node.className?.includes('logic-review-card') && node.dataset.reviewItem === 'anchor');
    const targetField=field==='caliber'||!field?'binding':field;
    const target=descendants(card).find(node=>node.dataset.reviewField===targetField);
    assert.equal(target.dataset.invalid,'true','A source selection never hides a numerical, evidence or legacy unit error');
    assert.match(target.textContent,/comparison value is not supported/);
    assert.match(reviewControl(env,'anchor','condition').textContent,/12000/,'A rejected comparison retains the exact threshold number');
    assert.equal(reviewControl(env,'anchor','caliber-unit'),undefined,'Source units are not replaced by manual input');
    assert.equal(env.nodes['logic-apply'].disabled,true,'A rejected numerical rule cannot be saved as synchronized');
    const update=env.requests.filter(row=>row.url.endsWith('/review/update'));assert.equal(update.length,1);
    assert.equal(update[0].body.series_id,'authorized-other');assert.equal(Object.hasOwn(update[0].body,'caliber'),false);
    env.context.window.JudgmentManager.close();
  }
  console.log('4 caliber versus condition/source error scenarios passed');
})().catch(error => { console.error(error); process.exitCode = 1; });

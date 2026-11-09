const assert = require('node:assert/strict');
const {test} = require('node:test');
const {generateReview, reviewControl, descendants} = require('./judgment-manager-ui.cjs');
const selected = frequency => ({series_id:'synthetic-frequency',name:'合成观测',unit:'测试单位',frequency});
const item = (binding, overrides = {}) => ({id:'frequency-rule',kind:'falsifiers',title:'合成证伪条件',
  description:'合成原文中的周期不能覆盖目录频率。',status:'ready',can_bind:true,can_confirm:true,
  binding,rule:{expression:'change > 0',side:'bull',consecutive_periods:2},...overrides});
const create = (row, options = {}) => generateReview({reviewSections:[{id:'rules',title:'核对条件',items:[row]}],...options});
const nodes = env => descendants(env.nodes['logic-review-items']);
const note = env => nodes(env).find(node => node.className === 'logic-period-note');
const caption = env => nodes(env).find(node => node.className === 'logic-binding-meta');

test('目录频率原样展示，连续期数只说明观测数量', async () => {
  for (const frequency of ['日','周','月','不定期（目录原文）','<img src=x onerror=alert(1)>']) {
    const env = await create(item(selected(frequency)));
    try {
      assert.equal(caption(env).textContent, '采用指标口径 · 测试单位 · ' + frequency);
      assert.equal(note(env).textContent, `连续 2 期 = 2 个观测（目录频率：${frequency}）`);
      assert.equal(note(env).hidden, false);
      assert.match(reviewControl(env,'frequency-rule','condition').dataset.conditionText, /连续 2 期成立/);
      assert.equal(env.requests.filter(row => row.url.endsWith('/review/update')).length, 0);
    } finally { env.context.window.JudgmentManager.close(); }
  }
});

test('旧快照及未注明频率保留核对提示，不采用人工频率或原文猜测', async () => {
  for (const frequency of [undefined,null,'','—','未注明','未确认','   ']) {
    const binding = selected(frequency);
    if (frequency === undefined) delete binding.frequency;
    const env = await create(item(binding,{caliber:{frequency:'周',model_frequency:'月'}}));
    try {
      assert.equal(caption(env).textContent, '采用指标口径 · 测试单位 · 频率未注明');
      assert.equal(note(env).textContent, '频率未注明，请核对连续期数的口径');
    } finally { env.context.window.JudgmentManager.close(); }
  }
});

test('普通阈值不附加连续期数说明，也不从文字提取连续期数', async () => {
  const env = await create(item(selected('周'),{kind:'thresholds',description:'连续 9 期仅为合成原文。',
    rule:{operator:'>',value_literal:'12'}}));
  try {
    assert.equal(caption(env).textContent, '采用指标口径 · 测试单位 · 周');
    assert.equal(note(env).hidden, true);
    assert.equal(note(env).textContent, '');
  } finally { env.context.window.JudgmentManager.close(); }
});

test('候选包含原始频率或缺省提示，选择新指标后更新说明且不改规则', async () => {
  const catalog = [selected('周'),{series_id:'synthetic-legacy',name:'合成旧目录指标',unit:'测试单位'}];
  const env = await create(item({...selected('日'),series_id:'synthetic-original'}),{catalogEntries:catalog});
  try {
    let search = reviewControl(env,'frequency-rule','search');
    await search.fire('focus');
    let options = nodes(env).filter(node => node.className === 'logic-binding-option');
    assert.equal(options[1].children[0].textContent, '合成观测 · 周');
    await reviewControl(env,'frequency-rule','more').fire('click');
    options = nodes(env).filter(node => node.className === 'logic-binding-option');
    assert.equal(options[2].children[0].textContent, '合成旧目录指标 · 频率未注明');
    await options[1].fire('click');
    assert.equal(caption(env).textContent, '采用指标口径 · 测试单位 · 周');
    assert.equal(note(env).textContent, '连续 2 期 = 2 个观测（目录频率：周）');
    search = reviewControl(env,'frequency-rule','search');
    await search.fire('focus'); await reviewControl(env,'frequency-rule','more').fire('click');
    options = nodes(env).filter(node => node.className === 'logic-binding-option');
    await options[2].fire('click');
    assert.equal(note(env).textContent, '频率未注明，请核对连续期数的口径');
    const updates = env.requests.filter(row => row.url.endsWith('/review/update'));
    assert.equal(updates.length, 2);
    for (const update of updates) {
      assert.equal(Object.hasOwn(update.body,'rule'), false);
      assert.equal(Object.hasOwn(update.body,'consecutive_periods'), false);
      assert.equal(Object.hasOwn(update.body,'caliber'), false);
    }
    assert.match(reviewControl(env,'frequency-rule','condition').dataset.conditionText, /连续 2 期成立/);
  } finally { env.context.window.JudgmentManager.close(); }
});

test('整条确认窗口同样展示连续期数的目录频率说明', async () => {
  const env = await create(item({...selected('日'),unit:null}));
  try {
    await reviewControl(env,'frequency-rule','confirm-item').fire('click');
    const modal = descendants(env.nodes['judgment-manager']).find(node => node.className === 'logic-confirm-dialog');
    assert.ok(modal?.open);
    assert.match(modal.textContent, /连续 2 期 = 2 个观测（目录频率：日）/);
    assert.equal(env.requests.filter(row => row.url.endsWith('/review/update')).length, 0);
  } finally { env.context.window.JudgmentManager.close(); }
});

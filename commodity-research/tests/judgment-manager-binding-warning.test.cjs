const assert = require('node:assert/strict');
const {generateReview, reviewControl, descendants, flush} = require('./judgment-manager-ui.cjs');

(async () => {
  for (const accepted of [true, false]) {
    const env = await generateReview({bindingWarning: true, confirmDelete: accepted});
    const search = reviewControl(env, 'anchor', 'search');
    await search.fire('focus');
    await search.fire('keydown', {key: 'ArrowDown'});
    await search.fire('keydown', {key: 'Enter'});
    await flush();
    const updates = env.requests.filter(row => row.url.endsWith('/review/update'));
    assert.equal(env.confirmations.length, 1);
    assert.match(env.confirmations[0], /原文口径尚未核对.*不自动换算/);
    assert.equal(updates.length, accepted ? 2 : 1);
    assert.equal(updates[0].body.binding_warning_token, undefined);
    if (accepted) {
      assert.equal(updates[1].body.binding_warning_token, 'synthetic-warning-token');
      assert.equal(updates[1].body.revision, updates[0].body.revision);
      assert.equal(reviewControl(env, 'anchor', 'search').value, 'Directory quantity <script>');
      const remember = reviewControl(env, 'anchor', 'remember'); remember.checked = true; await remember.fire('change');
      assert.equal(env.confirmations.length, 1, 'An unchanged confirmed binding does not prompt again');
    } else {
      assert.equal(reviewControl(env, 'anchor', 'search').value, '', 'Cancel restores the persisted selection');
      assert.match(env.nodes['logic-status'].textContent, /已取消/);
    }
    env.context.window.JudgmentManager.close();
  }
  {
    const env = await generateReview({bindingWarning: true});
    const search = reviewControl(env, 'anchor', 'search'); await search.fire('focus');
    const empty = descendants(env.nodes['logic-review-items']).find(node => node.className === 'logic-binding-option' && node.textContent === '暂无，暂不关联');
    await empty.fire('click'); await flush();
    assert.equal(env.confirmations.length, 0, 'Choosing no indicator does not prompt');
    const request = env.requests.find(row => row.url.endsWith('/review/update'));
    assert.equal(request.body.series_id, null);
    env.context.window.JudgmentManager.close();
  }
  {
    const env = await generateReview({analysisReused: true});
    assert.equal(env.nodes['logic-status'].textContent, '已复用相同原文的解析结果。');
    env.context.window.JudgmentManager.close();
  }
  {
    const notice = '部分原文尚未完成核对，已保留可用结果。';
    const env = await generateReview({analysisComplete: false, previewNotice: notice, readyMode: 'all'});
    assert.equal(env.nodes['logic-status'].textContent, notice, 'Partial background results do not claim analysis finished');
    assert.match(env.nodes['logic-review-save-note'].textContent, /部分原文尚未完成核对/, 'A zero-pending partial result still does not claim all content was parsed');
    env.context.window.JudgmentManager.close();
  }
  {
    const env = await generateReview({readyMode: true, boundIndicators: [{series_id: 'synthetic-a'}, {series_id: 'synthetic-b'}, {series_id: 'synthetic-a'}]});
    const count = '已解析 4 项 · 已关联 2 项指标 · 需处理 1 项';
    assert.equal(env.nodes['logic-review-count'].textContent, count);
    const requests = env.requests.length;
    await env.nodes['logic-review-attention'].fire('click');
    assert.equal(env.nodes['logic-review-count'].textContent, count, 'The binding count covers the entire preview, even in the attention filter');
    await env.nodes['logic-review-all'].fire('click');
    assert.equal(env.nodes['logic-review-count'].textContent, count);
    assert.equal(env.requests.length, requests, 'Showing statistics does not preload indicator directories or observations');
    env.context.window.JudgmentManager.close();
  }
  console.log('6 binding warning/replay/visibility interaction scenarios passed');
})().catch(error => { console.error(error); process.exitCode = 1; });

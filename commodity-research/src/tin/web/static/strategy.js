(() => {
  const notice = document.getElementById('strategy-notice');
  const show = (message, error = false) => {
    if (!notice) return;
    notice.textContent = message;
    notice.className = `strategy-notice visible ${error ? 'error' : ''}`;
  };
  const request = async (url, body = {}) => {
    const response = await researchFetch(url, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    const data = await response.json();
    if (!response.ok) throw new Error(data.detail || '操作失败');
    return data;
  };

  const generator = document.getElementById('generate-strategies');
  const model = document.getElementById('strategy-model');
  if (model && generator && window.ResearchAnalysisJobs) {
    const prefix = (window.RESEARCH_PREFIX || '') + '/api/sn/strategies';
    const note = document.getElementById('strategy-generation-status');
    const preview = document.getElementById('strategy-generation-preview');
    const menu = generator.closest('details');
    let openingLinkedJob = false;
    const renderText = (parent, tag, value) => {
      const node = document.createElement(tag); node.textContent = value; parent.append(node); return node;
    };
    const job = window.ResearchAnalysisJobs.create({
      purpose: 'strategy_generation',
      status: note,
      button: generator,
      restoreButton: document.getElementById('strategy-generation-restore'),
      disabled: () => model.disabled || !model.value,
      snapshot: () => JSON.stringify([model.value, window.ResearchStrategyEditor?.editStamp()]),
      present(result) {
        preview.replaceChildren();
        preview.hidden = !result.strategies.length;
        if (result.status !== 'generated') {
          note.textContent = (result.gaps || ['暂无可用策略']).join('；');
          return;
        }
        note.textContent = `已取回 ${result.strategies.length} 条模拟草稿，请核对后分别保存。`;
        for (const [candidate, plan] of result.strategies.entries()) {
          const article = document.createElement('article'); preview.append(article);
          renderText(article, 'h3', plan.title);
          renderText(article, 'p', plan.thesis);
          const details = document.createElement('details'); article.append(details);
          renderText(details, 'summary', '核对合约、计划与引用证据');
          const list = document.createElement('dl'); details.append(list);
          const append = (label, value) => { renderText(list, 'dt', label); renderText(list, 'dd', value); };
          for (const leg of plan.legs) append('合约', [leg.side === 'buy' ? '买入' : '卖出', leg.contract, '×' + leg.ratio].join(' '));
          for (const [key, label] of [['entry', '入场'], ['target', '目标'], ['stop', '止损']])
            append(label, `${plan[key].low} – ${plan[key].high} · ${plan[key].reason}`);
          for (const [key, label] of [['entry_condition', '入场条件'], ['invalidation', '失效条件'], ['catalyst', '催化剂'],
            ['market_expectation', '市场预期'], ['alternative_explanation', '竞争性解释'], ['review_at', '复核时间'],
            ['horizon_days', '期限（天）'], ['risk_budget', '计划风险预算']]) append(label, plan[key]);
          for (const evidence of plan.evidence)
            append('引用证据', [evidence.series_id, evidence.value, evidence.as_of, evidence.source, evidence.statement].filter(value => value != null).join(' · '));
          const save = renderText(article, 'button', '确认保存为独立草稿'); save.type = 'button';
          save.addEventListener('click', async () => {
            save.disabled = true;
            try {
              const value = await request(prefix + '/generated/' + encodeURIComponent(result.job.id), {candidate});
              window.ResearchStrategyEditor?.savedPlan(value);
              save.textContent = '草稿已保存';
            } catch (error) { show(error.message, true); save.disabled = false; }
          });
        }
      },
    });
    const models = researchFetch(prefix + '/models').then(async response => {
      const value = await response.json();
      if (!response.ok || !Array.isArray(value.models)) throw new Error(typeof value.detail === 'string' ? value.detail : '无法读取配置模型');
      model.replaceChildren();
      for (const item of value.models) {
        const option = document.createElement('option'); option.value = item.id; option.textContent = item.name; model.append(option);
      }
      if (!value.models.length) { model.append(new Option('暂无可用通用模型', '')); return; }
      model.disabled = false; generator.disabled = job.busy;
    }).catch(error => show(error.message, true));
    generator.addEventListener('click', () => {
      if (generator.disabled || !model.value || job.busy) return;
      preview.hidden = true;
      void job.submit('/api/sn/strategies/generate',
        {date: new URLSearchParams(location.search).get('date'), model_id: model.value});
    });
    menu.addEventListener('toggle', () => { if (menu.open && !openingLinkedJob && !job.busy) void models.then(() => job.discover()); });
    void window.ResearchAnalysisJobs.requested('strategy_generation').then(async meta => {
      if (!meta) return;
      await models;
      openingLinkedJob = true;
      menu.open = true;
      try { await job.restore(meta.id, {explicit: true}); }
      finally { openingLinkedJob = false; }
    });
  }

  document.querySelectorAll('[data-action]').forEach(button => button.addEventListener('click', async () => {
    const action = button.dataset.action;
    const id = window.STRATEGY_ID;
    if (!id) return;
    const url = `${window.RESEARCH_PREFIX || ""}/api/sn/strategies/${id}/${action}`;
    let body = {};
    if (action === 'cancel') {
      const reason = prompt('请说明作废原因：');
      if (!reason?.trim()) return;
      body = {reason: reason.trim()};
    } else if (action === 'open' || action === 'close') {
      const prices = [...document.querySelectorAll('[data-fill-price]')];
      const costs = Number(document.getElementById('fill-costs')?.value || 0) / prices.length;
      const fills = prices.map(input => {
        const contract = input.dataset.fillPrice;
        const time = document.querySelector(`[data-fill-time="${contract}"]`).value;
        return {contract, price: Number(input.value),
          quantity: Number(document.querySelector(`[data-fill-qty="${contract}"]`).value),
          filled_at: `${time}:00+08:00`, costs};
      });
      if (fills.some(fill => !fill.price || !fill.quantity || !fill.filled_at)) {
        show('请完整填写每条腿的成交价格、手数和时间。', true); return;
      }
      const reason = document.getElementById('fill-reason')?.value?.trim();
      if (!reason) { show('请填写成交说明。', true); return; }
      body = {fills, reason};
    } else {
      const promptText = action === 'publish' ? '确认已审阅策略依据与风险，发布并进入等待入场状态？' : '确认操作？';
      if (!confirm(promptText)) return;
      body = {reason: '研究员审阅并确认'};
    }
    button.disabled = true;
    try { await request(url, body); location.reload(); }
    catch (error) { show(error.message, true); button.disabled = false; }
  }));
})();

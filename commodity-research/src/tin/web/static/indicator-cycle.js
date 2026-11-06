(function () {
  'use strict';
  const node = document.getElementById('indicator-cycle-config'); if (!node) return;
  const config = JSON.parse(node.textContent), U = window.ChartUI;
  const $ = id => document.getElementById(id), select = $('indicator-cycle-template'), save = $('indicator-cycle-save'), usage = $('indicator-chart-usage');
  if (!config.enabled) {
    U.notice($('indicator-cycle-status'), config.reason, 'warning'); U.notice($('indicator-related-status'), config.reason, 'warning');
    if (usage) usage.textContent = config.reason; return;
  }
  let items = [], chart, payload, token = 0, next = 0, relatedBusy = false;
  U.exports($('indicator-cycle-exports'), () => chart, () => config.name + ' · 周期对比');
  async function loadCycle() {
    const own = ++token; save.disabled = true; payload = null; chart?.destroy(); chart = null;
    $('indicator-cycle-statistics').hidden = true; U.notice($('indicator-cycle-status'), '正在读取周期对比…');
    const query = new URLSearchParams({template: select.value}); if (config.date) query.set('date', config.date);
    try {
      const result = await U.api('/indicators/' + encodeURIComponent(config.series_id) + '/cycle?' + query);
      if (own !== token) return;
      payload = result; chart = window.ChartKit.render($('indicator-cycle-plot'), result, {title: config.name + ' · 周期对比'});
      U.stats($('indicator-cycle-statistics'), result); U.notice($('indicator-cycle-status'), ''); save.disabled = !config.can_write;
    } catch (error) { if (own === token) U.notice($('indicator-cycle-status'), error.message, 'error'); }
  }
  async function templates() {
    try {
      const result = await U.api('/cycle-templates'); items = result.items;
      U.fillTemplates(select, items, items.find(item => item.builtin)?.id);
      select.disabled = !items.length;
      if (items.length) await loadCycle(); else U.notice($('indicator-cycle-status'), '当前没有可用的周期模板');
    } catch (error) { U.notice($('indicator-cycle-status'), error.message, 'error'); }
  }
  async function related() {
    if (next === null || relatedBusy) return;
    relatedBusy = true; $('indicator-related-more').disabled = true;
    try {
      const result = await U.api('/charts?' + new URLSearchParams({series_id: config.series_id, offset: next, limit: 20}));
      if (usage) usage.textContent = `图表库中有 ${result.total} 张图表引用这条指标`;
      for (const item of result.items) {
        const link = document.createElement('a'); link.className = 'charts-card'; link.href = U.pageURL('/sn/charts/' + encodeURIComponent(item.id));
        link.textContent = `${item.definition.title} · ${item.definition.group || '未分组'} · v${item.revision}`;
        $('indicator-related-charts').append(link);
      }
      U.notice($('indicator-related-status'), result.total ? '' : '暂时没有包含此指标的图表。');
      next = result.next_offset; $('indicator-related-more').hidden = next === null;
    } catch (error) { U.notice($('indicator-related-status'), error.message, 'error'); if (usage) usage.textContent = '图表引用数暂不可用：' + error.message; }
    finally { relatedBusy = false; $('indicator-related-more').disabled = false; }
  }
  save.addEventListener('click', async () => {
    if (!config.can_write || !payload || save.disabled) return;
    const template = payload.template || items.find(item => item.id === select.value)?.template;
    if (!template) return;
    save.disabled = true;
    const definition = {format: 'chart.v1', title: (config.name + ' · ' + template.name).slice(0, 120), group: '', note: '',
      curves: [{series_id: config.series_id, label: config.name.slice(0, 120), kind: 'line', axis: 'left', style: {}}],
      time: {mode: 'cycle', template, cycles: 8, band_cycles: 5, transform: template.transform || 'raw'}, origin: {kind: 'manual', ref: ''}};
    try {
      const result = await U.api('/charts', {method: 'POST', body: JSON.stringify(definition)});
      U.notice($('indicator-cycle-status'), '已保存为共享图表。');
      const link = document.createElement('a'); link.className = 'charts-button'; link.textContent = '打开已保存图表'; link.href = U.pageURL('/sn/charts/' + encodeURIComponent(result.id)); $('indicator-cycle-status').append(' ', link);
      next = 0; $('indicator-related-charts').replaceChildren(); await related();
    } catch (error) { U.notice($('indicator-cycle-status'), error.message, 'error'); }
    finally { save.disabled = !config.can_write || !payload; }
  });
  select.addEventListener('change', loadCycle); $('indicator-related-more').addEventListener('click', related);
  window.addEventListener('pagehide', () => { token++; chart?.destroy(); }, {once: true});
  templates(); related();
})();

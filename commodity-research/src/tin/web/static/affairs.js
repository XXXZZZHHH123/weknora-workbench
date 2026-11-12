(function () {
  const button = document.getElementById('affairs-check');
  if (!button) return;
  const status = document.getElementById('affairs-check-status');
  const list = document.getElementById('affairs-alert-list');
  const prefix = window.RESEARCH_PREFIX || '';
  let busy = false;
  function inform(message, error = false) { status.textContent = message; status.dataset.error = String(error); }
  function stamp(value, dateOnly = false) {
    const text = String(value ?? ''), match = text.match(/^(\d{4}-\d{2}-\d{2})(?:[T ](\d{2}:\d{2}))?/);
    return match ? (dateOnly || !match[2] ? match[1] : match[1] + ' ' + match[2]) : text;
  }
  function render(items, result) {
    const rows = [];
    for (const item of items.slice(0, 30)) {
      const row = document.createElement('a'), copy = document.createElement('div');
      row.className = 'affairs-row';
      row.href = typeof item.href === 'string' && item.href.startsWith('/sn/') ? prefix + item.href : prefix + '/sn/judgment';
      const title = document.createElement('div'), kind = document.createElement('span'), name = document.createElement('span');
      title.className = 'affairs-row-title'; kind.className = 'affairs-row-kind';
      kind.textContent = {triggered: '研判触发', missing: '数据缺失', strategy: '策略条件', strategy_missing: '策略缺数'}[item.kind] || item.kind || '检查结果'; name.textContent = item.title || '';
      row.dataset.kind = typeof item.kind === 'string' ? item.kind : '';
      title.append(kind, name);
      const detail = document.createElement('div'); detail.className = 'affairs-row-detail';
      detail.textContent = [item.detail, item.trade_date ? '检查截点 ' + item.trade_date : '',
        Array.isArray(item.evidence_dates) && item.evidence_dates.length ? '依据时点 ' + [...new Set(item.evidence_dates.map(value => stamp(value, true)))].join('、') : '',
        item.recorded_at ? '检查于 ' + stamp(item.recorded_at) : ''].filter(Boolean).join(' · ');
      copy.append(title, detail); const arrow = document.createElement('span');
      arrow.className = 'affairs-row-arrow'; arrow.textContent = '查看 →'; row.append(copy, arrow); rows.push(row);
    }
    if (!rows.length) { const empty = document.createElement('div'); empty.className = 'affairs-empty';
      empty.textContent = result.checked_rules === 0 ? '暂无可检查规则' : '暂无预警记录'; rows.push(empty); }
    list.replaceChildren(...rows);
  }
  async function check() {
    if (busy || button.disabled) return;
    busy = true; button.disabled = true; button.dataset.busy = 'true'; inform('检查中…');
    try {
      const response = await researchFetch(prefix + '/api/sn/affairs/check', {method: 'POST'});
      const result = await response.json();
      if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : '检查未完成，原记录已保留。');
      if (!Array.isArray(result.alerts)) throw new Error('检查结果格式不完整，原记录已保留。');
      render(result.alerts, result); inform([result.checked_at ? '检查于 ' + stamp(result.checked_at) : '',
        result.partial ? '部分结果' : ''].filter(Boolean).join(' · '));
    } catch (error) { inform(error.message || '检查未完成，原记录已保留。', true); }
    finally { busy = false; button.disabled = false; button.dataset.busy = 'false'; }
  }
  button.addEventListener('click', check);
  // One bounded check per page open, after the fact-free page has rendered;
  // the button re-runs it. Nothing is polled in the background.
  if (!button.disabled) check();
})();

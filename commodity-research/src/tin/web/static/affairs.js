(function () {
  const button = document.getElementById('affairs-check');
  if (!button) return;
  const status = document.getElementById('affairs-check-status');
  const list = document.getElementById('affairs-alert-list');
  const monitorCount = document.getElementById('monitor-list-toggle');
  const toasts = document.getElementById('affairs-monitor-toasts');
  const prefix = window.RESEARCH_PREFIX || '';
  let busy = false, canCheck = !button.disabled;
  let currentMonitors, monitorChanges = 0, overflowToast, overflowCount = 0;
  let otherRows = [...list.querySelectorAll('.affairs-row:not([data-kind="monitor-resident"])')];
  const nav = toasts && document.querySelector('.research-nav');
  if (nav) {
    const measureNav = () => toasts.style.setProperty('--affairs-nav-height', `${nav.offsetHeight}px`);
    measureNav();
    new ResizeObserver(measureNav).observe(nav);
  }
  function inform(message, error = false) {
    status.textContent = error ? message + ' ' : message;
    status.dataset.error = String(error);
    if (error) {
      const retry = document.createElement('a');
      retry.href = '#affairs-check';
      retry.textContent = '重试';
      retry.addEventListener('click', event => { event.preventDefault(); check(); });
      status.append(retry);
    }
  }
  function stamp(value, dateOnly = false) {
    const text = String(value ?? ''), match = text.match(/^(\d{4}-\d{2}-\d{2})(?:[T ](\d{2}:\d{2}))?/);
    return match ? (dateOnly || !match[2] ? match[1] : match[1] + ' ' + match[2]) : text;
  }
  function alertRow(item) {
    const row = document.createElement('a'), copy = document.createElement('div');
    row.className = 'affairs-row';
    row.href = prefix + (typeof item.href === 'string' && item.href.startsWith('/sn/') ? item.href : '/sn/judgment');
    const title = document.createElement('div'), kind = document.createElement('span'), name = document.createElement('span');
    title.className = 'affairs-row-title'; kind.className = 'affairs-row-kind';
    kind.textContent = {triggered: '研判触发', missing: '数据缺失', undated: '无法按日期判断',
      strategy: '策略条件', strategy_missing: '策略缺数'}[item.kind] || item.kind || '检查结果';
    name.textContent = item.title || '';
    row.dataset.kind = typeof item.kind === 'string' ? item.kind : '';
    title.append(kind, name);
    const detail = document.createElement('div'); detail.className = 'affairs-row-detail';
    detail.textContent = [item.detail, item.trade_date ? '检查截点 ' + item.trade_date : '',
      Array.isArray(item.evidence_dates) && item.evidence_dates.length ? '依据时点 ' + [...new Set(item.evidence_dates.map(value => stamp(value, true)))].join('、') : '',
      item.recorded_at ? '检查于 ' + stamp(item.recorded_at) : ''].filter(Boolean).join(' · ');
    copy.append(title, detail); const arrow = document.createElement('span');
    arrow.className = 'affairs-row-arrow'; arrow.textContent = '查看 →'; row.append(copy, arrow);
    return row;
  }
  function residentRow(item) {
    const row = alertRow({title: item.title, kind: 'monitor-resident'});
    row.href = prefix + '/?monitor=' + encodeURIComponent(item.id) + '#monitors';
    row.dataset.status = item.current_state || '未检查';
    row.querySelector('.affairs-row-kind').textContent = row.dataset.status;
    const description = item.summary || (item.rules || []).map(rule => rule.label).join(item.logic === 'any' ? ' 或 ' : ' 且 ');
    const detail = row.querySelector('.affairs-row-detail');
    detail.className += ' monitor-description';
    detail.title = detail.textContent = [description, item.last_trigger && '最近触发 ' + stamp(item.last_trigger, true)].filter(Boolean).join(' · ');
    return row;
  }
  function render(checkable) {
    const residents = monitorCount && Array.isArray(currentMonitors) ? currentMonitors.filter(item => item.enabled)
      .sort((left, right) => left.title < right.title ? -1 : Number(left.title > right.title)).map(residentRow) : [];
    const rows = [...residents.filter(row => row.dataset.status === '触发'), ...otherRows,
      ...residents.filter(row => row.dataset.status !== '触发')];
    const empty = document.createElement('div');
    empty.className = 'affairs-empty';
    empty.textContent = checkable ? '暂无预警记录' : '暂无可检查规则';
    list.replaceChildren(...(rows.length ? rows : [empty]));
  }
  function toast(content, actions, onRemove) {
    const card = document.createElement('div'), close = document.createElement('button');
    card.className = 'affairs-monitor-toast'; close.className = 'affairs-check'; close.type = 'button';
    close.textContent = '×'; close.setAttribute('aria-label', '关闭监测提示');
    const controls = document.createElement('div'); controls.className = 'affairs-monitor-toast-actions';
    controls.append(...actions, close); card.append(content, controls);
    let timer, started, remaining = 10000, hovered = false, focused = false;
    const dismiss = () => { clearTimeout(timer); card.remove(); onRemove?.(); };
    const update = () => {
      if (hovered || focused) {
        if (timer !== undefined) { remaining -= Date.now() - started; clearTimeout(timer); timer = undefined; }
      } else if (timer === undefined) {
        started = Date.now(); timer = setTimeout(dismiss, remaining);
      }
    };
    card.addEventListener('mouseenter', () => { hovered = true; update(); });
    card.addEventListener('mouseleave', () => { hovered = false; update(); });
    card.addEventListener('focusin', () => { focused = true; update(); });
    card.addEventListener('focusout', event => { focused = card.contains(event.relatedTarget); update(); });
    close.addEventListener('click', dismiss);
    toasts.append(card); update();
    return {card, dismiss};
  }
  const visibleToasts = [];
  function notify(alerts) {
    if (!toasts || !alerts.length) return;
    const slots = overflowToast || visibleToasts.length + alerts.length > 3 ? 2 : 3;
    let extra = 0;
    if (visibleToasts.length > slots) {
      const replaceable = [...visibleToasts].reverse().find(entry => !entry.card.contains(document.activeElement));
      replaceable.dismiss(); extra++;
    }
    for (const item of alerts) {
      if (visibleToasts.length >= slots) { extra++; continue; }
      const content = document.createElement('div'), kind = document.createElement('span');
      const title = document.createElement('div'), detail = document.createElement('div'), link = document.createElement('a');
      kind.className = 'affairs-row-kind'; kind.textContent = '监测触发';
      title.className = 'affairs-row-title'; title.textContent = (item.title || '').replace(/ · 监测触发$/, '');
      detail.className = 'affairs-row-detail'; detail.textContent = item.detail || '';
      link.className = 'affairs-check'; link.textContent = '查看'; link.dataset.monitorToast = '';
      link.href = prefix + item.href;
      content.append(kind, title, detail);
      const entry = toast(content, [link], () => visibleToasts.splice(visibleToasts.indexOf(entry), 1));
      visibleToasts.push(entry);
    }
    if (extra) {
      overflowCount += extra;
      if (!overflowToast) {
        const content = document.createElement('div');
        overflowToast = {...toast(content, [], () => { overflowToast = null; overflowCount = 0; }), content};
      }
      overflowToast.content.textContent = `另有 ${overflowCount} 项监测触发`;
    }
  }
  async function check() {
    if (busy || button.disabled) return;
    const changesBeforeCheck = monitorChanges;
    const failed = '检查未完成，原记录已保留。';
    busy = true; button.disabled = true; button.dataset.busy = 'true'; inform('检查中…');
    try {
      const response = await researchFetch(prefix + '/api/sn/affairs/check', {method: 'POST'});
      const result = await response.json().catch(() => null);
      if (!response.ok) {
        inform(typeof result?.detail === 'string' && result.detail.trim() ? result.detail : failed, true);
        return;
      }
      if (!Array.isArray(result?.alerts)) {
        inform('检查结果格式不完整，原记录已保留。', true);
        return;
      }
      const monitorsEdited = changesBeforeCheck !== monitorChanges;
      if (!monitorsEdited && Array.isArray(result.monitors)) currentMonitors = result.monitors;
      otherRows = result.alerts.filter(item => item.kind !== 'monitor').slice(0, 30).map(alertRow);
      render(monitorsEdited ? canCheck : result.checked_rules !== 0 || result.checked_monitors > 0 || currentMonitors?.some(item => item.enabled));
      notify(result.alerts.filter(item => item.kind === 'monitor' && typeof item.href === 'string'
        && /^\/\?monitor=[A-Za-z0-9%._~-]+#monitors$/.test(item.href)));
      inform([result.checked_at ? '检查于 ' + stamp(result.checked_at) : '',
        result.partial ? '部分结果' : ''].filter(Boolean).join(' · '));
      if (typeof window.dispatchEvent === 'function') window.dispatchEvent(new CustomEvent('affairs-checked'));
    } catch (error) { inform(error?.network ? error.message : failed, true); }
    finally { busy = false; button.disabled = !canCheck; button.dataset.busy = 'false'; }
  }
  button.addEventListener('click', check);
  if (typeof window.addEventListener === 'function') window.addEventListener('monitors-changed', event => {
    const {count, enabled, items, changed} = event.detail || {};
    if (monitorCount && Number.isInteger(count)) monitorCount.textContent = '监测 ' + count;
    canCheck = button.dataset.canCheckRules === 'true' || enabled === true;
    if (!busy) button.disabled = !canCheck;
    if (!monitorCount || !Array.isArray(items)) return;
    if (changed) monitorChanges++;
    currentMonitors = items;
    render(canCheck);
  });
  // One bounded check per page open, after the fact-free page has rendered;
  // the button re-runs it. Nothing is polled in the background.
  if (!button.disabled) check();
})();

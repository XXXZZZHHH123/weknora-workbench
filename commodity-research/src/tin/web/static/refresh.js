(() => {
  const panel = document.getElementById('research-refresh');
  if (!panel) return;
  const byId = id => document.getElementById(id);
  const base = (window.RESEARCH_PREFIX || '') + '/api/sn/refresh';
  let state, timer, editing = false, observedRun = null, reloading = false;
  const prefix = window.RESEARCH_PREFIX || '';
  const defaultBoard = [prefix, prefix + '/', prefix + '/sn'].includes(location.pathname) && !new URLSearchParams(location.search).has('date');
  const stamp = value => value ? new Date(value).toLocaleString('zh-CN', {timeZone:'Asia/Shanghai', hour12:false}) : '暂无';
  const text = (id, value) => { byId(id).textContent = value; };
  async function request(path, options = {}) {
    const response = await researchFetch(base + path, options);
    const result = await response.json();
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : '更新请求失败，请检查设置');
    return result;
  }
  function show(data) {
    if (data.active) observedRun = data.active.id;
    const finishedObserved = observedRun && data.last_attempt?.id === observedRun && !data.active && ['success', 'partial'].includes(data.last_attempt.status);
    const completedBetweenPolls = state && !data.active && data.last_attempt?.id !== state.last_attempt?.id && ['success', 'partial'].includes(data.last_attempt?.status);
    const newerTradeDate = !data.active && ['success', 'partial'].includes(data.last_attempt?.status) && data.latest_trade_date && data.latest_trade_date !== panel.dataset.renderedDate;
    state = data;
    if (defaultBoard && !reloading && (finishedObserved || completedBetweenPolls || newerTradeDate)) {
      // The SSR board uses the data available when the page was rendered. Polling
      // only refreshes this status panel; reload once after new facts are ready.
      // A user-selected historical date is never changed by background updates.
      reloading = true;
      location.reload();
      return;
    }
    const recent = data.active || data.last_attempt;
    text('refresh-status', recent ? `${recent.label}${recent.error ? '：' + recent.error : ''}` : '尚未执行更新。可以立即更新，或等待计划时间。');
    byId('refresh-status').dataset.state = recent?.status || 'idle';
    text('refresh-summary', `${data.scheduler_available ? ('自动更新' + (data.enabled ? '已开启' : '已暂停')) : '可手动更新'} · 每天 ${data.times.join('、')}（北京时间）`);
    text('refresh-trade', data.latest_trade_date || '暂无行情');
    text('refresh-next', stamp(data.next_run_at));
    text('refresh-success', stamp(data.last_success));
    const rows = byId('refresh-sources'); rows.replaceChildren();
    data.sources.forEach(source => {
      const row = document.createElement('tr'); row.className = 'refresh-source-row';
      [source.name + (source.enabled ? '' : '（自动更新已关闭）'), (source.status === 'ok' ? '更新成功' : source.status) + (source.error ? '：' + source.error : ''),
       stamp(source.data_cutoff), stamp(source.last_attempt) + ' / ' + stamp(source.last_success), String(source.written)].forEach(value => {
        const cell = document.createElement('td'); cell.className = 'refresh-source-cell'; cell.textContent = value; row.append(cell);
      });
      const action = document.createElement('td'); action.className = 'py-2 whitespace-nowrap';
      const button = document.createElement('button'); button.type = 'button';
      button.className = 'research-button compact secondary';
      button.textContent = ['失败', '部分失败'].includes(source.status) ? '重试' : '更新';
      button.disabled = !!data.active || panel.dataset.readonly === 'true';
      button.addEventListener('click', () => startUpdate([source.id], button));
      action.append(button); row.append(action); rows.append(row);
    });
    if (!editing) {
      byId('refresh-enabled').checked = data.enabled;
      byId('refresh-times').value = data.times.join(', ');
      const fieldset = byId('refresh-source-settings'); fieldset.replaceChildren();
      data.sources.forEach(source => {
        const label = document.createElement('label'); label.className = 'flex gap-2 items-center';
        const input = document.createElement('input'); input.type = 'checkbox'; input.value = source.id; input.checked = source.enabled;
        label.append(input, document.createTextNode(source.name)); fieldset.append(label);
      });
    }
    byId('refresh-now').disabled = !!data.active || (window.RESEARCH_CONTEXT && panel.dataset.readonly === 'true');
  }
  async function load() {
    clearTimeout(timer);
    try { show(await request('')); }
    catch (error) { text('refresh-status', error.message); }
    timer = setTimeout(load, state?.active ? 3000 : 60000);
  }
  // Server-side role controls are authoritative; preserve the initial disabled state.
  panel.dataset.readonly = byId('refresh-now').disabled ? 'true' : 'false';
  async function startUpdate(sources = null, button = byId('refresh-now')) {
    button.disabled = true;
    const options = sources ? {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({sources})} : {method:'POST'};
    try { const result = await request('/run', options); observedRun = result.run_id; text('refresh-status', result.message); await load(); }
    catch (error) { text('refresh-status', error.message); button.disabled = panel.dataset.readonly === 'true'; }
  }
  byId('refresh-now').addEventListener('click', () => startUpdate());
  byId('refresh-settings-toggle').addEventListener('click', () => {
    editing = !editing; byId('refresh-settings').classList.toggle('hidden', !editing);
  });
  byId('refresh-settings-save').addEventListener('click', async () => {
    const button = byId('refresh-settings-save'); button.disabled = true;
    const payload = {enabled:byId('refresh-enabled').checked,
      times:byId('refresh-times').value.split(/[,，]/).map(value => value.trim()).filter(Boolean),
      sources:[...byId('refresh-source-settings').querySelectorAll('input:checked')].map(input => input.value)};
    try {
      const result = await request('/settings', {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload)});
      editing = false; byId('refresh-settings').classList.add('hidden'); show(result); text('refresh-status', '更新设置已保存');
    } catch (error) { text('refresh-status', error.message); }
    finally { button.disabled = false; }
  });
  load();
  window.addEventListener('pagehide', () => clearTimeout(timer), {once:true});
})();

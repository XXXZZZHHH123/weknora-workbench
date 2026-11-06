document.addEventListener('DOMContentLoaded', () => {
  'use strict';
  const root = document.getElementById('indicator-dedup');
  const banner = document.querySelector('[data-dedup-banner]');
  const sources = document.querySelector('[data-dedup-sources]');
  if (!root && !banner && !sources) return;
  const prefix = window.RESEARCH_PREFIX || '', api = prefix + '/api/sn/indicator-dedup';
  const states = {pending: '待确认', confirmed_default: '以 default 库为准', keep_both: '保留两者', rejected: '不是同一指标'};
  const name = field => field.metric || field.name || field.series_id;
  function node(tag, text, className) {
    const result = document.createElement(tag);
    if (text !== undefined) result.textContent = text;
    if (className) result.className = className;
    return result;
  }
  async function request(path = '', body) {
    const response = await window.researchFetch(api + path, body === undefined ? {} : {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : '指标对应暂时不可读，请刷新后重试。');
    return result;
  }
  function indicatorLink(field) {
    const link = node('a', name(field));
    link.href = prefix + '/sn/indicators/' + encodeURIComponent(field.series_id);
    return link;
  }
  function evidenceText(evidence) {
    if (!evidence) return '尚未核实重叠期数值。请先核实证据。';
    return `一致 ${evidence.matching_periods} 期 · 不一致 ${evidence.mismatching_periods} 期 · 最近不一致日期：${evidence.latest_mismatch_date || '无'}。` +
      `单位${evidence.unit_match ? '一致' : '不一致或缺失'}，频率${evidence.frequency_match ? '一致' : '不一致或缺失'}。` +
      `个人库截至 ${evidence.personal_as_of || '无数据'}；default 库截至 ${evidence.default_as_of || '无数据'}。` +
      '仅核实各序列最近 500 期的重叠日期；不填补空缺。' +
      (evidence.precision_complete === false ? '部分原值显示精度无法核实，当前比较仅供参考，不能批量确认。' : '');
  }
  if (root) {
    const status = document.getElementById('dedup-status'), groups = document.getElementById('dedup-groups');
    const batch = document.getElementById('dedup-batch-confirm'), permission = document.getElementById('dedup-permission');
    let data, busy = false, selectionOverflow = false;
    function button(text, action, enabled = true) {
      const value = node('button', text, 'indicator-button'); value.type = 'button'; value.disabled = !enabled;
      value.addEventListener('click', () => run(action)); return value;
    }
    function sync() {
      const count = root.querySelectorAll('[data-dedup-select]:checked').length;
      batch.disabled = busy || !data?.can_confirm || !count || count > 20;
      if (count > 20) status.textContent = '每批最多选择 20 组，请减少所选候选。';
      else if (selectionOverflow) status.textContent = `共 ${data.items.length} 组对应，${data.pending_count} 组待确认。`;
      selectionOverflow = count > 20;
    }
    function render() {
      groups.replaceChildren();
      permission.hidden = data.can_confirm;
      permission.textContent = '当前账号仅可查看和核实。确认与撤销需要本空间中心来源维护权限（已有读取权限的空间管理员）。';
      const buckets = new Map();
      for (const item of data.items) {
        const personal = item.personal, standard = item.default;
        const key = JSON.stringify([personal.knowledge_base_id, personal.source_file, personal.sheet]);
        let group = buckets.get(key);
        if (!group) {
          group = node('section', undefined, 'dedup-group'); group.dataset.dedupGroup = key;
          group.append(node('h2', `${personal.knowledge_base_name || personal.knowledge_base_id || '个人库'} · ${personal.source_file || '来源文件'} · ${personal.sheet || '未注明工作表'}`));
          buckets.set(key, group); groups.append(group);
        }
        const row = node('article', undefined, 'dedup-row'); row.dataset.dedupRow = item.id;
        const heading = node('header');
        const select = node('input'); select.type = 'checkbox'; select.dataset.dedupSelect = item.id;
        select.setAttribute('aria-label', '选择 ' + name(personal));
        select.disabled = !data.can_confirm || !item.batch_eligible || item.state !== 'pending';
        select.addEventListener('change', sync);
        heading.append(select, node('strong', states[item.state] || item.state)); row.append(heading);
        const fields = node('div', undefined, 'dedup-fields');
        for (const [label, field] of [['个人库副本', personal], ['default 库', standard]]) {
          const box = node('div'); box.append(node('b', label), node('p')); box.lastChild.append(indicatorLink(field));
          box.append(node('p', `${field.unit || '单位未注明'} · ${field.frequency || '频率未注明'}`), node('p', field.series_id, 'dedup-code'));
          fields.append(box);
        }
        row.append(fields);
        const codes = item.codes || item.candidate?.codes || item.shared_codes;
        row.append(node('p', (item.candidate?.reason ? item.candidate.reason + '。' : '') +
          (codes?.length ? '共同编码：' + codes.map(c => typeof c === 'string' ? c : `${c.vendor} · ${c.code}`).join('、') : '候选证据请结合完整名称与字段口径核对。'), 'indicator-muted'));
        row.append(node('p', evidenceText(item.evidence), 'dedup-evidence'));
        if (item.evidence?.stale_default) row.append(node('p', 'default 库序列截至日期早于个人库副本，请核对中心来源更新情况；仍使用已确认的 default 库序列。', 'indicator-warning'));
        const actions = node('div', undefined, 'dedup-actions');
        const verify = button('核实最近 500 期', async () => { await request('/' + encodeURIComponent(item.id) + '/verify', {sources_revision: data.sources_revision}); await load(); });
        verify.dataset.dedupVerify = item.id; actions.append(verify);
        for (const state of ['confirmed_default', 'keep_both', 'rejected']) {
          const choice = button(states[state], async () => {
            await request('/decisions', {ids: [item.id], revisions: {[item.id]: item.revision}, state, batch: false, sources_revision: data.sources_revision}); await load();
          }, data.can_confirm && item.state === 'pending' && (state !== 'confirmed_default' || Boolean(item.evidence)));
          choice.dataset.dedupDecision = state; actions.append(choice);
        }
        if (item.state !== 'pending') {
          const undo = button('撤销决定', async () => { await request('/' + encodeURIComponent(item.id) + '/undo', {revision: item.revision, sources_revision: data.sources_revision}); await load(); }, data.can_confirm);
          undo.dataset.dedupUndo = item.id; actions.append(undo);
        }
        row.append(actions);
        if (item.history?.length) {
          const history = node('details'); history.append(node('summary', '决定与撤销记录'));
          for (const event of item.history) history.append(node('p', `${event.actor || event.confirmed_by || '系统'} · ${event.at || event.created_at || event.time || ''} · ${states[event.state || event.to_state] || event.action || event.state || ''}`));
          row.append(history);
        }
        group.append(row);
      }
      status.textContent = data.items.length ? `共 ${data.items.length} 组对应，${data.pending_count} 组待确认。` : '当前来源未发现可能重复的指标。';
      sync();
    }
    async function load() { data = await request(); if (!Array.isArray(data.items)) throw new Error('指标对应响应不完整'); render(); }
    async function run(action) {
      if (busy) return; busy = true; root.setAttribute('aria-busy', 'true'); sync();
      const buttons = [...root.querySelectorAll('button')]; const disabled = buttons.map(value => value.disabled); buttons.forEach(value => { value.disabled = true; });
      try { await action(); } catch (error) { status.textContent = error.message; }
      finally { busy = false; root.removeAttribute('aria-busy'); buttons.forEach((value, index) => { if (value.isConnected) value.disabled = disabled[index]; }); sync(); }
    }
    document.getElementById('dedup-reload').addEventListener('click', () => run(load));
    batch.addEventListener('click', () => run(async () => {
      const ids = [...root.querySelectorAll('[data-dedup-select]:checked')].map(value => value.dataset.dedupSelect);
      const revisions = Object.fromEntries(data.items.filter(item => ids.includes(item.id)).map(item => [item.id, item.revision]));
      await request('/decisions', {ids, revisions, state: 'confirmed_default', batch: true, sources_revision: data.sources_revision}); await load();
    }));
    run(load);
  }
  if (banner) request().then(data => {
    banner.hidden = !data.pending_count;
    banner.querySelector('a').textContent = `发现 ${data.pending_count} 组可能重复的指标 · 指标对应确认`;
  }).catch(error => { banner.hidden = false; banner.textContent = error.message; });
  if (sources) request().then(async data => {
    const identifier = sources.dataset.dedupSources;
    const related = data.items.filter(item => item.state === 'confirmed_default' && [item.personal.series_id, item.default.series_id].includes(identifier));
    if (!related.length) return;
    sources.hidden = false; sources.append(node('h2', '指标对应来源'));
    for (const item of related) {
      const paragraph = node('p', item.default.series_id === identifier ? '已与个人库对应，副本保留：' : '本指标已对应至 default 库：');
      const field = item.default.series_id === identifier ? item.personal : item.default;
      paragraph.append(indicatorLink(field), document.createTextNode(` · ${field.source_file || field.knowledge_base_id || ''} · ${field.sheet || ''}`));
      sources.append(paragraph);
      try {
        const verified = await request('/' + encodeURIComponent(item.id) + '/verify', {sources_revision: data.sources_revision});
        const evidence = verified.evidence || verified.item?.evidence;
        sources.append(node('p', evidenceText(evidence), 'text-xs'));
        if (evidence?.stale_default) sources.append(node('p', 'default 库序列截至日期早于个人库副本，请核对中心来源更新情况；仍使用已确认的 default 库序列。', 'indicator-warning'));
      } catch (error) { sources.append(node('p', error.message, 'indicator-warning')); }
    }
    const manage = node('a', '查看对应与撤销记录'); manage.href = prefix + '/sn/indicator-dedup'; sources.append(manage);
  }).catch(error => { sources.hidden = false; sources.textContent = error.message; });
});

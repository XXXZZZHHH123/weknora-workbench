document.addEventListener('DOMContentLoaded', () => {
  'use strict';
  const root = document.getElementById('indicator-dedup');
  const banner = document.querySelector('[data-dedup-banner]');
  const sources = document.querySelector('[data-dedup-sources]');
  if (!root && !banner && !sources) return;
  const prefix = window.RESEARCH_PREFIX || '', api = prefix + '/api/sn/indicator-dedup';
  const states = {pending: '待确认', confirmed_default: '以共享库为准', keep_both: '保留两者', rejected: '不是同一指标'};
  const isRemap = item => item.remap || item.candidate?.kind === 'remap';
  const name = field => field ? [field.metric,field.name].find(value=>value && value!==field.series_id) || '未命名指标' : '来源已不可用';
  const localTime = new Intl.DateTimeFormat('sv-SE',{timeZone:'Asia/Shanghai',dateStyle:'short',timeStyle:'short'});
  const accountLabel = value => value===window.RESEARCH_CONTEXT?.user ? '本人' :
    value && !/^[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}$/i.test(value) ? value : '研究员';
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
  function evidenceText(evidence, remap = false) {
    if (!evidence) return '尚未核实重叠期数值。请先核实证据。';
    if (remap) return `已逐条核对 ${evidence.matching_periods} 条原表记录，内容一致。`;
    return `一致 ${evidence.matching_periods} 期 · 不一致 ${evidence.mismatching_periods} 期 · 最近不一致日期：${evidence.latest_mismatch_date || '无'}。` +
      `单位${evidence.unit_match ? '一致' : '不一致或缺失'}，频率${evidence.frequency_match ? '一致' : '不一致或缺失'}。` +
      `个人库截至 ${evidence.personal_as_of || '无数据'}；共享库截至 ${evidence.default_as_of || '无数据'}。` +
      '仅核实各序列最近 500 期的重叠日期；不填补空缺。' +
      (evidence.precision_complete === false ? '部分原值显示精度无法核实，当前比较仅供参考，不能批量确认。' : '');
  }
  if (root) {
    const status = document.getElementById('dedup-status'), groups = document.getElementById('dedup-groups');
    const batch = document.getElementById('dedup-batch-confirm'), permission = document.getElementById('dedup-permission');
    const selectRemaps = document.getElementById('dedup-select-remaps');
    let data, busy = false, selectionOverflow = false;
    function button(text, action, enabled = true) {
      const value = node('button', text, 'indicator-button'); value.type = 'button'; value.disabled = !enabled;
      value.addEventListener('click', () => run(action)); return value;
    }
    function sync() {
      const selected = [...root.querySelectorAll('[data-dedup-select]:checked')];
      const remaps = selected.filter(input => input.dataset.remap).length;
      const mixed = Boolean(remaps && remaps !== selected.length);
      const overflow = selected.length > (remaps ? 10000 : 20);
      batch.disabled = busy || !data?.can_confirm || !selected.length || mixed || overflow;
      batch.textContent = remaps && !mixed ? '用所选新版指标更新引用' : '所选以共享库为准';
      if (mixed) status.textContent = '请选择同一类候选进行确认。';
      else if (overflow) status.textContent = '所选候选超过单批上限，请减少所选候选。';
      else if (selectionOverflow) status.textContent = `共 ${data.items.length} 组对应，${data.pending_count} 组待确认。`;
      selectionOverflow = mixed || overflow;
    }
    function render() {
      groups.replaceChildren();
      permission.hidden = data.can_confirm;
      permission.textContent = '当前账号可查看和核对。要确认或撤销，请联系共享库维护者，或有权编辑原表和研究台的同事。';
      selectRemaps.hidden = !data.items.some(item => isRemap(item) && item.batch_eligible);
      selectRemaps.disabled = !data.can_confirm;
      const buckets = new Map();
      for (const item of data.items) {
        const personal = item.personal, standard = item.default;
        const remap = isRemap(item);
        const labelFor = state => remap && state === 'confirmed_default' ? '用新版指标更新引用' : states[state] || '暂时无法确认';
        const canConfirm = item.can_confirm ?? data.can_confirm;
        const key = JSON.stringify(personal ? [personal.knowledge_base_id, personal.source_file, personal.sheet] : ['unavailable']);
        let group = buckets.get(key);
        if (!group) {
          group = node('section', undefined, 'dedup-group'); group.dataset.dedupGroup = key;
          group.append(node('h2', personal ? `${personal.knowledge_base_name || '个人库'} · ${personal.source_file || '来源文件未注明'} · ${personal.sheet || '未注明工作表'}` : '个人库来源已不可用'));
          buckets.set(key, group); groups.append(group);
        }
        const row = node('article', undefined, 'dedup-row'); row.dataset.dedupRow = item.id;
        const heading = node('header');
        const select = node('input'); select.type = 'checkbox'; select.dataset.dedupSelect = item.id;
        select.setAttribute('aria-label', '选择 ' + name(personal));
        if (remap) select.dataset.remap = 'true';
        select.disabled = !canConfirm || !item.batch_eligible || item.state !== 'pending';
        select.addEventListener('change', sync);
        heading.append(select, node('strong', labelFor(item.state))); row.append(heading);
        const fields = node('div', undefined, 'dedup-fields');
        for (const [label, field] of (remap ? [['旧版指标', personal], ['新版指标', standard]]
                                            : [['个人库副本', personal], ['共享库', standard]])) {
          if (!field) {
            const box = node('div'); box.append(node('b', label), node('p', '来源已不可用，未读取历史字段资料。', 'indicator-muted'));
            fields.append(box); continue;
          }
          const box = node('div'); box.append(node('b', label), node('p'));
          box.lastChild.append(remap && label === '旧版指标' ? document.createTextNode(name(field)) : indicatorLink(field));
          box.append(node('p', [field.knowledge_base_name, field.source_file, field.sheet, field.unit || '单位未注明', field.frequency || '频率未注明'].filter(Boolean).join(' · ')));
          fields.append(box);
        }
        row.append(fields);
        if (item.source_unavailable) {
          row.append(node('p', '一侧来源已不可用。原决定继续保留，不会自动切换来源；可显式撤销决定。', 'indicator-warning'));
        } else {
          const codes = item.codes || item.candidate?.codes || item.shared_codes;
          const location = remap
            ? `同一原件 · 区块 ${item.candidate.block_index + 1} · ${item.candidate.column} 列` +
              (item.candidate.category === null ? '' : ` · 类别 ${item.candidate.category}`)
            : (item.candidate?.reason ? item.candidate.reason + '。' : '') +
              (codes?.length ? '相同的供应商指标代码：' + codes.map(c => typeof c === 'string' ? c : `${c.vendor} · ${c.code}`).join('、') : '请核对指标的完整名称、来源和统计口径。');
          row.append(node('p', location, 'indicator-muted'));
          row.append(node('p', evidenceText(item.evidence, remap), 'dedup-evidence'));
        }
        if (item.evidence?.stale_default) row.append(node('p', '共享库的数据日期早于个人库副本。请查看共享库是否需要更新，当前仍使用已确认的共享库数据。', 'indicator-warning'));
        const actions = node('div', undefined, 'dedup-actions');
        const verify = button(remap ? '核对全部记录' : '核实最近 500 期', async () => { await request('/' + encodeURIComponent(item.id) + '/verify', {sources_revision: data.sources_revision}); await load(); });
        verify.dataset.dedupVerify = item.id; verify.disabled = Boolean(item.source_unavailable); actions.append(verify);
        for (const state of ['confirmed_default', 'keep_both', 'rejected']) {
          const choice = button(labelFor(state), async () => {
            await request('/decisions', {ids: [item.id], revisions: {[item.id]: item.revision}, state, batch: false, sources_revision: data.sources_revision}); await load();
          }, canConfirm && item.state === 'pending' && (state !== 'confirmed_default' || Boolean(item.evidence)));
          choice.dataset.dedupDecision = state; actions.append(choice);
        }
        if (item.state !== 'pending') {
          const undo = button(remap && item.state === 'confirmed_default' ? '撤销决定并恢复引用' : '撤销决定', async () => { await request('/' + encodeURIComponent(item.id) + '/undo', {revision: item.revision, sources_revision: data.sources_revision}); await load(); }, canConfirm);
          undo.dataset.dedupUndo = item.id; actions.append(undo);
        }
        row.append(actions);
        if (item.history?.length) {
          const history = node('details'); history.append(node('summary', '决定与撤销记录'));
          for (const event of item.history) {
            const actor = event.actor || event.confirmed_by;
            const recorded = event.at || event.created_at || event.time || '';
            const stamp = new Date(recorded);
            const time = /^\d{4}-\d{2}-\d{2}$/.test(recorded) ? recorded :
              Number.isNaN(stamp.getTime()) ? '' : localTime.format(stamp);
            const action = event.action==='undo' ? '撤销决定' : event.state || event.to_state ? labelFor(event.state || event.to_state) : '更新指标对应关系';
            history.append(node('p',[actor ? accountLabel(actor) : '系统',time,action].filter(Boolean).join(' · ')));
          }
          row.append(history);
        }
        group.append(row);
      }
      status.textContent = data.items.length ? `共 ${data.items.length} 组对应，${data.pending_count} 组待确认。` : '当前来源未发现可能重复的指标。';
      sync();
    }
    async function load() { data = await request('?include_remaps=true'); if (!Array.isArray(data.items)) throw new Error('指标对应响应不完整'); render(); }
    async function run(action) {
      if (busy) return; busy = true; root.setAttribute('aria-busy', 'true'); sync();
      const buttons = [...root.querySelectorAll('button')]; const disabled = buttons.map(value => value.disabled); buttons.forEach(value => { value.disabled = true; });
      try { await action(); } catch (error) { status.textContent = error.message; }
      finally { busy = false; root.removeAttribute('aria-busy'); buttons.forEach((value, index) => { if (value.isConnected) value.disabled = disabled[index]; }); sync(); }
    }
    document.getElementById('dedup-reload').addEventListener('click', () => run(load));
    selectRemaps.addEventListener('click', () => {
      for (const input of root.querySelectorAll('[data-dedup-select]')) {
        input.checked = Boolean(input.dataset.remap && !input.disabled);
      }
      sync();
    });
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
    const related = data.items.filter(item => !item.source_unavailable && item.state === 'confirmed_default' && [item.personal.series_id, item.default.series_id].includes(identifier));
    if (!related.length) return;
    sources.hidden = false; sources.append(node('h2', '指标对应来源'));
    for (const item of related) {
      const paragraph = node('p', item.default.series_id === identifier ? '已与个人库对应，副本保留：' : '本指标已对应至 共享库：');
      const field = item.default.series_id === identifier ? item.personal : item.default;
      paragraph.append(indicatorLink(field), document.createTextNode(' · '+[field.source_file||field.knowledge_base_name||'来源未注明',field.sheet].filter(Boolean).join(' · ')));
      sources.append(paragraph);
      try {
        const verified = await request('/' + encodeURIComponent(item.id) + '/verify', {sources_revision: data.sources_revision});
        const evidence = verified.evidence || verified.item?.evidence;
        sources.append(node('p', evidenceText(evidence), 'text-xs'));
        if (evidence?.stale_default) sources.append(node('p', '共享库的数据日期早于个人库副本。请查看共享库是否需要更新，当前仍使用已确认的共享库数据。', 'indicator-warning'));
      } catch (error) { sources.append(node('p', error.message, 'indicator-warning')); }
    }
    const manage = node('a', '查看对应与撤销记录'); manage.href = prefix + '/sn/indicator-dedup'; sources.append(manage);
  }).catch(error => { sources.hidden = false; sources.textContent = error.message; });
});

(function () {
  'use strict';
  const page = document.querySelector('[data-charts-page]'), configNode = document.getElementById('chart-page-config');
  if (!page || !configNode) return;
  const config = JSON.parse(configNode.textContent), U = window.ChartUI, K = window.ChartKit;
  if (!config.enabled) return;
  const $ = id => document.getElementById(id), E = U.esc;
  const dataDate = config.date ? '?date=' + encodeURIComponent(config.date) : '';
  if (page.dataset.chartsPage === 'library') {
    let started = false;
    const start = () => {
      if (!started && !page.closest('[data-indicator-panel]')?.hidden) { started = true; library(); }
    };
    document.addEventListener('indicator:tab', start);
    start();
  }
  if (page.dataset.chartsPage === 'detail') detail();

  async function anomalies() {
    const status = $('chart-anomalies-status'), table = $('chart-anomalies-table'), more = $('chart-anomalies-more');
    const labels = {extreme_high: '同期前两高', extreme_low: '同期前两低', rank_change: '排名变化'};
    const controller = new AbortController();
    window.addEventListener('pagehide', () => controller.abort(), {once: true});
    U.notice(status, '正在读取同期位置…');
    try {
      const data = await U.api('/charts/anomalies' + dataDate, {signal: controller.signal});
      let expanded = false;
      function render() {
        const items = expanded ? data.items : data.items.slice(0, 10);
        $('chart-anomalies-rows').innerHTML = items.map(item => {
          const link = U.pageURL('/sn/charts/' + encodeURIComponent(item.id)) + '?date=' + encodeURIComponent(data.as_of);
          const unit = item.transform === 'rebase' ? '（起点=100）' : (item.unit ? ' ' + item.unit : '') + (item.transform === 'change' ? '（相对起点变化）' : '');
          const previous = item.previous_rank == null ? '暂无可用排名' : `${item.previous_rank}/${item.previous_n + 1}`;
          return `<tr><td><a href="${E(link)}">${E(item.title)}</a></td><td>${E(item.metric)}</td><td>${E(item.date)}</td><td>${E(U.fmt(item.value) + unit)}</td><td>${E(item.rank)}/${E(item.n + 1)}</td><td>${E(previous)}${item.previous_date ? `<br><span class="charts-muted">${E(item.previous_date)}</span>` : ''}</td><td>${item.reasons.map(reason => `<span class="charts-anomaly-tag">${E(labels[reason] || reason)}</span>`).join('')}</td></tr>`;
        }).join('');
        table.hidden = !items.length;
        $('chart-anomalies-count').textContent = `共 ${data.total} 张`;
        more.hidden = data.items.length <= 10;
        more.textContent = expanded ? '收起为前 10 条' : `展开全部 ${data.total} 条`;
        more.setAttribute('aria-expanded', String(expanded));
      }
      more.addEventListener('click', () => { expanded = !expanded; render(); });
      render();
      const limited = data.items.filter(item => item.warnings?.includes('observation_window_limited')).length;
      U.notice(status, !data.items.length ? '本期没有同期前两高、前两低或排名明显变化的图表' :
        limited ? `${limited} 张图表已达到观测读取上限，排名仅覆盖已读取的窗口。` : '', limited ? 'warning' : '');
      const failed = data.failed || [];
      $('chart-anomalies-failed').hidden = !failed.length;
      $('chart-anomalies-failed-summary').textContent = `${failed.length} 张图表读取或计算失败，未纳入本次清单，展开查看原因`;
      $('chart-anomalies-failed-list').innerHTML = failed.map(item => `<li>${E(item.title)}：${E(item.reason)}</li>`).join('');
    } catch (error) {
      if (error.name !== 'AbortError') U.notice(status, '同期异动读取失败：' + error.message, 'error');
    }
  }

  function library() {
    anomalies();
    const cards = $('chart-cards'), status = $('charts-status'), more = $('charts-more'), groups = $('chart-groups');
    const knownGroups = new Set(), renderers = new Set(), controllers = new Set();
    let selectedGroup = null, nextOffset = null, generation = 0, busy = false, listController, searchTimer, closed = false;
    let active = 0, queue = [];
    function pump() {
      while (active < 4 && queue.length) {
        const task = queue.shift();
        if (!task.element.isConnected || task.generation !== generation) continue;
        active++;
        const controller = new AbortController(); controllers.add(controller);
        U.api('/charts/' + encodeURIComponent(task.item.id) + '/data' + dataDate, {signal: controller.signal}).then(payload => {
          if (!task.element.isConnected || task.generation !== generation) return;
          const plot = K.render(task.element, payload, {definition: task.item.definition, thumbnail: true, width: 680, height: 250}); renderers.add(plot);
          U.noticeMarker(task.notice, [...task.warnings, ...(payload.notices || []), ...(payload.warnings || [])]);
        }).catch(error => {
          if (error.name !== 'AbortError' && task.element.isConnected && task.generation === generation) {
            task.element.textContent = error.message; task.element.classList.add('charts-notice', 'error');
            if ([403, 409].includes(error.status)) U.notice(status, error.message, 'error');
          }
        }).finally(() => { active--; controllers.delete(controller); if (!closed) pump(); });
      }
    }
    const observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
      for (const entry of entries) if (entry.isIntersecting) {
        observer.unobserve(entry.target);
        queue.push(entry.target._chartTask); pump();
      }
    }, {rootMargin: '180px'}) : null;
    function updateGroups() {
      groups.replaceChildren();
      for (const value of [null, ...[...knownGroups].sort((a, b) => a.localeCompare(b, 'zh-CN'))]) {
        const button = document.createElement('button'); button.type = 'button'; button.className = 'charts-button';
        button.textContent = value === null ? '全部分组' : value || '未分组'; button.dataset.group = value ?? '__all'; button.setAttribute('aria-pressed', String(value === selectedGroup));
        button.addEventListener('click', () => { selectedGroup = value; updateGroups(); load(true); }); groups.append(button);
      }
    }
    function addCard(item) {
      const def = item.definition, card = document.createElement('article'); card.className = 'charts-card';
      card.dataset.chartId = item.id;
      const link = U.pageURL('/sn/charts/' + encodeURIComponent(item.id)) + dataDate;
      card.innerHTML = `<h2><a href="${E(link)}">${E(def.title)}</a> <span class="charts-warning-marker" hidden></span></h2><p class="charts-muted">${E(def.group || '未分组')} · ${def.time.mode === 'cycle' ? '周期对比' : '走势'} · v${E(item.revision)}</p><a class="charts-thumbnail" href="${E(link)}" aria-label="${E('打开图表 ' + def.title)}">等待图表进入视野…</a><button type="button" class="favorites-star" data-favorite="${E(item.id)}" data-favorite-kind="chart" data-favorite-label="${E(def.title)}" aria-pressed="false" disabled>☆ 自选</button>`;
      const element = card.querySelector('.charts-thumbnail'), warning = card.querySelector('.charts-warning-marker');
      const warnings = [...(item.warnings || []), ...(item.curves || []).filter(c => c.status === 'missing').map(c => ({code: 'missing', label: c.label}))];
      U.noticeMarker(warning, warnings);
      element._chartTask = {element, notice: warning, warnings, item, generation}; cards.append(card);
      window.AccountFavorites?.syncButtons(card);
      if (observer) observer.observe(element); else { queue.push(element._chartTask); pump(); }
    }
    async function load(reset) {
      if (reset) {
        generation++; listController?.abort(); observer?.disconnect(); controllers.forEach(c => c.abort()); queue = [];
        renderers.forEach(chart => chart.destroy()); renderers.clear(); cards.replaceChildren(); nextOffset = 0;
      } else if (busy || nextOffset === null) return;
      const ownGeneration = generation, controller = new AbortController(); listController = controller;
      busy = true; more.disabled = true; U.notice(status, '正在读取图表列表…');
      const query = new URLSearchParams({q: $('chart-search').value.trim(), offset: nextOffset || 0, limit: 24});
      if (selectedGroup !== null) query.set('group', selectedGroup);
      try {
        const data = await U.api('/charts?' + query, {signal: controller.signal});
        if (ownGeneration !== generation) return;
        for (const item of data.items) { knownGroups.add(item.definition.group || ''); addCard(item); }
        updateGroups(); nextOffset = data.next_offset; more.hidden = nextOffset === null;
        $('chart-count').textContent = `已展示 ${cards.children.length} / ${data.total} 张`;
        U.notice(status, data.total ? '' : '没有匹配图表。可从原表导入，或在单指标页另存为图表。');
      } catch (error) {
        if (error.name !== 'AbortError' && ownGeneration === generation) { U.notice(status, error.message, 'error'); more.hidden = true; }
      } finally { if (ownGeneration === generation) { busy = false; more.disabled = false; } }
    }
    // The list API has no group catalogue. Read only definition metadata to index all groups.
    async function indexGroups() {
      let offset = 0;
      try {
        do {
          const data = await U.api('/charts?' + new URLSearchParams({offset, limit: 100}));
          if (closed) return;
          data.items.forEach(item => knownGroups.add(item.definition.group || '')); updateGroups();
          if (data.next_offset === null || data.next_offset <= offset) break;
          offset = data.next_offset;
        } while (!closed);
      } catch (error) { U.notice(status, error.message, 'error'); }
    }
    $('chart-search-form').addEventListener('submit', event => { event.preventDefault(); clearTimeout(searchTimer); load(true); });
    $('chart-search').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => load(true), 300); });
    more.addEventListener('click', () => load(false));
    window.addEventListener('pagehide', () => { closed = true; clearTimeout(searchTimer); listController?.abort(); controllers.forEach(c => c.abort()); observer?.disconnect(); renderers.forEach(c => c.destroy()); }, {once: true});
    load(true); indexGroups();
  }

  function detail() {
    const form = $('chart-editor'), fields = $('chart-editor-fields'), status = $('chart-detail-status'), plot = $('chart-plot');
    const path = '/charts/' + encodeURIComponent(config.id);
    let record, templates = [], chart, loading = false, revisionOffset = null, generation = 0, currentTemplate;
    const input = name => form.elements.namedItem(name);
    U.exports($('chart-exports'), () => chart, () => record?.definition.title);
    function timeFields() {
      const mode = input('mode').value;
      form.querySelectorAll('[data-time-field]').forEach(el => {
        const shown = el.dataset.timeField.split(' ').includes(mode); el.hidden = !shown;
        el.querySelectorAll('input,select').forEach(control => { control.disabled = !shown; });
      });
      input('start').required = ['since', 'range'].includes(mode); input('end').required = mode === 'range';
      $('chart-custom-start-field').hidden = input('template').value !== '__custom';
      input('cycle_start').disabled = mode !== 'cycle' || input('template').value !== '__custom';
      input('cycle_start').required = !input('cycle_start').disabled;
    }
    function setTemplateOptions(time) {
      currentTemplate = time.template;
      const selected = templates.find(item => {
        const a = {...item.template}, b = {...currentTemplate}; delete a.transform; delete b.transform;
        return JSON.stringify(a) === JSON.stringify(b);
      });
      U.fillTemplates(input('template'), templates, selected?.id);
      if (time.template && !selected) {
        const option = document.createElement('option'); option.value = '__current'; option.textContent = '当前定义 · ' + time.template.name;
        input('template').append(option); input('template').value = '__current';
      }
      const option = document.createElement('option'); option.value = '__custom'; option.textContent = '自定义年度起点（MM-DD）'; input('template').append(option);
      if (!time.template && templates.length) input('template').value = templates[0].id;
      input('cycle_start').value = time.template?.start || '';
    }
    function populate() {
      const def = record.definition, time = def.time;
      $('chart-title').textContent = def.title; $('chart-meta').textContent = `${def.group || '未分组'} · 修订 v${record.revision}`;
      const favorite = page.querySelector('[data-favorite-kind="chart"]');
      if (favorite) { favorite.dataset.favoriteLabel = def.title; window.AccountFavorites?.syncButtons(page); }
      $('chart-note').textContent = def.note;
      for (const name of ['title', 'group', 'note']) input(name).value = def[name] || '';
      input('mode').value = time.mode; input('points').value = time.points || 120; input('start').value = time.start || ''; input('end').value = time.end || '';
      input('cycles').value = time.cycles || 8; input('band_cycles').value = time.band_cycles || 5; input('transform').value = time.transform || 'raw';
      setTemplateOptions(time);
      $('chart-curve-fields').innerHTML = def.curves.map((curve, index) => `<div class="charts-curve" data-curve-index="${index}"><div><strong>${E(curve.label)}</strong><p class="charts-muted">${E(curve.series_id)}</p></div><label class="charts-field">图型<select data-curve-kind><option value="line">线</option><option value="bar">柱</option><option value="area">面积</option></select></label><label class="charts-field">纵轴<select data-curve-axis><option value="left">左轴</option><option value="right">右轴</option></select></label><label class="charts-field">颜色（留空用调色板）<input data-curve-color maxlength="7" placeholder="#RRGGBB" pattern="#[0-9a-fA-F]{6}"></label></div>`).join('');
      $('chart-curve-fields').querySelectorAll('[data-curve-index]').forEach((row, index) => {
        row.querySelector('[data-curve-kind]').value = def.curves[index].kind;
        row.querySelector('[data-curve-axis]').value = def.curves[index].axis;
        row.querySelector('[data-curve-color]').value = def.curves[index].style?.color || '';
      });
      timeFields(); fields.disabled = !config.can_write;
    }
    async function load() {
      const own = ++generation;
      fields.disabled = true; loading = true; $('chart-conflict').hidden = true; U.notice(status, '正在读取图表…');
      chart?.destroy(); chart = null; plot.textContent = '正在读取数据…';
      $('chart-statistics').hidden = true;
      try {
        const results = await Promise.allSettled([U.api(path), U.api('/cycle-templates')]);
        if (own !== generation) return;
        if (results[0].status === 'rejected') throw results[0].reason;
        record = results[0].value;
        templates = results[1].status === 'fulfilled' ? results[1].value.items : [];
        populate();
        U.notice(status, results[1].status === 'rejected' ? '周期模板未能加载：' + results[1].reason.message : '', results[1].status === 'rejected' ? 'warning' : '');
        loadRevisions(true);
        try {
          const payload = await U.api(path + '/data' + dataDate);
          if (own !== generation) return;
          if (payload.revision !== undefined && payload.revision !== record.revision) {
            $('chart-conflict').hidden = false; fields.disabled = true; throw new Error('图表定义和数据版本已变化，请重新加载最新版本');
          }
          chart = K.render(plot, payload, {definition: record.definition, title: record.definition.title, height: 500});
          U.stats($('chart-statistics'), payload, {showWarnings: false});
          U.notices($('chart-notices'), [...(record.warnings || []), ...(payload.notices || []), ...(payload.warnings || [])], 'subtle');
        } catch (error) { plot.textContent = error.message; U.notice(status, error.message, 'error'); }
      } catch (error) { record = null; plot.textContent = error.message; U.notice(status, error.message, 'error'); }
      finally { if (own === generation) loading = false; }
    }
    async function loadRevisions(reset) {
      if (reset) { revisionOffset = 0; $('chart-revisions').replaceChildren(); }
      if (revisionOffset === null) return;
      const own = generation, more = $('chart-revisions-more'); more.disabled = true;
      try {
        const result = await U.api(path + '/revisions?' + new URLSearchParams({offset: revisionOffset, limit: 20}));
        if (own !== generation) return;
        for (const item of result.items) {
          const details = document.createElement('details'), summary = document.createElement('summary'), pre = document.createElement('pre');
          summary.textContent = `v${item.revision} · ${item.definition.title} · ${item.recorded_at || ''} · ${item.edited_by || ''}${item.status === 'deleted' ? ' · 已删除' : ''}`;
          pre.textContent = JSON.stringify(item.definition, null, 2); details.append(summary, pre); $('chart-revisions').append(details);
        }
        revisionOffset = result.next_offset; more.hidden = revisionOffset === null; U.notice($('chart-revision-status'), '');
      } catch (error) { U.notice($('chart-revision-status'), '修订历史读取失败：' + error.message, 'error'); }
      finally { more.disabled = false; }
    }
    function editedDefinition() {
      const mode = input('mode').value;
      let time;
      if (mode === 'recent') time = {mode, points: Number(input('points').value)};
      if (mode === 'since') time = {mode, start: input('start').value};
      if (mode === 'range') time = {mode, start: input('start').value, end: input('end').value};
      if (mode === 'cycle') {
        if (record.definition.curves.length !== 1) throw new Error('周期模式一期只支持一条指标曲线，请保留走势模式');
        let template;
        if (input('template').value === '__custom') {
          const start = input('cycle_start').value.trim(); if (!U.validMonthDay(start)) throw new Error('请填写有效的周期起点 MM-DD');
          template = {kind: 'repeat', name: '自定义年度周期', period: 'year', start, align: 'days'};
        } else template = input('template').value === '__current' ? currentTemplate : templates.find(t => t.id === input('template').value)?.template;
        if (!template) throw new Error('请选择周期模板');
        time = {mode, template: {...template, transform: input('transform').value}, cycles: Number(input('cycles').value), band_cycles: Number(input('band_cycles').value), transform: input('transform').value,
          segment_styles: record.definition.time.segment_styles || []};
      }
      const curves = [...$('chart-curve-fields').querySelectorAll('[data-curve-index]')].map((row, index) => ({...record.definition.curves[index],
        kind: row.querySelector('[data-curve-kind]').value, axis: row.querySelector('[data-curve-axis]').value,
        style: {...record.definition.curves[index].style, color: row.querySelector('[data-curve-color]').value.trim() || null}}));
      return {revision: record.revision, title: input('title').value.trim(), group: input('group').value.trim(), note: input('note').value, time, curves};
    }
    function conflict(error) {
      U.notice(status, error.message, 'error');
      if (error.status === 409) { $('chart-conflict').hidden = false; fields.disabled = true; }
    }
    form.addEventListener('submit', async event => {
      event.preventDefault(); if (!record || !config.can_write || loading) return;
      let body;
      try { body = editedDefinition(); } catch (error) { U.notice(status, error.message, 'error'); return; }
      loading = true; fields.disabled = true;
      try { await U.api(path, {method: 'PATCH', body: JSON.stringify(body)}); await load(); if ($('chart-conflict').hidden && status.hidden) U.notice(status, '已保存图表定义'); }
      catch (error) { fields.disabled = !config.can_write; conflict(error); }
      finally { loading = false; }
    });
    $('chart-delete').addEventListener('click', async () => {
      if (!record || !config.can_write || loading || !window.confirm(`确认删除图表“${record.definition.title}”？`)) return;
      loading = true; fields.disabled = true;
      try { await U.api(path + '?revision=' + record.revision, {method: 'DELETE'}); window.location.assign(U.pageURL('/sn/indicators') + '?tab=charts' + (config.date ? '&date=' + encodeURIComponent(config.date) : '')); }
      catch (error) { fields.disabled = !config.can_write; conflict(error); }
      finally { loading = false; }
    });
    input('mode').addEventListener('change', timeFields); input('template').addEventListener('change', timeFields);
    $('chart-reload').addEventListener('click', load); $('chart-revisions-more').addEventListener('click', () => loadRevisions(false));
    window.addEventListener('pagehide', () => { generation++; chart?.destroy(); }, {once: true});
    load();
  }
})();

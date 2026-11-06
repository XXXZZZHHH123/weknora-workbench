/* Personal PPT templates. Candidate recall never changes bindings without a click. */
(function (root) {
  'use strict';
  const EMU_PER_PIXEL = 9525;
  const esc = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[char]));
  const types = {chart: '图', table: '数据小表', image: '图片'};
  const reasons = {native_chart: '原生图表暂不支持替换', svg: 'SVG 图片暂不支持替换', missing_media: '原件媒体缺失',
    unbound: '尚未绑定', keep: '已选择保留原图', chart_missing: '绑定图表已删除或不可访问', chart_invalid: '绑定图表定义无效',
    series_missing: '绑定指标已不在当前来源目录', chart_unit_mismatch: '图表纵轴单位不一致', source_unavailable: '来源暂不可读',
    data_read_failed: '数据读取或图片生成失败', png_not_supplied: '未提供新图片'};
  const reason = code => reasons[code] || code || '';
  function emuToPx(value) {
    if (typeof value !== 'number' || !Number.isFinite(value)) throw new Error('图位 EMU 坐标无效');
    return value / EMU_PER_PIXEL;
  }
  function slotSize(slot) {
    const width = emuToPx(slot.position?.cx), height = emuToPx(slot.position?.cy);
    if (width <= 0 || height <= 0) throw new Error('图位宽高必须大于零');
    return {width, height};
  }
  function slideLayout(presentation, maxWidth, maxHeight = Infinity) {
    const width = emuToPx(presentation.width_emu), height = emuToPx(presentation.height_emu);
    if (width <= 0 || height <= 0 || !(maxWidth > 0) || !(maxHeight > 0)) throw new Error('幻灯片尺寸无效');
    const scale = Math.min(1, maxWidth / width, maxHeight / height);
    return {width: width * scale, height: height * scale, scale};
  }
  function slotRect(slot, presentation) {
    const p = slot.page_position || slot.position;
    if (!p || ![p.x, p.y, p.cx, p.cy].every(Number.isFinite) || presentation.width_emu <= 0 || presentation.height_emu <= 0) throw new Error('图位版面坐标无效');
    return {left: p.x / presentation.width_emu * 100, top: p.y / presentation.height_emu * 100,
      width: Math.max(0, p.cx) / presentation.width_emu * 100, height: Math.max(0, p.cy) / presentation.height_emu * 100};
  }
  function allSlots(record) { return record.presentation.slides.flatMap(slide => slide.slots); }
  function slotState(slot, binding) {
    return !slot.replaceable || binding?.kind === 'keep' ? 'kept' : binding ? 'bound' : 'pending';
  }
  function counts(record, bindings = record.bindings || {}) {
    const result = {bound: 0, kept: 0, pending: 0};
    allSlots(record).forEach(slot => result[slotState(slot, bindings[slot.slot_id])]++);
    return result;
  }
  const summary = value => `已绑定 ${value.bound} / 保留原图 ${value.kept} / 待确认 ${value.pending}`;
  function candidateBinding(candidate) {
    if (candidate?.kind === 'chart') return {kind: 'chart', chart_id: candidate.chart_id};
    if (candidate?.kind === 'series') return {kind: 'series', series_id: candidate.series_id, rows: candidate.rows};
    return null;
  }
  function firstCandidates(slide, bindings, candidates) {
    return slide.slots.filter(slot => slot.replaceable && !bindings[slot.slot_id] && candidates[slot.slot_id]?.length)
      .map(slot => ({slot_id: slot.slot_id, binding: candidateBinding(candidates[slot.slot_id][0])})).filter(change => change.binding);
  }
  async function mapLimit(items, limit, worker) {
    if (!Number.isInteger(limit) || limit < 1) throw new Error('并发数无效');
    const result = new Array(items.length); let next = 0;
    await Promise.all(Array.from({length: Math.min(items.length, limit, 4)}, async () => {
      while (next < items.length) {
        const index = next++;
        try { result[index] = {status: 'fulfilled', value: await worker(items[index], index)}; }
        catch (error) { result[index] = {status: 'rejected', reason: error}; }
      }
    }));
    return result;
  }
  const pageURL = path => (root.RESEARCH_PREFIX || '') + path;
  const apiURL = path => pageURL('/api/sn' + path);
  function responseError(body, status) {
    const detail = body?.detail;
    const message = typeof detail === 'string' ? detail : Array.isArray(detail) ? detail.map(item => item.msg || '输入无效').join('；') : '请求失败，请稍后重试';
    return Object.assign(new Error(message), {status});
  }
  async function api(path, options = {}) {
    const headers = new root.Headers(options.headers || {}), init = {...options, headers};
    if (init.body && typeof init.body !== 'string' && !(init.body instanceof root.FormData)) {
      headers.set('Content-Type', 'application/json'); init.body = JSON.stringify(init.body);
    }
    const fetcher = typeof root.researchFetch === 'function' ? root.researchFetch : root.fetch.bind(root);
    const response = await fetcher(apiURL(path), init), body = await response.json().catch(() => ({}));
    if (!response.ok) throw responseError(body, response.status);
    return body;
  }
  function uploadFile(file, progress, signal) {
    return new Promise((resolve, reject) => {
      const xhr = new root.XMLHttpRequest(), form = new root.FormData(); form.append('file', file);
      xhr.open('POST', apiURL('/ppt-templates'));
      // Match researchFetch's verified account headers while retaining byte progress.
      if (root.RESEARCH_CONTEXT) {
        xhr.setRequestHeader('X-Tenant-ID', String(root.RESEARCH_CONTEXT.tenant));
        xhr.setRequestHeader('X-Research-User', root.RESEARCH_CONTEXT.user);
      }
      const abort = () => xhr.abort();
      if (signal?.aborted) { reject(new DOMException('上传已取消', 'AbortError')); return; }
      signal?.addEventListener('abort', abort, {once: true});
      xhr.upload.addEventListener('progress', event => progress('upload', event.lengthComputable ? event.loaded / event.total * 100 : null));
      xhr.upload.addEventListener('load', () => progress('parse', null));
      xhr.addEventListener('loadend', () => signal?.removeEventListener('abort', abort));
      xhr.addEventListener('load', () => {
        let body = {}; try { body = JSON.parse(xhr.responseText); } catch (_) { /* HTTP error below */ }
        if (xhr.status >= 200 && xhr.status < 300) resolve(body);
        else {
          if (xhr.status === 401 && root.RESEARCH_CONTEXT) root.parent.postMessage({type: 'research-auth-expired'}, root.location.origin);
          reject(responseError(body, xhr.status));
        }
      });
      xhr.addEventListener('error', () => reject(new Error('上传连接失败，请重试')));
      xhr.addEventListener('abort', () => reject(new DOMException('上传已取消', 'AbortError')));
      progress('upload', 0); xhr.send(form);
    });
  }
  function notice(element, text, kind = '') {
    element.textContent = text || ''; element.hidden = !text; element.className = 'charts-notice' + (kind ? ' ' + kind : '');
  }
  function today() {
    const date = new Date(); return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
  }
  const helpers = {emuToPx, slotSize, slideLayout, slotRect, allSlots, slotState, counts, summary, candidateBinding, firstCandidates, mapLimit, pageURL, apiURL, api, uploadFile, responseError, notice, esc, reason, types, today};
  if (typeof module !== 'undefined' && module.exports) module.exports = helpers;
  root.PptUI = Object.freeze(helpers);
  if (!root.document) return;
  const page = root.document.querySelector('[data-ppt-page]'), configNode = root.document.getElementById('ppt-page-config');
  if (!page || !configNode) return;
  const config = JSON.parse(configNode.textContent), $ = id => root.document.getElementById(id), E = esc;
  if (!config.enabled) return;
  if (page.dataset.pptPage === 'list') library();
  if (page.dataset.pptPage === 'detail') detail();

  function library() {
    const status = $('ppt-list-status'), rows = $('ppt-list-rows'), more = $('ppt-list-more'), fields = $('ppt-upload-fields');
    const controller = new AbortController(); let offset = 0, busy = false, closed = false, generation = 0, uploading = false, reloadQueued = false;
    const records = new Map(); fields.disabled = !config.can_write;
    async function load(reset = false) {
      if (busy) { if (reset) reloadQueued = true; return; }
      if (reset) { generation++; records.clear(); rows.replaceChildren(); offset = 0; $('ppt-list-conflict').hidden = true; }
      const own = generation; busy = true; more.disabled = true; notice(status, '正在读取模板…');
      try {
        const data = await api('/ppt-templates?' + new URLSearchParams({offset, limit: 12}), {signal: controller.signal});
        for (const item of data.items) {
          records.set(item.id, item);
          const tr = root.document.createElement('tr'); tr.dataset.templateId = item.id;
          tr.innerHTML = `<td><a href="${E(pageURL('/sn/ppt/' + encodeURIComponent(item.id)))}">${E(item.title || item.filename)}</a></td><td>${E(item.slide_count)}</td><td data-ppt-counts>正在读取图位统计…</td><td data-ppt-updated>${E(item.updated_at || '—')}</td><td><button type="button" class="charts-button danger" data-ppt-delete ${config.can_write ? '' : 'disabled'}>删除</button></td>`;
          rows.append(tr);
        }
        offset = data.next_offset; more.hidden = offset === null;
        notice(status, data.total ? `共 ${data.total} 个模板` : '还没有模板，请上传以往的 PPTX 周报。');
        // The list has no binding counts; use existing owner-checked detail reads.
        await mapLimit(data.items, 4, async item => {
          const tr = [...rows.children].find(row => row.dataset.templateId === item.id);
          try {
            const record = await api('/ppt-templates/' + encodeURIComponent(item.id), {signal: controller.signal});
            if (closed || own !== generation || !tr?.isConnected) return;
            records.set(item.id, record); tr.querySelector('[data-ppt-counts]').textContent = summary(counts(record));
            tr.querySelector('[data-ppt-updated]').textContent = record.updated_at || '—';
          } catch (error) { if (!closed && tr?.isConnected) tr.querySelector('[data-ppt-counts]').textContent = '统计暂不可用：' + error.message; }
        });
      } catch (error) { if (error.name !== 'AbortError') notice(status, error.message, 'error'); }
      finally {
        busy = false; more.disabled = false;
        if (reloadQueued && !closed) { reloadQueued = false; load(true); }
      }
    }
    rows.addEventListener('click', async event => {
      const button = event.target.closest('[data-ppt-delete]'); if (!button || !config.can_write) return;
      const tr = button.closest('tr'), record = records.get(tr.dataset.templateId);
      if (!root.confirm(`删除模板“${record.title || record.filename}”？`)) return;
      button.disabled = true;
      try {
        await api('/ppt-templates/' + encodeURIComponent(record.id) + '?revision=' + record.revision, {method: 'DELETE', signal: controller.signal});
        tr.remove(); records.delete(record.id); notice(status, '模板已删除');
      } catch (error) {
        if (error.status === 409) $('ppt-list-conflict').hidden = false;
        notice(status, error.message, 'error'); button.disabled = error.status === 409;
      }
    });
    $('ppt-upload-form').addEventListener('submit', async event => {
      event.preventDefault(); if (!config.can_write || uploading) return;
      const file = $('ppt-file').files[0], output = $('ppt-upload-status'), bar = $('ppt-upload-bar'); $('ppt-upload-progress').hidden = false;
      if (!file || !/\.pptx$/i.test(file.name) || file.size > 60 * 1024 * 1024) { notice(output, '请选择不超过 60 MB 的 .pptx 文件', 'error'); return; }
      fields.disabled = true; uploading = true;
      try {
        const record = await uploadFile(file, (phase, percent) => {
          if (percent === null || phase === 'parse') bar.removeAttribute('value'); else bar.value = percent;
          notice(output, phase === 'parse' ? '上传完成，正在解析幻灯片与图位…' : percent === null ? '正在上传…' : `正在上传 ${Math.floor(percent)}%…`);
        }, controller.signal);
        bar.value = 100; output.replaceChildren(); output.hidden = false;
        const link = root.document.createElement('a'); link.className = 'ppt-report-link'; link.href = pageURL('/sn/ppt/' + encodeURIComponent(record.id));
        link.textContent = `解析完成：${record.slide_count} 页、${record.slot_count} 个图位，点击确认绑定`; output.append(link);
        $('ppt-upload-form').reset();
        await load(true);
      } catch (error) { if (error.name !== 'AbortError') { bar.value = 0; notice(output, '上传或解析失败：' + error.message, 'error'); } }
      finally { uploading = false; fields.disabled = !config.can_write; }
    });
    more.addEventListener('click', () => load()); $('ppt-list-reload').addEventListener('click', () => load(true));
    root.addEventListener('pagehide', () => { closed = true; controller.abort(); }, {once: true});
    load();
  }

  function detail() {
    const path = '/ppt-templates/' + encodeURIComponent(config.id), status = $('ppt-detail-status'), inspector = $('ppt-inspector');
    const controller = new AbortController(), labels = new Map(), pending = new Map();
    let record, bindings = {}, selected, loading = false, saving = false, conflict = false, inspectorController, resizeObserver, plots = [];
    const stateText = {bound: '已绑定', kept: '保留原图', pending: '待确认'};
    const labelKey = binding => binding.kind === 'chart' ? 'chart:' + binding.chart_id : 'series:' + binding.series_id;
    const bindingText = binding => !binding ? '尚未绑定' : binding.kind === 'keep' ? '保留原图' :
      (labels.get(labelKey(binding)) || (binding.kind === 'chart' ? '图表 ' + binding.chart_id : '指标 ' + binding.series_id)) + (binding.kind === 'series' ? ` · ${binding.rows} 行` : '');
    function editable() { return config.can_write && record && !loading && !saving && !conflict; }
    function controls() {
      $('ppt-save').disabled = !editable() || !pending.size;
      root.document.querySelectorAll('[data-ppt-edit]').forEach(button => { button.disabled = !editable(); });
      inspector.querySelectorAll('[data-ppt-replace]').forEach(fieldset => { fieldset.disabled = !editable() || !selected?.replaceable; });
    }
    function refresh() {
      $('ppt-summary').textContent = summary(counts(record, bindings));
      $('ppt-dirty').textContent = pending.size ? `${pending.size} 个图位有未保存选择` : '所有绑定已保存';
      $('ppt-meta').textContent = `${record.slide_count} 页 · ${record.slot_count} 个图位 · 修订 v${record.revision}`;
      root.document.querySelectorAll('[data-slot-id]').forEach(button => {
        const slot = allSlots(record).find(slot => slot.slot_id === button.dataset.slotId), state = slotState(slot, bindings[slot.slot_id]);
        button.dataset.state = state; button.setAttribute('aria-pressed', String(selected?.slot_id === slot.slot_id));
        button.querySelector('.ppt-slot-state').textContent = stateText[state];
      });
      if (selected && $('ppt-current-binding')) $('ppt-current-binding').textContent = '当前选择：' + bindingText(bindings[selected.slot_id]);
      controls();
    }
    function change(slot, binding) {
      if (!editable() || (!slot.replaceable && binding && binding.kind !== 'keep')) return;
      bindings[slot.slot_id] = binding;
      if (selected?.slot_id === slot.slot_id && binding?.kind === 'series' && $('ppt-series-rows')) $('ppt-series-rows').value = binding.rows;
      if (JSON.stringify(record.bindings[slot.slot_id] || null) === JSON.stringify(binding)) pending.delete(slot.slot_id);
      else pending.set(slot.slot_id, {slot_id: slot.slot_id, binding});
      refresh();
    }
    function drawSlides() {
      resizeObserver?.disconnect(); $('ppt-slides').replaceChildren();
      for (const slide of record.presentation.slides) {
        const section = root.document.createElement('article'); section.className = 'charts-panel ppt-slide'; section.dataset.slide = slide.number;
        section.innerHTML = `<div class="charts-toolbar"><h2>第 ${E(slide.number)} 页 · ${E(slide.title || '无标题')}</h2><button class="charts-button" type="button" data-ppt-edit>按候选确认本页</button></div><div class="ppt-board-wrap"><div class="ppt-board" aria-label="第 ${E(slide.number)} 页版面示意"></div></div><div class="ppt-slot-list" aria-label="本页图位列表"></div>`;
        const board = section.querySelector('.ppt-board'), list = section.querySelector('.ppt-slot-list');
        slide.slots.forEach((slot, index) => {
          function button(onBoard) {
            const el = root.document.createElement('button'); el.type = 'button'; el.dataset.slotId = slot.slot_id;
            el.className = onBoard ? 'ppt-slot ppt-kind-' + (Object.hasOwn(types, slot.kind) ? slot.kind : 'image') : 'charts-button';
            el.innerHTML = `<span>${index + 1} · ${E(types[slot.kind] || '图片')}</span><span class="ppt-slot-state"></span>`;
            el.title = slot.name || slot.slot_id; el.setAttribute('aria-label', `第 ${slide.number} 页图位 ${index + 1} · ${types[slot.kind] || '图片'}`);
            el.addEventListener('click', () => { selected = slot; renderInspector(); refresh(); }); return el;
          }
          const shape = button(true);
          try { Object.entries(slotRect(slot, record.presentation)).forEach(([key, value]) => { shape.style[key] = value + '%'; }); board.append(shape); }
          catch (error) { notice(status, error.message + '，可从图位列表选择。', 'warning'); }
          list.append(button(false));
        });
        section.querySelector('[data-ppt-edit]').addEventListener('click', () => {
          if (!editable()) return;
          const changes = firstCandidates(slide, bindings, record.candidates || {});
          changes.forEach(item => change(slide.slots.find(slot => slot.slot_id === item.slot_id), item.binding));
          notice(status, changes.length ? `已按首位候选选择本页 ${changes.length} 个图位，请保存绑定。` : '本页没有尚未绑定且有候选的可替换图位。');
        });
        $('ppt-slides').append(section);
        const size = () => {
          try { const layout = slideLayout(record.presentation, Math.max(1, board.parentElement.clientWidth - 2)); board.style.width = layout.width + 'px'; board.style.height = layout.height + 'px'; }
          catch (error) { board.textContent = error.message; }
        };
        board._resize = size; size();
      }
      if (root.ResizeObserver) {
        resizeObserver = new root.ResizeObserver(entries => entries.forEach(entry => entry.target.querySelector('.ppt-board')._resize()));
        $('ppt-slides').querySelectorAll('.ppt-board-wrap').forEach(element => resizeObserver.observe(element));
      }
    }
    function clearInspector() { inspectorController?.abort(); plots.forEach(plot => plot.destroy()); plots = []; }
    function renderInspector() {
      clearInspector(); inspectorController = new AbortController();
      const signal = inspectorController.signal, slot = selected, candidates = record.candidates?.[slot.slot_id] || [];
      const texts = (slot.texts || []).slice(0, 20);
      inspector.innerHTML = `<h2>第 ${E(slot.slide)} 页 · ${E(types[slot.kind] || '图片')}</h2><p class="charts-muted">${E(slot.name || slot.slot_id)}</p><p id="ppt-current-binding"></p><p id="ppt-binding-status" role="status" hidden></p>
        ${!slot.replaceable ? `<p class="charts-notice warning">${E(reason(slot.preserve_reason) || '此图位仅支持保留原图')}</p>` : ''}
        <details class="ppt-texts"><summary>识别到的文字（${E((slot.texts || []).length)} 段，最多显示 20 段）</summary>${texts.length ? '<ol>' + texts.map(text => `<li>${E(text)}</li>`).join('') + '</ol>' : '<p class="charts-muted">未识别到文字</p>'}</details>
        <fieldset data-ppt-replace disabled><h3>候选</h3><p class="charts-muted">候选只供核对，点击绑定或按候选确认本页后生效。</p><div id="ppt-candidates"></div>
        <form id="ppt-chart-search"><label class="charts-field">搜索图表库其他图表<input type="search" maxlength="200" name="q" placeholder="图表标题"></label><button type="submit" class="charts-button">搜索图表</button></form><div id="ppt-chart-results"></div><button type="button" id="ppt-chart-more" class="charts-button" hidden>更多图表</button>
        <form id="ppt-series-search"><label class="charts-field">选择指标<input type="search" maxlength="200" name="q" placeholder="指标名称"></label><label class="charts-field">显示行数<input id="ppt-series-rows" type="number" min="1" max="60" step="1" required></label><p class="charts-footnote">已绑定数据小表时，调整行数后保存即可生效。</p><button type="submit" class="charts-button">搜索指标</button></form><div id="ppt-series-results"></div><button type="button" id="ppt-series-more" class="charts-button" hidden>更多指标</button></fieldset>
        <div class="charts-toolbar"><button type="button" id="ppt-keep" class="charts-button" data-ppt-edit>保留原图</button><button type="button" id="ppt-clear" class="charts-button" data-ppt-edit>清除绑定</button></div>`;
      $('ppt-series-rows').value = bindings[slot.slot_id]?.rows || Math.min(60, slot.pairs?.length || 10);
      $('ppt-series-rows').addEventListener('change', event => {
        const binding = bindings[slot.slot_id];
        if (binding?.kind === 'series' && event.target.reportValidity()) change(slot, {...binding, rows: Number(event.target.value)});
      });
      $('ppt-keep').addEventListener('click', () => change(slot, {kind: 'keep'}));
      $('ppt-clear').addEventListener('click', () => change(slot, null));
      for (const candidate of candidates) {
        const node = root.document.createElement('article'); node.className = 'ppt-candidate';
        if (candidate.kind === 'chart') {
          labels.set(labelKey(candidate), candidate.title);
          node.innerHTML = `<p><strong>${E(candidate.title)}</strong></p><p class="charts-muted">相似度分数：${E(candidate.score)}</p><div class="charts-thumbnail">正在读取缩略图…</div>`;
        } else {
          labels.set(labelKey(candidate), candidate.name);
          node.innerHTML = `<p><strong>${E(candidate.name)}</strong> ${candidate.numerically_verified ? '<span class="ppt-verified">数值已核实</span>' : ''}</p><p class="charts-muted">一致比例 ${E(candidate.matched)}/${E(candidate.total)}（${E((candidate.score * 100).toFixed(1))}%） · ${E(candidate.rows)} 行${candidate.observation_window_limited ? ' · 核实观测窗口已达上限' : ''}</p>`;
        }
        const bind = root.document.createElement('button'); bind.type = 'button'; bind.className = 'charts-button'; bind.textContent = '绑定此候选'; bind.dataset.candidateKind = candidate.kind;
        bind.addEventListener('click', () => change(slot, candidateBinding(candidate))); node.append(bind); $('ppt-candidates').append(node);
        node._candidate = candidate;
      }
      if (!candidates.length) $('ppt-candidates').textContent = '暂无候选，可搜索图表或指标。';
      mapLimit([...$('ppt-candidates').children].filter(node => node._candidate?.kind === 'chart'), 4, async node => {
        const element = node.querySelector('.charts-thumbnail'), candidate = node._candidate;
        try {
          const chart = await api('/charts/' + encodeURIComponent(candidate.chart_id), {signal});
          const data = await api('/charts/' + encodeURIComponent(candidate.chart_id) + '/data', {signal});
          if (!signal.aborted && element.isConnected) plots.push(root.ChartKit.render(element, data, {definition: chart.definition, thumbnail: true, width: 640, height: 230}));
        } catch (error) { if (!signal.aborted) element.textContent = '缩略图暂不可用：' + error.message; }
      });
      for (const kind of ['chart', 'series']) {
        const form = $('ppt-' + kind + '-search'), results = $('ppt-' + kind + '-results'), more = $('ppt-' + kind + '-more');
        let next = null, searchController, query = '', version = 0;
        async function search(reset) {
          if (!editable() || !slot.replaceable) return;
          if (reset) { next = 0; query = form.elements.namedItem('q').value.trim(); version++; searchController?.abort(); results.replaceChildren(); }
          if (next === null) return;
          const own = version, local = new AbortController(); searchController = local;
          const abort = () => local.abort(); signal.addEventListener('abort', abort, {once: true}); more.disabled = true;
          notice($('ppt-binding-status'), '正在搜索…');
          try {
            const params = new URLSearchParams({offset: next, limit: 20});
            params.set(kind === 'chart' ? 'q' : 'search', query); if (kind === 'series') params.set('format', 'metadata');
            const data = await api((kind === 'chart' ? '/charts?' : '/indicators/catalog?') + params, {signal: local.signal});
            if (own !== version || signal.aborted) return;
            const items = kind === 'chart' ? data.items : data.indicators;
            for (const item of items) {
              const binding = kind === 'chart' ? {kind: 'chart', chart_id: item.id} : {kind: 'series', series_id: item.series_id};
              const title = kind === 'chart' ? item.definition.title : item.name;
              labels.set(labelKey(binding), title);
              const row = root.document.createElement('div'); row.className = 'ppt-search-result';
              const name = root.document.createElement('p'); name.textContent = title + (kind === 'series' ? ' · ' + (item.unit || '单位未注明') : '');
              const button = root.document.createElement('button'); button.type = 'button'; button.className = 'charts-button'; button.textContent = kind === 'chart' ? '绑定此图表' : '绑定此指标';
              button.addEventListener('click', () => {
                if (kind === 'series') {
                  const input = $('ppt-series-rows'); if (!input.reportValidity()) return;
                  change(slot, {...binding, rows: Number(input.value)});
                } else change(slot, binding);
              }); row.append(name, button); results.append(row);
            }
            next = data.next_offset; more.hidden = next === null; notice($('ppt-binding-status'), items.length ? '' : '没有匹配结果。'); refresh();
          } catch (error) { if (error.name !== 'AbortError') notice($('ppt-binding-status'), error.message, 'error'); }
          finally { signal.removeEventListener('abort', abort); if (own === version) more.disabled = false; }
        }
        form.addEventListener('submit', event => { event.preventDefault(); search(true); }); more.addEventListener('click', () => search(false));
      }
      const binding = bindings[slot.slot_id];
      if (binding && binding.kind !== 'keep' && !labels.has(labelKey(binding))) {
        (async () => {
          try {
            const data = binding.kind === 'chart' ? await api('/charts/' + encodeURIComponent(binding.chart_id), {signal}) :
              await api('/indicators/catalog?' + new URLSearchParams({format: 'metadata', series_id: binding.series_id, limit: 1}), {signal});
            const name = binding.kind === 'chart' ? data.definition.title : data.indicators[0]?.name;
            if (!signal.aborted && name) { labels.set(labelKey(binding), name); refresh(); }
          } catch (error) { if (!signal.aborted) notice($('ppt-binding-status'), '当前绑定暂不可读：' + error.message, 'warning'); }
        })();
      }
      refresh();
    }
    async function load() {
      if (loading || saving) return; loading = true; controls(); notice(status, '正在读取模板与候选…');
      try {
        const next = await api(path, {signal: controller.signal}); clearInspector();
        record = next; bindings = structuredClone(record.bindings); pending.clear(); conflict = false; selected = undefined;
        $('ppt-conflict').hidden = true; $('ppt-title').textContent = record.title || record.filename;
        Object.values(record.candidates || {}).flat().forEach(candidate => labels.set(labelKey(candidate), candidate.title || candidate.name));
        inspector.innerHTML = '<p class="charts-muted">点击左侧图位开始确认。</p>'; drawSlides(); refresh();
        notice(status, record.warnings?.length ? '部分图表定义无效，候选可能不完整；请核对绑定。' : '');
      } catch (error) { if (error.name !== 'AbortError') notice(status, error.message, 'error'); }
      finally { loading = false; controls(); }
    }
    $('ppt-save').addEventListener('click', async () => {
      if (!editable() || !pending.size) return; saving = true; controls(); notice(status, '正在保存绑定…');
      try {
        const saved = await api(path + '/bindings', {method: 'PATCH', body: {revision: record.revision, slots: [...pending.values()]}, signal: controller.signal});
        record = {...saved, candidates: record.candidates}; bindings = structuredClone(saved.bindings); pending.clear(); refresh(); notice(status, '已保存图位绑定');
      } catch (error) {
        if (error.status === 409) { conflict = true; $('ppt-conflict').hidden = false; }
        if (error.name !== 'AbortError') notice(status, error.message, 'error');
      } finally { saving = false; controls(); }
    });
    $('ppt-reload').addEventListener('click', load);
    $('ppt-export-link').addEventListener('click', event => { if (pending.size && !root.confirm('还有未保存的绑定选择，离开后将丢失。继续导出已保存版本？')) event.preventDefault(); });
    root.addEventListener('pagehide', () => { controller.abort(); clearInspector(); resizeObserver?.disconnect(); }, {once: true});
    load();
  }
})(typeof window !== 'undefined' ? window : globalThis);

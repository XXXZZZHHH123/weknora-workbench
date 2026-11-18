/* Submit one report setup job and reopen its checklist from the affairs centre. */
(function () {
  'use strict';
  const get = key => document.getElementById('workbench-setup-' + key);
  const dialog = get('dialog');
  if (!dialog) return;

  const prefix = window.RESEARCH_PREFIX || '', scope = window.RESEARCH_CONTEXT;
  const jobs = '/workbench-setup/jobs';
  const form = get('form'), report = get('report'), model = get('model'), search = get('search');
  const parts = [...form.querySelectorAll('input[name="part"]')];
  const guide = get('guide');
  const jobId = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value) ? value : '';
  const active = new Set(['queued', 'running']);
  const phaseLabels = {
    queued: '正在等待后台任务', reading_source: '正在读取周报', dimensions: '正在归纳维度',
    indicators: '正在建议指标', charts: '正在识别图表', layout: '正在准备周报版式',
    focus: '正在生成核心关注建议', diagram: '正在生成产业图骨架', judgment: '正在生成研判草稿',
    strategy: '正在生成策略草稿', monitors: '正在生成监测候选', validating: '正在核对任务范围',
  };
  const reviewPages = {
    dimensions: ['研究维度', '/sn/dimensions'], indicators: ['维度绑定指标', '/sn/dimensions'],
    charts: ['图表库', '/sn/charts/import'], layout: ['周报版式', '/sn/reports?period=weekly'],
    focus: ['核心关注', '/sn'], diagram: ['产业图骨架', '/sn/diagram'],
    judgment: ['研判草稿', '/sn/judgment'], strategy: ['策略草稿', '/sn/strategies'], monitors: ['监测候选', '/'],
  };
  const countLabels = {
    dimensions: {dimensions: ['维度', '个'], sections: ['环节', '个']},
    indicators: {bindings: ['绑定指标', '个'], pending_sections: ['待确认环节', '个']},
    charts: {ready: ['可导入', '张'], needs_mapping: ['需补映射', '张'], failed: ['失败', '项']},
    layout: {dimensions: ['维度', '个'], groups: ['证据图分组', '组'], figures: ['证据图指标', '个'],
      unmapped_figures: ['待补映射', '张']},
    focus: {suggestions: ['关注建议', '项']}, diagram: {nodes: ['节点', '个']},
    judgment: {drafts: ['研判草稿', '份']}, strategy: {drafts: ['策略草稿', '份']},
    monitors: {candidates: ['监测候选', '项'], rules: ['候选条件', '条']},
  };
  let stage = 'form', currentJob = '', submission, pollTimer, searchTimer, fileController;
  let modelLoading = false, filesLoading = false, nextOffset = null, modelError = '', fileStatus = '';
  let loadedFiles = false, loadedModels = false;

  async function api(path, options = {}) {
    const response = await researchFetch(prefix + '/api/sn' + path, {signal: AbortSignal.timeout(30000), ...options});
    const value = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(typeof value.detail === 'string' ? value.detail
        : value.detail?.message || '请求未完成，请重试');
      error.status = response.status;
      throw error;
    }
    return value;
  }

  function checkJob(job, expected) {
    if (!jobId(job?.id) || job.purpose !== 'workbench_setup' || job.workbench_id !== scope?.workbench
        || (expected && expected !== job.id)) {
      const error = new Error('后台任务范围已改变，请重新打开工作台');
      error.status = 409;
      throw error;
    }
    return job;
  }

  function status(message, busy = false, error = false) {
    const label = get('status');
    if (label.textContent !== message) label.textContent = message;
    label.dataset.busy = String(busy);
    label.dataset.error = String(error);
    const inline = get('running-status');
    const inlineMessage = stage === 'results' ? '周报建立任务已完成，待核对' : message;
    if (inline.textContent !== inlineMessage) inline.textContent = inlineMessage;
    inline.dataset.busy = String(busy);
  }

  function controls() {
    const configuring = stage === 'form' || stage === 'submitting';
    const locked = stage !== 'form';
    form.hidden = !configuring;
    search.disabled = locked;
    report.disabled = locked || filesLoading;
    model.disabled = locked || modelLoading || !loadedModels;
    get('more').hidden = nextOffset == null;
    get('more').disabled = locked || filesLoading;
    get('parts').disabled = locked;
    get('submit').hidden = !configuring;
    get('submit').disabled = locked || filesLoading || !report.value || !model.value || !parts.some(part => part.checked);
    get('reopen').hidden = !['results', 'terminal'].includes(stage);
    get('review').hidden = stage !== 'results';
    get('running').hidden = dialog.open || (stage === 'form' || stage === 'discovering');
    const sourceStatus = [fileStatus, modelError].filter(Boolean).join('。');
    if (get('source-status').textContent !== sourceStatus) get('source-status').textContent = sourceStatus;
  }

  async function loadFiles(reset = false) {
    fileController?.abort();
    const controller = new AbortController();
    fileController = controller;
    if (reset) {
      report.replaceChildren(new Option('请选择周报', ''));
      nextOffset = null;
    }
    filesLoading = true;
    fileStatus = '正在读取已授权周报…';
    controls();
    try {
      const query = new URLSearchParams({search: search.value.trim(), offset: nextOffset ?? 0, limit: 50});
      const page = await api('/charts/import/report/files?' + query,
        {signal: AbortSignal.any([controller.signal, AbortSignal.timeout(30000)])});
      if (fileController !== controller) return;
      const values = new Set([...report.options].map(option => option.value));
      for (const item of page.items || []) {
        const value = JSON.stringify(item.citation);
        if (!value || values.has(value)) continue;
        report.append(new Option([item.title, item.subtitle].filter(Boolean).join(' · '), value));
        values.add(value);
      }
      nextOffset = page.next_offset ?? null;
      fileStatus = report.options.length > 1 ? '' : (nextOffset == null
        ? '当前没有匹配的已授权 PPTX / PDF 周报' : '本页没有匹配的周报，可加载更多');
      loadedFiles = true;
    } catch (error) {
      if (fileController === controller && error.name !== 'AbortError') fileStatus = error.message;
    } finally {
      if (fileController === controller) {
        filesLoading = false;
        controls();
      }
    }
  }

  async function loadModels() {
    modelLoading = true;
    modelError = '';
    controls();
    try {
      const data = await api('/strategies/models');
      model.replaceChildren(new Option(data.models?.length ? '请选择模型' : '暂无可用模型', ''));
      for (const item of data.models || []) model.append(new Option(item.name, item.id));
      loadedModels = true;
    } catch (error) {
      model.replaceChildren(new Option('模型读取失败', ''));
      modelError = error.message;
    } finally {
      modelLoading = false;
      controls();
    }
  }

  function showForm() {
    stage = 'form';
    status('');
    controls();
    if (!loadedFiles && !filesLoading) void loadFiles(true);
    if (!loadedModels && !modelLoading) void loadModels();
  }

  function safeText(tag, value, className = '') {
    const child = document.createElement(tag);
    child.className = className;
    child.textContent = value;
    return child;
  }

  function countsFor(item) {
    const labels = countLabels[item.part];
    return Object.entries(labels).flatMap(([key, [label, unit]]) => {
      const count = item.counts?.[key];
      return Number.isSafeInteger(count) && count >= 0 ? [`${label} ${count} ${unit}`] : [];
    }).join('，');
  }

  function showResult(result) {
    const rows = get('review-items');
    rows.replaceChildren();
    const source = result.report?.name;
    get('review-source').textContent = source ? `来源 ${source}` : '';
    for (const item of result.items || []) {
      if (!Object.hasOwn(reviewPages, item.part)) continue;
      const [label, path] = reviewPages[item.part];
      const row = document.createElement('li'), text = document.createElement('div');
      const done = item.state === 'succeeded';
      const summary = done ? countsFor(item) : (item.state === 'blocked' ? '依赖步骤未完成' : '未生成');
      text.append(safeText('strong', label), safeText('p', [summary, item.message].filter(Boolean).join(' · ')));
      row.append(text);
      if (done && (item.review_available ?? Boolean(item.data))) {
        const link = safeText('a', '去核对', 'workbench-setup-link');
        const url = new URL(prefix + path, location.origin);
        url.searchParams.set('setup_job', currentJob);
        url.searchParams.set('setup_part', item.part);
        link.href = url.href;
        link.setAttribute('aria-label', '去核对' + label);
        row.append(link);
      }
      rows.append(row);
    }
    stage = 'results';
    status('请逐项核对后在对应页面保存。');
    controls();
  }

  function terminal(message) {
    stage = 'terminal';
    status(message, false, true);
    controls();
  }

  async function follow(identifier) {
    clearTimeout(pollTimer);
    currentJob = identifier;
    stage = 'waiting';
    status('正在读取后台任务，可关闭窗口。', true);
    controls();
    async function poll() {
      try {
        const job = checkJob(await api(`${jobs}/${identifier}`), identifier);
        if (currentJob !== identifier) return;
        if (active.has(job.state)) {
          status((phaseLabels[job.phase] || '正在后台建立') + '，可关闭窗口。', true);
          pollTimer = setTimeout(poll, 1400);
        } else if (job.state === 'succeeded' && job.result_available) {
          const result = await api(`${jobs}/${identifier}/result`);
          if (currentJob === identifier) showResult(result);
        } else terminal(job.message || '后台任务未完成，请重新建立。');
      } catch (error) {
        if (currentJob !== identifier) return;
        if (error.status >= 400 && error.status < 500 && error.status !== 429) {
          terminal(error.message);
        } else {
          status('正在重试读取后台任务，可关闭窗口。', true);
          pollTimer = setTimeout(poll, 3000);
        }
      }
    }
    await poll();
  }

  function forget() {
    clearTimeout(pollTimer);
    currentJob = '';
    submission = undefined;
    const url = new URL(location.href);
    if (url.searchParams.has('analysis_job')) {
      url.searchParams.delete('analysis_job');
      history.replaceState(history.state, '', url);
    }
    showForm();
  }

  function open() {
    if (!dialog.open) dialog.showModal();
    controls();
    if (stage !== 'form') return;
    if (loadedFiles || filesLoading) {
      showForm();
      return;
    }
    stage = 'discovering';
    status('正在读取后台任务…', true);
    controls();
    void api(jobs + '/latest').then(({job}) => {
      if (job?.result_available || active.has(job?.state)) void follow(checkJob(job).id);
      else showForm();
    }).catch(() => showForm());
  }

  for (const entry of document.querySelectorAll('[data-workbench-setup-open]')) entry.addEventListener('click', open);
  get('close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', controls);
  get('reopen').addEventListener('click', forget);
  form.addEventListener('change', controls);
  search.addEventListener('input', () => {
    clearTimeout(searchTimer);
    fileController?.abort();
    fileController = undefined;
    filesLoading = false;
    fileStatus = '';
    report.replaceChildren(new Option('请选择周报', ''));
    nextOffset = null;
    controls();
    searchTimer = setTimeout(() => void loadFiles(true), 300);
  });
  get('more').addEventListener('click', () => void loadFiles());

  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (get('submit').disabled) return;
    const body = JSON.stringify({source: JSON.parse(report.value), model_id: model.value,
      parts: parts.filter(part => part.checked).map(part => part.value)});
    if (body !== submission?.body) {
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      submission = {body, key: Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')};
    }
    stage = 'submitting';
    status('正在提交后台任务，可关闭窗口。', true);
    controls();
    try {
      const job = checkJob(await api(jobs, {method: 'POST', keepalive: true,
        headers: {'Content-Type': 'application/json', 'Idempotency-Key': submission.key}, body}));
      if (window.parent !== window) window.parent.postMessage({type: 'research-analysis-job-started',
        purpose: 'workbench_setup', workbenchId: job.workbench_id, jobId: job.id}, location.origin);
      if (location.pathname === prefix + '/') {
        const url = new URL(location.href);
        url.searchParams.set('analysis_job', job.id);
        history.replaceState(history.state, '', url);
      }
      void follow(job.id);
    } catch (error) {
      stage = 'form';
      status(error.message, false, true);
      controls();
    }
  });

  if (guide) {
    const key = JSON.stringify(['workbench-setup-guide', scope?.tenant, scope?.user, scope?.workbench]);
    try { guide.hidden = localStorage.getItem(key) === 'dismissed'; }
    catch { guide.hidden = false; }
    get('dismiss').addEventListener('click', () => {
      guide.hidden = true;
      try { localStorage.setItem(key, 'dismissed'); } catch { /* Storage can be disabled. */ }
    });
  }
  const requested = jobId(new URL(location.href).searchParams.get('analysis_job'));
  if (requested && location.pathname === prefix + '/') {
    dialog.showModal();
    void follow(requested);
  }
})();

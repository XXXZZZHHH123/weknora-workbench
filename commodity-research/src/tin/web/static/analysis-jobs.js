/* Owner-scoped detached jobs shared by the research draft editors. */
(function () {
  const endpoint = '/api/sn/analysis-jobs';
  const validId = value => typeof value === 'string' && /^[A-Za-z0-9_-]{16,128}$/.test(value);
  const active = job => job.state === 'queued' || job.state === 'running';
  const requestedId = () => new URL(location.href, location.origin).searchParams.get('analysis_job');
  let requestedJob;

  async function request(path, options) {
    const response = await researchFetch((window.RESEARCH_PREFIX || '') + path, options);
    let data;
    try { data = await response.json(); }
    catch { throw new Error('暂时无法读取后台任务，请稍后重新打开。'); }
    if (!response.ok) {
      const error = new Error(typeof data?.detail === 'string' ? data.detail :
        data?.detail?.message || '后台任务未完成，请稍后重试。');
      error.status = response.status;
      error.detail = data?.detail;
      throw error;
    }
    return data;
  }

  function checkJob(job, purpose, id) {
    const workbench = window.RESEARCH_CONTEXT?.workbench;
    if (!validId(job?.id) || id && job.id !== id || purpose && job.purpose !== purpose
        || workbench && job.workbench_id !== workbench) {
      throw new Error('后台任务范围已改变，请重新打开工作台。');
    }
    return job;
  }

  function requested(purpose, id = requestedId()) {
    if (!validId(id)) return Promise.resolve(null);
    if (requestedJob?.id !== id) {
      const value = request(endpoint + '/' + encodeURIComponent(id)).then(job => checkJob(job, null, id))
        .catch(error => error.detail?.purpose ? {id, purpose: error.detail.purpose} : null);
      requestedJob = {id, value};
    }
    return requestedJob.value.then(job => job && (!purpose || job.purpose === purpose) ? job : null);
  }

  function message(status, text, busy = false) {
    if (!status) return;
    status.textContent = text;
    status.dataset.analysisBusy = String(busy);
  }

  function announce(job) {
    const workbenchId = window.RESEARCH_CONTEXT?.workbench;
    if (parent !== window && workbenchId) {
      parent.postMessage({type: 'research-analysis-job-started', purpose: job.purpose,
        workbenchId, jobId: job.id}, location.origin);
    }
  }

  function create({purpose, button, status, snapshot, present, isOpen = () => true,
    disabled = () => false, accept = () => true, restoreButton}) {
    let tracked, timer, controller, latest, uncertain, serial = 0;
    let running = false, submitting = false;
    const busy = () => running || submitting;
    const sync = () => { if (button) button.disabled = busy() || disabled(); };
    const tell = (text, spinning = false) => message(status, text, spinning);
    const fingerprint = () => JSON.stringify(snapshot());

    function pause() {
      clearTimeout(timer);
      controller?.abort();
      controller = undefined;
      serial++;
    }

    async function follow(entry) {
      pause();
      const operation = serial;
      controller = new AbortController();
      const signal = controller.signal;
      const current = () => operation === serial && isOpen();
      const path = endpoint + '/' + encodeURIComponent(entry.id);
      running = true;
      sync();
      tell('正在读取后台分析进度，可关闭窗口。', true);

      async function poll() {
        if (!current()) return;
        try {
          const job = checkJob(await request(path, {signal}), purpose, entry.id);
          if (!current()) return;
          if (active(job)) {
            tell('正在后台分析 · ' + (job.phase_label || '处理中') + '，可关闭窗口。', true);
            timer = setTimeout(poll, 2000);
            return;
          }
          if (job.state !== 'succeeded') {
            tracked = undefined;
            tell(job.message || '后台分析未完成，请重新发起。');
          } else {
            const result = await request(path + '/result', {signal});
            if (!current()) return;
            checkJob(result.job, purpose, entry.id);
            latest = job;
            if (restoreButton) restoreButton.hidden = false;
            if (fingerprint() !== entry.before) {
              tell('后台分析已完成。编辑内容已改动，未覆盖。');
            } else {
              await present(result, {restored: entry.restored, explicit: entry.explicit,
                current: () => current() && fingerprint() === entry.before});
            }
            // A result may close its source dialog after filling another editor.
            if (operation !== serial) return;
            tracked = undefined;
            if (isOpen() && status?.dataset.analysisBusy === 'true') {
              status.dataset.analysisBusy = 'false';
              if (status.textContent.includes('可关闭窗口')) tell('后台分析已完成，请核对结果。');
            }
          }
        } catch (error) {
          if (!current()) return;
          tracked = undefined;
          tell(error.message || '暂时无法读取后台任务，请稍后重新打开。');
        }
        running = false;
        if (isOpen()) sync();
      }
      return poll();
    }

    function restore(id, {explicit = false} = {}) {
      if (!validId(id) || submitting || !isOpen()) return;
      if (tracked && tracked.id !== id && running) return;
      if (tracked?.id !== id) tracked = {id, before: fingerprint(), restored: true, explicit};
      return follow(tracked);
    }

    async function submit(path, body) {
      if (busy() || disabled()) return;
      const raw = JSON.stringify(body);
      if (!uncertain || uncertain.path !== path || uncertain.raw !== raw) {
        const bytes = new Uint8Array(16);
        window.crypto.getRandomValues(bytes);
        uncertain = {path, raw, key: Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')};
      }
      const before = fingerprint();
      submitting = true;
      sync();
      tell('正在提交后台分析，可关闭窗口。', true);
      try {
        const job = checkJob(await request(path, {method: 'POST',
          headers: {'Content-Type': 'application/json', 'Idempotency-Key': uncertain.key}, body: raw}), purpose);
        uncertain = undefined;
        tracked = {id: job.id, before, restored: false, explicit: false};
        running = active(job);
        announce(job);
      } catch (error) {
        if (error.status && error.status < 500) uncertain = undefined;
        if (isOpen()) tell(error.message || '暂时无法提交后台任务。');
      } finally {
        submitting = false;
        if (isOpen()) sync();
      }
      if (tracked && isOpen()) return follow(tracked);
    }

    async function discover() {
      if (!isOpen() || submitting) return;
      sync();
      if (tracked) return follow(tracked);
      if (status?.dataset.analysisBusy === 'true') tell('');
      if (restoreButton) restoreButton.hidden = true;
      const operation = serial;
      try {
        const {job} = await request(endpoint + '/latest?purpose=' + encodeURIComponent(purpose));
        if (!job || !isOpen() || operation !== serial || busy() || !accept(job)) return;
        checkJob(job, purpose);
        if (active(job) || !restoreButton) return restore(job.id);
        latest = job;
        restoreButton.hidden = false;
      } catch { /* An unavailable previous draft does not block the editor. */ }
    }

    restoreButton?.addEventListener('click', () => { if (latest) restore(latest.id, {explicit: true}); });
    return {submit, restore, discover, pause, get busy() { return busy(); }};
  }

  window.ResearchAnalysisJobs = {create, requested, message};
})();

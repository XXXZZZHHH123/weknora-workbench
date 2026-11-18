(() => {
  'use strict';
  const params = new URLSearchParams(location.search);
  const job = params.get('setup_job'), part = params.get('setup_part');
  if (!job || !part) return;
  const prefix = window.RESEARCH_PREFIX || '';
  async function request(path, options) {
    const url = `${prefix}/api/sn/workbench-setup/jobs/${encodeURIComponent(job)}${path}`;
    const response = await window.researchFetch(url, options);
    const result = await response.json();
    if (!response.ok) throw new Error(typeof result.detail === 'string' ? result.detail : '任务结果无法读取，请返回核对清单重试。');
    return result;
  }
  window.WorkbenchSetupReview = {
    is: (...parts) => parts.includes(part),
    id: job,
    part,
    historicalNotice: '来自所选周报的当期观点，请修改为当前判断后再保存。',
    async load() {
      if (part === 'judgment') return request('/judgment-preview', {method: 'POST'});
      if (part === 'monitors') return request('/monitor-preview', {method: 'POST'});
      const result = await request('/result');
      const entry = result.items?.find(item => item.part === part);
      if (entry?.state !== 'succeeded' || !entry.data) throw new Error('这个任务没有可供核对的结果。');
      return {...entry.data, setupReport: result.report};
    },
  };
})();

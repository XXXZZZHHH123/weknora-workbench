(() => {
  'use strict';
  const dialog = document.getElementById('research-card-dialog');
  const sourceData = document.getElementById('research-card-data');
  if (!dialog || !sourceData) return;
  const records = JSON.parse(sourceData.textContent).items || [];
  const $ = id => document.getElementById('research-card-' + id);
  const fields = {title: 'title', direction: 'direction', thesis: 'thesis', watch_for: 'watch',
    trigger: 'trigger', invalidation: 'invalidation', review_date: 'review', original_text: 'original'};
  const endpoint = (window.RESEARCH_PREFIX || '') + '/api/sn/research-strategies';
  let editing = null, selectedSources = new Set(), sourcesPage = 0, generation = 0, sourceLoading = false, fileSequence = 0;
  function status(message, error = false) {
    $('form-status').textContent = message;
    $('form-status').setAttribute('role', error ? 'alert' : 'status');
  }
  function fill(card = {}) {
    for (const [key, id] of Object.entries(fields)) $(id).value = card[key] || (key === 'direction' ? 'observe' : '');
  }
  async function loadSources() {
    if (sourceLoading) return;
    sourceLoading = true;
    const own = generation, page = sourcesPage + 1;
    $('more-sources').disabled = true;
    try {
      const response = await researchFetch(endpoint + '/sources?page=' + page);
      const data = await response.json();
      if (own !== generation || !dialog.open) return;
      if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '无法读取关联资料');
      for (const item of data.items || []) {
        const label = document.createElement('label'), box = document.createElement('input'), text = document.createElement('span');
        box.type = 'checkbox'; box.value = item.id; box.checked = selectedSources.has(item.id);
        box.addEventListener('change', () => box.checked ? selectedSources.add(item.id) : selectedSources.delete(item.id));
        text.textContent = item.file_name || item.title || item.id; label.append(box, text); $('sources').append(label);
      }
      sourcesPage = page; $('more-sources').hidden = !data.has_more;
    } catch (error) { if (own === generation) status(error.message, true); }
    finally { if (own === generation) { sourceLoading = false; $('more-sources').disabled = false; } }
  }
  function open(item = null) {
    generation += 1; sourceLoading = false; editing = item;
    selectedSources = new Set(item?.source_ids || []); sourcesPage = 0;
    fill(item?.card || {}); $('file').value = ''; $('filename').textContent = '';
    $('sources').replaceChildren(); $('more-sources').hidden = true;
    $('save').disabled = false; status(''); dialog.showModal(); loadSources();
  }
  $('add').addEventListener('click', () => open());
  document.querySelectorAll('[data-research-card-edit]').forEach(button => button.addEventListener('click', () => {
    const item = records.find(record => record.id === button.dataset.researchCardEdit);
    if (item?.card) open(item);
  }));
  document.querySelectorAll('[data-research-card-close]').forEach(button => button.addEventListener('click', () => dialog.close()));
  dialog.addEventListener('close', () => { generation += 1; sourceLoading = false; });
  $('more-sources').addEventListener('click', loadSources);
  $('file').addEventListener('change', async () => {
    const file = $('file').files?.[0], own = generation, fileRequest = ++fileSequence;
    if (!file) return;
    try {
      if (!/\.(txt|md|json)$/i.test(file.name) || file.size > 96000) throw new Error('请选择不超过96 KB的 UTF-8 .txt、.md 或研究策略卡 .json 文件');
      const text = new TextDecoder('utf-8', {fatal: true}).decode(await file.arrayBuffer());
      if (own !== generation || fileRequest !== fileSequence || !dialog.open) return;
      if (/\.json$/i.test(file.name)) {
        const input = JSON.parse(text), card = input.card || input;
        if (!card || typeof card !== 'object' || Array.isArray(card) || card.format !== 'research-strategy-card.v1'
            || Object.keys(card).some(key => key !== 'format' && !Object.hasOwn(fields, key))
            || Object.entries(fields).some(([key]) => card[key] !== undefined && typeof card[key] !== 'string')) {
          throw new Error('该JSON不是研究策略卡格式，请使用TXT/MD保留原文后人工填写');
        }
        if (input.source_ids !== undefined && (!Array.isArray(input.source_ids)
            || input.source_ids.some(value => typeof value !== 'string'))) throw new Error('关联资料编号格式无效');
        fill(card);
        if (Array.isArray(input.source_ids)) selectedSources = new Set(input.source_ids);
        $('sources').querySelectorAll('input').forEach(box => { box.checked = selectedSources.has(box.value); });
      } else {
        if (text.length > 24000) throw new Error('原文超过24,000字，请先缩短');
        $('original').value = text;
        if (!$('title').value) $('title').value = file.name.replace(/\.(txt|md)$/i, '').slice(0, 120);
        $('original').closest('details').open = true;
      }
      $('filename').textContent = file.name;
      status('原文已载入，请核对研究假设、条件和资料后保存；没有自动生成交易价位。');
    } catch (error) { status(error.message, true); }
  });
  $('form').addEventListener('submit', async event => {
    event.preventDefault();
    const own = generation, button = $('save'); button.disabled = true;
    const card = {format: 'research-strategy-card.v1'};
    for (const [key, id] of Object.entries(fields)) card[key] = $(id).value;
    const body = {card, source_ids: [...selectedSources], ...(editing ? {revision: editing.revision} : {})};
    try {
      status('正在保存个人研究草稿与关联来源…');
      const response = await researchFetch(endpoint + (editing ? '/' + encodeURIComponent(editing.id) : ''), {
        method: editing ? 'PATCH' : 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
      const data = await response.json();
      if (own !== generation || !dialog.open) return;
      if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : '保存未完成，请检查字段');
      location.reload();
    } catch (error) { if (own === generation) status(error.message, true); }
    finally { if (own === generation) button.disabled = false; }
  });
})();

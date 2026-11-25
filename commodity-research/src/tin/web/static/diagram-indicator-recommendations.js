/* Keep model recommendations in the editable canvas until the researcher saves. */
(() => {
  const purpose = 'diagram_indicator_recommendations';
  const button = document.getElementById('dg-recommend');
  if (!button) return;
  const status = document.getElementById('dg-recommend-status');
  const jobs = window.ResearchAnalysisJobs;
  const empty = node => !dgIsGroup(node) && [undefined, null, 'none'].includes(node.binding?.kind);
  let choosingModel = false;

  function subjects() {
    const byId = new Map(dg.layout.nodes.map(node => [node.id, node]));
    return dg.layout.nodes.filter(empty).map(node => {
      const trail = [];
      for (let parent = byId.get(node.parent); parent && trail.length < 3; parent = byId.get(parent.parent)) {
        trail.unshift(parent.label);
      }
      const adjacent = new Set();
      for (const edge of dg.layout.edges) {
        const other = byId.get(edge.from === node.id ? edge.to : edge.to === node.id ? edge.from : null);
        if (other) adjacent.add(other.label);
        if (adjacent.size === 4) break;
      }
      const neighbors = adjacent.size ? '；关联节点：' + [...adjacent].join(' / ') : '';
      return {id: node.id, label: node.label, context: (trail.join(' / ') + neighbors).slice(0, 600)};
    });
  }

  function count() { return dg.layout?.nodes.filter(empty).length || 0; }
  function sync() {
    const remaining = count();
    button.textContent = `为空节点推荐指标（${remaining} 个）`;
    button.disabled = choosingModel || controller.busy || !remaining || !dg.layout?.__id;
  }

  function describe(candidate) {
    const dimensions = Object.entries(candidate.dimensions || {}).map(([key, value]) => `${key}：${value}`);
    return [candidate.name, ...dimensions, candidate.sheet, dgKnown(candidate.unit), dgKnown(candidate.frequency),
      candidate.reason].filter(Boolean).join(' · ');
  }

  function get(node) {
    return Object.hasOwn(dg.recommendations, node.id) ? dg.recommendations[node.id] : null;
  }
  function title(item) {
    if (item.recommended) return '模型推荐 · 待核对：' + describe(item.recommended);
    if (!item.ambiguous) return '没有找到一致的指标';
    return ['节点口径待明确', ...item.recommendations.map(describe)].join('\n');
  }
  function show(node) {
    const item = get(node);
    if (!item) return false;
    const panel = document.getElementById('dg-suggest');
    const heading = document.createElement('p');
    heading.textContent = item.recommended ? title(item) : item.ambiguous ? '节点口径待明确，请核对候选口径' : title(item);
    panel.replaceChildren(heading);
    for (const candidate of item.recommendations) {
      if (candidate.series_id === item.recommended?.series_id) continue;
      const option = document.createElement('button');
      option.type = 'button';
      option.className = 'w-full text-left px-2 py-1.5 rounded hover:bg-[var(--surface-soft)]';
      option.textContent = describe(candidate);
      option.addEventListener('click', () => dgBind(candidate.series_id, 'observation'));
      panel.append(option);
    }
    const unbind = document.getElementById('dg-unbind');
    if (unbind && item.recommended) unbind.textContent = '撤回模型推荐';
    return true;
  }

  function fill(items, version, eligible = () => true) {
    let filled = 0, ambiguous = 0, skipped = 0;
    for (const node of dg.layout.nodes) {
      const item = Object.hasOwn(items, node.id) && items[node.id];
      if (!item) continue;
      if (!empty(node) || !eligible(node)) { skipped++; continue; }
      if (item.recommended) {
        if (!filled) dgPushUndo();
        filled++;
        node.binding = {kind: 'series', series_id: item.recommended.series_id};
      }
      if (item.ambiguous) ambiguous++;
      dg.recommendations = {...dg.recommendations, [node.id]: {...item, version}};
    }
    if (filled) dg.dirty = true;
    dgSyncSave();
    dgRender();
    if (filled) void dgLoad();
    const active = dg.layout.nodes.find(node => node.id === dg.active);
    if (active && get(active)) {
      dgSuggestGeneration++;
      dgSuggestController?.abort();
      show(active);
    }
    return {filled, ambiguous, skipped};
  }

  const controller = jobs.create({
    purpose, button, status, restoreButton: document.getElementById('dg-recommend-restore'),
    isOpen: () => dg.edit && !!dg.layout,
    snapshot: () => [dg.layout.__id, dg.generationDraft?.key],
    disabled: () => choosingModel || !count(),
    onSync: sync,
    accept: job => job.context?.template_id === dg.layout.__id,
    present(data) {
      if (data.request?.template_id !== dg.layout.__id || data.request?.title !== dg.layout.__name) {
        jobs.message(status, '这次推荐对应另一份产业图草稿。');
        return;
      }
      const submitted = new Map(data.request.nodes.map(node => [node.id, node]));
      const current = new Map(subjects().map(node => [node.id, node]));
      const {filled, ambiguous, skipped} = fill(data.items, data.version, node => {
        const previous = submitted.get(node.id), now = current.get(node.id);
        return now && previous?.label === now.label && previous.context === now.context;
      });
      jobs.message(status, `已填入 ${filled} 个待核对绑定` + (ambiguous ? `，${ambiguous} 个节点口径待明确` : '') +
        (skipped ? `，${skipped} 个节点已改动或已绑定` : '') + '。');
    },
  });

  button.addEventListener('click', async () => {
    if (button.disabled) return;
    const templateId = dg.layout.__id;
    choosingModel = true;
    sync();
    jobs.message(status, '正在读取可用模型。', true);
    try {
      const {models} = await dgGeneratorRequest('/models');
      if (!dg.edit || dg.layout.__id !== templateId) { jobs.message(status, ''); return; }
      const selected = document.getElementById('dg-ai-model')?.value;
      const model = models.find(model => model.id === selected) || models[0];
      if (!model) { jobs.message(status, '请先配置一个可用的问答模型。'); return; }
      choosingModel = false;
      await controller.submit('/api/sn/diagram/generation/indicator-recommendations', {
        model_id: model.id, template_id: templateId, title: dg.layout.__name,
        nodes: subjects(), shared_ids: [...new Set(dg.layout.nodes.map(node => node.binding?.series_id).filter(Boolean))],
      });
    } catch (error) { jobs.message(status, error.message); }
    finally { choosingModel = false; sync(); }
  });

  async function restore() {
    const job = await jobs.requested(purpose);
    if (!job || job.context?.template_id !== dg.layout?.__id) return false;
    if (!dg.edit) dgToggleEdit();
    void controller.restore(job.id, {explicit: true});
    return true;
  }
  window.DiagramRecommendations = {
    get, title, show, fill, sync, restore,
    open: () => { if (!dg.generationDraft) void controller.discover(); },
    close: () => controller.pause(),
    detach: () => controller.detach(),
  };
})();

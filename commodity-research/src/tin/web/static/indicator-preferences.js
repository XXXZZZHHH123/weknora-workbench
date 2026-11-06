(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.IndicatorPreferences = api;
})(typeof window === "undefined" ? globalThis : window, function () {
  "use strict";
  const unique = (ids) => [...new Set(ids)];
  function key(context, kind = "favorites") {
    const prefix = kind === "core" ? "tin.market.core.v1" : "tin.indicators.favorites.v1";
    return context?.workbench ? `${prefix}:${context.tenant}:${context.user}:${context.workbench}` : prefix;
  }
  function read(storage, storageKey, allowed, limit) {
    const raw = storage.getItem(storageKey);
    if (raw === null) return null;
    const values = JSON.parse(raw);
    if (!Array.isArray(values) || values.some((id) => typeof id !== "string")) throw new Error("保存的指标选择格式无效，请重新选择。");
    return unique(values).filter((id) => allowed.has(id)).slice(0, limit);
  }
  function plan(core, favorites, allowed) {
    if (!Array.isArray(core) || core.some((id) => typeof id !== "string" || !allowed.has(id))) throw new Error("部分指标已不在当前知识库目录，请刷新后重新选择。");
    const selected = unique(core);
    if (selected.length > 16) throw new Error("核心关注最多选择 16 项。");
    const merged = favorites.map(item => ({...item}));
    const present = new Set(favorites.filter(item => item.kind === "indicator").map(item => item.id));
    selected.forEach(id => { if (!present.has(id)) merged.push({kind: "indicator", id}); });
    if (merged.length > 100) throw new Error("自选最多 100 项，请先在指标页移出部分自选，再添加核心关注。");
    return { core: selected, favorites: merged };
  }
  function saveCore(storage, context, selection) {
    try {
      storage.setItem(key(context, "core"), JSON.stringify(selection.core));
      return true;
    } catch (_) { return false; }
  }
  const identity = item => `${item.kind}:${item.id}`;
  function focusPlan(items, favorites, allowed) {
    if (!Array.isArray(items) || items.some(item => !item || !['indicator', 'chart'].includes(item.kind) ||
        typeof item.id !== 'string' || !item.id || !allowed.has(identity(item)))) throw new Error('部分条目已不在当前目录或图表库，请刷新后重新选择。');
    const seen = new Set(), core = [];
    for (const item of items) if (!seen.has(identity(item))) { seen.add(identity(item)); core.push({kind: item.kind, id: item.id}); }
    if (core.length > 16) throw new Error('核心关注最多选择 16 项。');
    const merged = favorites.map(item => ({...item})), present = new Set(merged.map(identity));
    for (const item of core) if (!present.has(identity(item))) merged.push({...item});
    if (merged.length > 100) throw new Error('自选最多 100 项，请先在指标页移出部分自选，再添加核心关注。');
    return {core, favorites: merged};
  }
  function chartSummary(data, definition) {
    const cycle = definition?.time?.mode === 'cycle';
    const finite = value => typeof value === 'number' && Number.isFinite(value);
    const sourceValue = point => cycle || finite(point.raw) ? point.raw : point.value;
    const latest = points => (points || []).filter(point => finite(sourceValue(point)))
      .reduce((previous, point) => !previous || point.date > previous.date ? point : previous, null);
    if (cycle) {
      const summarize = (result, label, unit) => {
        const segment = (result?.segments || []).find(item => item.current), point = latest(segment?.points);
        return {label, value: point?.raw ?? null, date: point?.date || null,
          unit: unit || '', rank: result?.position?.rank ?? null, n: result?.position?.n ?? null};
      };
      if ((data.curves || []).some(curve => curve.cycle)) return data.curves.map((curve, index) =>
        summarize(curve.cycle, curve.label || definition.curves?.[index]?.label || '', curve.unit));
      return [summarize(data, definition.curves?.[0]?.label || '', data.unit)];
    }
    return (data.curves || []).map(curve => { const point = latest(curve.points); return {
      label: curve.label || '', value: point ? sourceValue(point) : null, date: point?.date || null, unit: curve.unit || '', rank: null, n: null,
    }; });
  }
  function pool(limit = 4) {
    let active = 0; const queue = [];
    const pump = () => { while (active < limit && queue.length) {
      const {task, resolve, reject} = queue.shift(); active++;
      Promise.resolve().then(task).then(resolve, reject).finally(() => { active--; pump(); });
    } };
    return task => new Promise((resolve, reject) => { queue.push({task, resolve, reject}); pump(); });
  }
  return Object.freeze({ key, read, plan, saveCore, identity, focusPlan, chartSummary, pool });
});

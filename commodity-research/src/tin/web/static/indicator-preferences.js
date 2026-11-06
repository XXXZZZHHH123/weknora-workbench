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
    if (selected.length > 12) throw new Error("核心指标最多选择 12 项。");
    const merged = favorites.map(item => ({...item}));
    const present = new Set(favorites.filter(item => item.kind === "indicator").map(item => item.id));
    selected.forEach(id => { if (!present.has(id)) merged.push({kind: "indicator", id}); });
    if (merged.length > 100) throw new Error("自选最多 100 项，请先在指标页移出部分自选，再添加核心指标。");
    return { core: selected, favorites: merged };
  }
  function saveCore(storage, context, selection) {
    try {
      storage.setItem(key(context, "core"), JSON.stringify(selection.core));
      return true;
    } catch (_) { return false; }
  }
  return Object.freeze({ key, read, plan, saveCore });
});

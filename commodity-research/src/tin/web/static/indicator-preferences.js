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
    const merged = unique([...favorites.filter((id) => allowed.has(id)), ...selected]);
    if (merged.length > 40) throw new Error("自选最多 40 项，请先在指标页移出部分自选，再添加核心指标。");
    return { core: selected, favorites: merged };
  }
  function save(storage, context, selection) {
    const coreKey = key(context, "core"), favoritesKey = key(context);
    let oldCore, oldFavorites;
    try {
      oldCore = storage.getItem(coreKey); oldFavorites = storage.getItem(favoritesKey);
    } catch (_) { return false; }
    try {
      storage.setItem(favoritesKey, JSON.stringify(selection.favorites));
      storage.setItem(coreKey, JSON.stringify(selection.core));
      return true;
    } catch (_) {
      for (const [storageKey, previous] of [[coreKey, oldCore], [favoritesKey, oldFavorites]]) {
        try { if (previous === null) storage.removeItem(storageKey); else storage.setItem(storageKey, previous); }
        catch (_) { /* Still attempt the other key if one storage operation remains unavailable. */ }
      }
      return false;
    }
  }
  return Object.freeze({ key, read, plan, save });
});

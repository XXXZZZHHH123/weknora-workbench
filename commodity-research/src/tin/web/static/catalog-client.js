window.ResearchCatalog = {
  async read({ search = "", offset = 0, identifiers, date, signal } = {}) {
    const query = new URLSearchParams({ search, offset: String(offset), limit: "200" });
    identifiers?.forEach(identifier => query.append("series_id", identifier));
    if (date) query.set("date", date);
    const response = await window.researchFetch(`${window.RESEARCH_PREFIX || ""}/api/sn/indicators/catalog?${query}`, { signal });
    if (!response.ok) throw new Error("指标目录暂时不可读，请重试。");
    const result = await response.json();
    if (!Array.isArray(result.indicators) || result.indicators.length > 200 || !Number.isSafeInteger(result.total) || result.total < result.indicators.length || result.offset !== offset || !(result.next_offset === null || Number.isSafeInteger(result.next_offset) && result.next_offset === offset + result.indicators.length && result.next_offset < result.total)) throw new Error("指标目录分页信息不完整，请刷新后重试。");
    return result;
  },
  identifiers(storage, key, limit) {
    const raw = storage.getItem(key);
    if (raw === null) return null;
    const value = JSON.parse(raw);
    if (!Array.isArray(value) || value.length > limit || value.some(identifier => typeof identifier !== "string" || !identifier || identifier.length > 128)) throw new Error("指标选择保存内容无效。");
    return [...new Set(value)];
  },
  favorites(storage, key) {
    return this.identifiers(storage, key, 40) || [];
  },
};

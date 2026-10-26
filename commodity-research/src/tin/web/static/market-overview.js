document.addEventListener("DOMContentLoaded", () => {
  const root = document.getElementById("market-core");
  if (!root) return;
  const preferences = window.IndicatorPreferences;
  const metadata = JSON.parse(document.getElementById("market-core-catalog").textContent);
  const catalog = new Map(metadata.indicators.map((row) => [row.series_id, row]));
  const allowed = new Set(catalog.keys()), context = window.RESEARCH_CONTEXT;
  let storage;
  try { storage = window.localStorage; }
  catch (_) { storage = { getItem() { throw new Error("unavailable"); } }; }
  const get = (id) => document.getElementById(`market-core-${id}`);
  const dialog = get("dialog"), grid = get("grid"), notice = get("notice"), error = get("error");
  let selected = metadata.defaults.filter((id) => allowed.has(id)), draft = [], favorites = [];
  let controller, generation = 0, restoredCore = null, restoreController, restoreGeneration = 0;
  const announce = (message) => { notice.textContent = message; notice.hidden = !message; };
  try {
    if (metadata.catalog_paged) restoredCore = window.ResearchCatalog.identifiers(storage, preferences.key(context, "core"), 12);
    else {
      const saved = preferences.read(storage, preferences.key(context, "core"), allowed, 12);
      if (saved !== null) selected = saved;
    }
    favorites = metadata.catalog_paged ? window.ResearchCatalog.favorites(storage, preferences.key(context)) : preferences.read(storage, preferences.key(context), allowed, 40) || [];
  } catch (_) { announce("无法读取已保存的指标选择，本次使用默认排列；重新选择后可尝试保存。"); }
  function syncCount() {
    get("count").textContent = String(selected.length);
    get("empty").hidden = selected.length > 0;
    const total = get("total"); if (total) total.textContent = String(selected.length);
  }
  async function restoreSelection() {
    if (restoredCore === null) return;
    restoreController?.abort(); restoreController = new AbortController(); const own = ++restoreGeneration;
    get("open").disabled = true; grid.setAttribute("aria-busy", "true");
    try {
      let confirmed = [];
      if (restoredCore.length) {
        const result = await window.ResearchCatalog.read({ identifiers: restoredCore, signal: restoreController.signal });
        if (own !== restoreGeneration) return;
        if (result.total !== result.indicators.length || result.next_offset !== null) throw new Error("已保存的核心指标目录未完整返回，请刷新后重试。");
        const requested = new Set(restoredCore), available = new Set();
        for (const row of result.indicators) {
          if (!requested.has(row.series_id)) throw new Error("返回的核心指标目录范围不匹配，请刷新后重试。");
          catalog.set(row.series_id, row); allowed.add(row.series_id); available.add(row.series_id);
        }
        confirmed = restoredCore.filter((id) => available.has(id));
        if (confirmed.length !== restoredCore.length) announce("部分已保存的核心指标当前不可用，已暂时隐藏；浏览器中的选择仍保留。");
      }
      selected = confirmed;
      syncCount();
      if (JSON.stringify(selected) !== JSON.stringify(metadata.defaults)) await loadCards();
    } catch (error) {
      if (own === restoreGeneration && error.name !== "AbortError") announce("暂时无法核对已保存的核心指标，本次显示默认排列；浏览器中的选择仍保留。");
    } finally {
      if (own === restoreGeneration) { get("open").disabled = false; grid.removeAttribute("aria-busy"); }
    }
  }
  async function loadCards() {
    controller?.abort(); controller = new AbortController(); const own = ++generation;
    error.hidden = true; grid.replaceChildren(); syncCount();
    const ready = get("ready"); if (ready) ready.textContent = selected.length ? "…" : "0";
    if (!selected.length) { grid.removeAttribute("aria-busy"); return; }
    grid.setAttribute("aria-busy", "true");
    try {
      const params = new URLSearchParams();
      selected.forEach((id) => params.append("series_id", id));
      if (metadata.date) params.set("date", metadata.date);
      const response = await window.researchFetch(`${window.RESEARCH_PREFIX || ""}/api/sn/market/core?${params}`, { signal: controller.signal });
      if (!response.ok) throw new Error("核心指标暂时无法读取，选择与排列已保留。请重试或核对资料权限。");
      const data = await response.json();
      if (own !== generation) return;
      if (!Array.isArray(data.series_ids) || data.series_ids.length !== selected.length || data.series_ids.some((id, index) => id !== selected[index]) || typeof data.html !== "string" || !Number.isInteger(data.ready_count) || data.ready_count < 0 || data.ready_count > selected.length) throw new Error("返回的核心指标范围不匹配，请刷新后重试。");
      grid.innerHTML = data.html;
      if (ready) ready.textContent = String(data.ready_count);
    } catch (e) {
      if (own !== generation || e.name === "AbortError") return;
      if (ready) ready.textContent = "—";
      error.querySelector("span").textContent = e.message || "核心指标暂时无法读取，请重试。";
      error.hidden = false;
    } finally { if (own === generation) grid.removeAttribute("aria-busy"); }
  }
  const normalize = (value) => String(value).normalize("NFKC").toLocaleLowerCase();
  function button(label, action, disabled) {
    const element = document.createElement("button"); element.type = "button";
    element.textContent = label; element.className = "market-core-button"; element.disabled = disabled;
    element.addEventListener("click", action); return element;
  }
  function renderSelected(focusIndex, direction) {
    const list = get("selected"); list.replaceChildren();
    get("draft-count").textContent = `${draft.length}/12`;
    get("selected-empty").hidden = draft.length > 0;
    draft.forEach((id, index) => {
      const item = document.createElement("li"), label = document.createElement("span");
      label.textContent = `${index + 1}. ${catalog.get(id).name}`; item.append(label);
      const actions = document.createElement("div");
      for (const [delta, text] of [[-1, "上移"], [1, "下移"]]) {
        const move = button(text, () => {
          const target = index + delta;
          [draft[index], draft[target]] = [draft[target], draft[index]];
          renderSelected(target, delta);
        }, index + delta < 0 || index + delta >= draft.length);
        move.setAttribute("aria-label", `${text}${catalog.get(id).name}`); move.dataset.direction = String(delta); actions.append(move);
      }
      const remove = button("移出", () => {
        draft = draft.filter((value) => value !== id); renderSelected(); renderCatalog();
      }, false);
      remove.setAttribute("aria-label", `移出核心${catalog.get(id).name}`); actions.append(remove);
      item.append(actions); list.append(item);
    });
    if (focusIndex !== undefined) {
      const item = list.children[focusIndex];
      const target = [...item.querySelectorAll("button")].find((el) => el.dataset.direction === String(direction) && !el.disabled) || item.querySelector("button:not(:disabled)");
      target?.focus();
    }
  }
  let directoryController, directoryGeneration = 0, directoryTimer, visibleChoices;
  async function renderCatalog(offset = 0) {
    const query = normalize(get("search").value.trim()), list = get("choices"); list.replaceChildren();
    if (metadata.catalog_paged) {
      directoryController?.abort(); directoryController = new AbortController(); const own = ++directoryGeneration;
      list.setAttribute("aria-busy", "true");
      try {
        const result = await window.ResearchCatalog.read({ search: get("search").value.trim(), offset, signal: directoryController.signal });
        if (own !== directoryGeneration) return;
        visibleChoices = result;
        for (const row of result.indicators) { catalog.set(row.series_id, row); allowed.add(row.series_id); }
      } catch (error) {
        if (own === directoryGeneration && error.name !== "AbortError") { get("dialog-error").textContent = error.message; get("dialog-error").hidden = false; }
        return;
      } finally { if (own === directoryGeneration) list.removeAttribute("aria-busy"); }
    }
    for (const row of metadata.catalog_paged ? visibleChoices.indicators : catalog.values()) {
      if (!metadata.catalog_paged && query && !normalize(`${row.name} ${row.series_id} ${row.category} ${row.unit} ${row.source}`).includes(query)) continue;
      const label = document.createElement("label"), checkbox = document.createElement("input"), content = document.createElement("span");
      checkbox.type = "checkbox"; checkbox.value = row.series_id; checkbox.checked = draft.includes(row.series_id);
      checkbox.addEventListener("change", () => {
        get("dialog-error").hidden = true;
        if (checkbox.checked && draft.length >= 12) {
          checkbox.checked = false; get("dialog-error").textContent = "核心指标最多选择 12 项。"; get("dialog-error").hidden = false; return;
        }
        draft = checkbox.checked ? [...draft, row.series_id] : draft.filter((id) => id !== row.series_id);
        renderSelected();
      });
      const title = document.createElement("b"), detail = document.createElement("small");
      title.textContent = row.name; detail.textContent = `${row.category} · ${row.unit} · ${row.source}`;
      content.append(title, detail); label.append(checkbox, content); list.append(label);
    }
    get("no-results").hidden = list.children.length > 0;
    if (metadata.catalog_paged && (offset || visibleChoices.next_offset !== null)) {
      const pagination = document.createElement("div");
      if (offset) pagination.append(button("上一页", () => renderCatalog(Math.max(0, offset - 200)), false));
      if (visibleChoices.next_offset !== null) pagination.append(button("下一页", () => renderCatalog(visibleChoices.next_offset), false));
      list.append(pagination);
    }
  }
  get("open").addEventListener("click", () => {
    draft = [...selected]; get("search").value = ""; get("dialog-error").hidden = true;
    renderSelected(); renderCatalog(); dialog.showModal(); get("search").focus();
  });
  get("search").addEventListener("input", () => { if (!metadata.catalog_paged) { void renderCatalog(); return; } clearTimeout(directoryTimer); directoryGeneration++; directoryController?.abort(); directoryTimer = setTimeout(() => renderCatalog(), 240); });
  get("cancel").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => { directoryGeneration++; directoryController?.abort(); clearTimeout(directoryTimer); get("open").focus(); });
  get("save").addEventListener("click", async () => {
    const saveButton = get("save");
    if (saveButton.disabled) return;
    saveButton.disabled = true;
    try {
      // Re-read this browser's favorites so a second tab's changes are retained.
      let currentFavorites = favorites;
      try { currentFavorites = metadata.catalog_paged ? window.ResearchCatalog.favorites(storage, preferences.key(context)) : preferences.read(storage, preferences.key(context), allowed, 40) || favorites; } catch (_) {}
      let selection;
      if (metadata.catalog_paged) {
        selection = preferences.plan(draft, [], allowed);
        selection.favorites = [...new Set([...currentFavorites, ...selection.core])];
        if (selection.favorites.length > 40) throw new Error("自选最多 40 项，请先在指标页移出部分自选，再添加核心指标。");
      } else selection = preferences.plan(draft, currentFavorites, allowed);
      if (metadata.can_edit_shared) {
        const response = await window.researchFetch(`${window.RESEARCH_PREFIX || ""}/api/sn/market/core/config`, {
          method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ series_ids: selection.core }),
        });
        if (!response.ok) throw new Error("工作台核心指标未保存，请核对编辑权限后重试。");
        const saved = await response.json();
        if (JSON.stringify(saved.series_ids) !== JSON.stringify(selection.core)) throw new Error("保存的核心指标范围不一致，请刷新后重试。");
      }
      selected = selection.core; favorites = selection.favorites;
      if (metadata.catalog_paged) restoredCore = [...selected];
      const persisted = preferences.save(storage, context, selection);
      announce(persisted ? `${metadata.can_edit_shared ? "工作台核心指标与排列已保存；" : "当前浏览器排列已保存；"}选中的指标已加入自选。移出核心不会移出自选。` : "浏览器未允许完整保存，本次选择在当前页面有效；请在指标页核对自选。");
      dialog.close(); void loadCards();
    } catch (e) { get("dialog-error").textContent = e.message; get("dialog-error").hidden = false; }
    finally { saveButton.disabled = false; }
  });
  get("retry").addEventListener("click", loadCards);
  syncCount();
  if (metadata.catalog_paged) void restoreSelection();
  else if (JSON.stringify(selected) !== JSON.stringify(metadata.defaults)) void loadCards();
  window.addEventListener("pagehide", () => { generation++; controller?.abort(); restoreGeneration++; restoreController?.abort(); directoryGeneration++; directoryController?.abort(); clearTimeout(directoryTimer); });
  window.addEventListener("pageshow", (event) => { if (event.persisted) { if (metadata.catalog_paged && restoredCore !== null) void restoreSelection(); else void loadCards(); } });
});

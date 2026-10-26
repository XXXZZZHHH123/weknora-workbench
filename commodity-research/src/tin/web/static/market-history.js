document.addEventListener("DOMContentLoaded", () => {
  const open = document.getElementById("market-history-open");
  const dialog = document.getElementById("market-history-dialog");
  if (!open || !dialog) return;
  const metadata = JSON.parse(document.getElementById("market-core-catalog").textContent);
  if (!metadata.can_edit_shared) return;
  const get = (name) => document.getElementById(`market-history-${name}`);
  let catalog = metadata.indicators;
  const allowed = new Set(catalog.map((row) => row.series_id));
  let draft = [];
  let directoryController, directoryGeneration = 0, directoryTimer;
  const normalize = (value) => String(value).normalize("NFKC").toLocaleLowerCase();
  async function render(offset = 0) {
    const query = normalize(get("search").value.trim());
    const choices = get("choices"); choices.replaceChildren();
    let result;
    if (metadata.catalog_paged) {
      directoryController?.abort(); directoryController = new AbortController(); const own = ++directoryGeneration;
      choices.setAttribute("aria-busy", "true");
      try {
        result = await window.ResearchCatalog.read({ search: get("search").value.trim(), offset, signal: directoryController.signal });
        if (own !== directoryGeneration) return;
        catalog = result.indicators; catalog.forEach(row => allowed.add(row.series_id));
      } catch (error) {
        if (own === directoryGeneration && error.name !== "AbortError") { get("error").textContent = error.message; get("error").hidden = false; }
        return;
      } finally { if (own === directoryGeneration) choices.removeAttribute("aria-busy"); }
    }
    for (const row of catalog) {
      if (!metadata.catalog_paged && query && !normalize(`${row.name} ${row.source} ${row.unit}`).includes(query)) continue;
      const label = document.createElement("label"), checkbox = document.createElement("input");
      const content = document.createElement("span"), name = document.createElement("b"), detail = document.createElement("small");
      checkbox.type = "checkbox"; checkbox.value = row.series_id; checkbox.checked = draft.includes(row.series_id);
      checkbox.addEventListener("change", () => {
        get("error").hidden = true;
        if (checkbox.checked && draft.length >= 12) {
          checkbox.checked = false; get("error").textContent = "最多选择 12 项历史行情。"; get("error").hidden = false; return;
        }
        draft = checkbox.checked ? [...draft, row.series_id] : draft.filter((id) => id !== row.series_id);
        get("selection").textContent = `已选择 ${draft.length}/12 项`;
      });
      name.textContent = row.name; detail.textContent = [row.unit, row.source].filter(Boolean).join(" · ");
      content.append(name, detail); label.append(checkbox, content); choices.append(label);
    }
    if (!choices.children.length) choices.textContent = "没有匹配的指标。";
    if (metadata.catalog_paged && (offset || result.next_offset !== null)) {
      for (const [label, page] of [["上一页", offset ? Math.max(0, offset - 200) : null], ["下一页", result.next_offset]]) {
        if (page === null) continue;
        const button = document.createElement("button"); button.type = "button"; button.className = "market-core-button"; button.textContent = label;
        button.addEventListener("click", () => render(page)); choices.append(button);
      }
    }
    get("selection").textContent = `已选择 ${draft.length}/12 项`;
  }
  open.addEventListener("click", () => {
    draft = (metadata.history?.series_ids || []).filter((id) => allowed.has(id));
    get("error").hidden = true; get("search").value = "";
    render(); dialog.showModal(); get("search").focus();
  });
  get("search").addEventListener("input", () => { if (!metadata.catalog_paged) { void render(); return; } clearTimeout(directoryTimer); directoryGeneration++; directoryController?.abort(); directoryTimer = setTimeout(() => render(), 240); });
  get("cancel").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => { directoryGeneration++; directoryController?.abort(); clearTimeout(directoryTimer); open.focus(); });
  get("save").addEventListener("click", async () => {
    const button = get("save"); if (button.disabled) return;
    button.disabled = true; get("error").hidden = true;
    try {
      const response = await window.researchFetch(`${window.RESEARCH_PREFIX || ""}/api/sn/market/history/config`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ series_ids: draft, points_per_series: 180 }),
      });
      if (!response.ok) throw new Error("历史行情未保存，请核对来源和编辑权限后重试。");
      const saved = await response.json();
      if (JSON.stringify(saved.series_ids) !== JSON.stringify(draft) || saved.points_per_series !== 180) throw new Error("保存范围不一致，请刷新后重试。");
      window.location.reload();
    } catch (error) {
      get("error").textContent = error.message || "历史行情未保存，请重试。"; get("error").hidden = false;
    } finally { button.disabled = false; }
  });
});

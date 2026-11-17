/* Explicit personal report editing; observed numbers stay outside this form. */
document.addEventListener("DOMContentLoaded", () => {
  const boot = document.getElementById("report-editor-data"), dialog = document.getElementById("report-editor"), open = document.getElementById("report-edit-open");
  if (!boot || !dialog || !open) return;
  const initial = JSON.parse(boot.textContent), prefix = window.RESEARCH_PREFIX || "";
  const get = (suffix) => document.getElementById(`report-${suffix}`);
  const status = (value) => { get("edit-status").textContent = value; };
  const node = (tag, text, cls) => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (cls) el.className = cls; return el; };
  const labels = { summary: "导语", research: "研判", dashboard: "图表", watch: "策略" };
  let draft, selected = "summary", saving = false, records = [], generation = 0;
  const titles = new Map(), options = new Map((initial.all_figure_options || initial.figure_options).map((x) => [x.series_id, x]));
  const initialAutomatic = initial.all_automatic_figures || initial.automatic_figures || [], extraCharts = new Map();
  const refKey = (r) => JSON.stringify([r.kind, r.id, r.revision]);
  function writeSection() {
    if (!draft) return;
    draft.sections[selected].text = get("edit-text").value;
    draft.sections[selected].visible = get("edit-visible").checked;
  }
  function readSection() {
    get("edit-text").value = draft.sections[selected].text;
    get("edit-visible").checked = draft.sections[selected].visible;
    document.querySelectorAll("[data-report-section]").forEach((b) => b.setAttribute("aria-selected", String(b.dataset.reportSection === selected)));
    renderReferences(); renderFigures();
  }
  function renderReferences() {
    const host = get("ref-list"); host.replaceChildren();
    draft.references.forEach((reference, index) => {
      const row = node("div", undefined, "report-reference-row");
      row.append(node("span", `${titles.get(refKey(reference)) || (reference.kind === "judgment" ? "已保存研判" : "已保存策略")} · v${reference.revision} · ${labels[reference.section]}`));
      const remove = node("button", "移除"); remove.type = "button"; remove.setAttribute("aria-label", "移除此引用");
      remove.addEventListener("click", () => { draft.references.splice(index, 1); renderReferences(); renderFigures(); }); row.append(remove); host.append(row);
    });
  }
  function automaticFigures() {
    const result = new Map();
    for (const figure of initialAutomatic) {
      const refs = (figure.references || []).filter(ref => draft.sections[ref.kind === "judgment" ? "research" : "watch"].visible && (!ref.report_reference || draft.references.some(saved => refKey(saved) === refKey(ref.report_reference))));
      if (figure.references?.length && !refs.length) continue;
      const selectedPin = refs.reduce((chosen, ref) => Number.isInteger(ref.chart_revision) && (!chosen || ref.chart_revision > chosen.chart_revision) ? ref : chosen, null);
      const selectedFigure = {...figure, chart_revision: selectedPin?.chart_revision ?? figure.reference?.revision,
        title: selectedPin?.chart_title || figure.title,
        single_series_id: selectedPin && Object.hasOwn(selectedPin, "single_series_id") ? selectedPin.single_series_id : figure.single_series_id};
      const parts = refs.map(ref => ref.text || ref.label || (ref.kind === "judgment" ? "研判" : `策略「${ref.title || "已保存方案"}」`));
      result.set(figure.key, {...selectedFigure, caption_parts: parts, caption: parts.length ? "引用于：" + [...new Set(parts)].join("、") : figure.caption});
    }
    for (const reference of draft.references) {
      if (!draft.sections[reference.kind === "judgment" ? "research" : "watch"].visible) continue;
      for (const pin of extraCharts.get(refKey(reference)) || []) {
        const key = "chart:" + pin.id, previous = result.get(key), place = reference.kind === "judgment" ? "研判" : `策略「${titles.get(refKey(reference)) || "已保存方案"}」`;
        const caption = place + (pin.report_target_text ? "（" + pin.report_target_text + "）" : "");
        const curves = pin.definition?.curves || [], single = curves.length === 1 ? curves[0].series_id : null;
        const parts = [...new Set([...(previous?.caption_parts || []), caption])];
        result.set(key, {...(previous && (previous.chart_revision ?? previous.reference?.revision) >= pin.revision ? previous : {key, title: pin.title, reference: pin, chart_revision: pin.revision, single_series_id: single}), caption_parts: parts, caption: "引用于：" + parts.join("、")});
      }
    }
    return [...result.values()];
  }
  function renderFigures() {
    const automatic = automaticFigures(), excluded = new Set(automatic.map(figure => figure.single_series_id).filter(Boolean));
    const readonly = get("automatic-figures"); readonly?.replaceChildren();
    for (const figure of automatic) {
      const row = node("div", undefined, "report-automatic-figure");
      row.append(node("strong", figure.title), node("small", figure.caption)); readonly?.append(row);
    }
    if (!automatic.length) readonly?.append(node("p", "当前没有引用图。", "report-muted"));
    const selectionSummary = () => {
      const repeated = draft.figures.filter(figure => excluded.has(figure.series_id)).length;
      return `已选 ${draft.figures.length} 条，其中 ${repeated} 条与引用图重复并合并展示；原手选与图说保留，手选上限仍为 6 条。`;
    };
    const updateCount = () => { const count = get("figure-count"); if (count) count.textContent = selectionSummary(); };
    updateCount();
    const host = get("figure-options"); host.replaceChildren();
    draft.figures.forEach((f) => { if (!options.has(f.series_id)) options.set(f.series_id, { series_id: f.series_id, name: "引用指标 · " + f.series_id.slice(-10) }); });
    const candidates = [...options.entries()].filter(([id]) => !excluded.has(id));
    if (!candidates.length) { host.append(node("p", "没有另外可选的已绑定指标；引用图中的单曲线指标已去重。", "report-muted")); return; }
    candidates.forEach(([identifier, option]) => {
      const row = node("div", undefined, "report-figure-option"), label = node("label", undefined, "report-check"), check = node("input"); check.type = "checkbox";
      const selectedFigure = draft.figures.find((f) => f.series_id === identifier); check.checked = Boolean(selectedFigure);
      label.append(check, document.createTextNode(option.name + (option.unit ? " · " + option.unit : ""))); label.title = identifier;
      const caption = node("input"); caption.type = "text"; caption.maxLength = 600; caption.placeholder = "图说（可留空）"; caption.value = selectedFigure?.caption || ""; caption.disabled = !selectedFigure;
      caption.setAttribute("aria-label", `${option.name}的图说`);
      check.addEventListener("change", () => {
        if (check.checked) {
          if (draft.figures.length >= 6) { check.checked = false; status("手选已达 6 条上限。" + selectionSummary() + "请先取消一条手选。"); return; }
          draft.figures.push({ series_id: identifier, caption: caption.value });
        } else draft.figures = draft.figures.filter((f) => f.series_id !== identifier);
        caption.disabled = !check.checked; updateCount(); status("");
      });
      caption.addEventListener("input", () => { const figure = draft.figures.find((f) => f.series_id === identifier); if (figure) figure.caption = caption.value; });
      row.append(label, caption); host.append(row);
    });
  }
  async function loadReferences() {
    const own = ++generation, kind = get("ref-kind").value, select = get("ref-record");
    select.replaceChildren(new Option("正在读取已有记录…", ""));
    try {
      const url = `${prefix}/api/sn/research-references?kind=${encodeURIComponent(kind)}&limit=50&offset=0`;
      const response = await fetch(url, { credentials: "same-origin" });
      if (!response.ok) throw new Error("已有记录暂时无法读取。");
      const body = await response.json(); if (own !== generation) return;
      if (!Array.isArray(body.items)) throw new Error("已有记录响应无效。");
      records = body.items; select.replaceChildren(new Option(records.length ? "选择记录与版本" : "暂无可引用记录", ""));
      records.forEach((r, i) => { if (r.kind !== kind || typeof r.id !== "string" || !Number.isInteger(r.revision)) return; titles.set(refKey(r), r.title); select.append(new Option(`${r.title} · v${r.revision}`, String(i))); });
      if (body.truncated) status("当前列出最近50条记录；引用保留所选记录的明确版本。");
    } catch (error) { if (own === generation) { select.replaceChildren(new Option("读取未完成", "")); status(error.message); } }
  }
  get("ref-add").addEventListener("click", async () => {
    const value = get("ref-record").value, record = value === "" ? null : records[Number(value)];
    if (!record) { status("请先选择已有研判或策略。"); return; }
    if (draft.references.length >= 8) { status("每份报告最多引用8条记录。"); return; }
    const reference = { kind: record.kind, id: record.id, revision: record.revision, section: selected };
    if (draft.references.some((r) => refKey(r) === refKey(reference) && r.section === selected)) { status("本节已经引用此版本。"); return; }
    const button = get("ref-add"); button.disabled = true;
    try {
      const response = await fetch(`${prefix}/api/sn/research-references/${encodeURIComponent(record.kind)}/${encodeURIComponent(record.id)}?revision=${record.revision}`, { credentials: "same-origin" });
      if (!response.ok) throw new Error("所选版本或权限已变化，请重新选择。");
      const actual = await response.json();
      if (actual.id !== record.id || actual.revision !== record.revision || actual.kind !== record.kind) throw new Error("引用版本不一致，未替换为最新内容。");
      draft.references.push(reference); titles.set(refKey(record), actual.title);
      extraCharts.set(refKey(record), actual.chart_references || []);
      (actual.series_ids || []).forEach((id) => { if (!options.has(id)) options.set(id, { series_id: id, name: "引用指标 · " + id.slice(-10) }); });
      renderReferences(); renderFigures(); status("已加入引用，保存时将保留这个版本。");
    } catch (error) { status(error.message); } finally { button.disabled = false; }
  });
  document.querySelectorAll("[data-report-section]").forEach((button) => button.addEventListener("click", () => { writeSection(); selected = button.dataset.reportSection; readSection(); }));
  open.addEventListener("click", () => {
    extraCharts.clear(); draft = structuredClone(initial.draft); selected = "summary";
    if (!initial.id && typeof initial.default_title === "string" && initial.default_title.trim()) draft.title = initial.default_title.trim().slice(0, 120);
    get("edit-title").value = draft.title;
    status(""); readSection(); renderFigures(); dialog.showModal(); loadReferences();
  });
  const close = () => { if (!saving) { generation++; dialog.close(); } };
  get("edit-close").addEventListener("click", close); get("edit-cancel").addEventListener("click", close);
  dialog.addEventListener("cancel", (event) => { if (saving) event.preventDefault(); });
  get("edit-visible").addEventListener("change", () => { writeSection(); renderFigures(); });
  get("ref-kind").addEventListener("change", loadReferences);
  get("editor-form").addEventListener("submit", async (event) => {
    event.preventDefault(); if (saving) return; writeSection(); draft.title = get("edit-title").value.trim();
    if (!draft.title) { status("请填写报告标题。"); return; }
    saving = true; get("edit-save").disabled = true; status("正在保存个人草稿…");
    try {
      const payload = { draft }; if (initial.revision) payload.revision = initial.revision;
      const url = `${prefix}/api/sn/report-drafts${initial.id ? "/" + encodeURIComponent(initial.id) : ""}`;
      const response = await fetch(url, { method: initial.id ? "PATCH" : "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      if (!response.ok) {
        let message = response.status === 409 ? "已有新版本或引用变化，请保留本地文字并重新打开报告。" : "保存未完成，请核对权限、引用与选图。";
        try { const body = await response.json(); if (typeof body.detail === "string") message = body.detail; } catch {}
        throw new Error(message);
      }
      const saved = await response.json();
      if (!saved.id || !Number.isInteger(saved.revision)) throw new Error("保存响应不完整，暂不覆盖本地文字。");
      status("已保存"); location.reload();
    } catch (error) { status(error.message); } finally { saving = false; get("edit-save").disabled = false; }
  });
});

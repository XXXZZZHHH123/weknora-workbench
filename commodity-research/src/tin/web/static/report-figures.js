/* Report-only, bounded native SVG charts. The browser never supplies series IDs. */
document.addEventListener("DOMContentLoaded", () => {
  const host = document.getElementById("report-figures");
  if (!host) return;
  if (host.dataset.seriesCount === "0") {
    host.textContent = "未选择报告图表，可在编辑报告中选择已有绑定指标。";
    return;
  }
  const esc = (s) => String(s ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
  const fmt = (v) => new Intl.NumberFormat("zh-CN", { maximumSignificantDigits: 10 }).format(v);
  const el = (tag, cls, value) => { const node = document.createElement(tag); node.className = cls || ""; if (value !== undefined) node.textContent = value; return node; };
  let loaded = false;
  async function load() {
    if (loaded) return;
    loaded = true;
    const request = new URL(host.dataset.url, location.origin);
    request.searchParams.set("date", host.dataset.day);
    request.searchParams.set("period", host.dataset.period);
    if (host.dataset.revision) request.searchParams.set("revision", host.dataset.revision);
    try {
      const response = await fetch(request, { credentials: "same-origin" });
      if (!response.ok) throw new Error(response.status === 409 ? "报告选图或来源已有变化，请重新打开。" : "图表暂时无法读取，请稍后重试。");
      const data = await response.json();
      const limit = host.dataset.period === "weekly" ? 120 : 90;
      if (!Array.isArray(data.series) || data.series.length > 6 || data.points_per_series !== limit) throw new Error("图表响应超出报告范围。");
      data.series.forEach((s) => {
        if (!Array.isArray(s.points) || s.points.length > limit || s.points.some((p) => !Number.isFinite(p.value) || !Number.isFinite(p.x) || !Number.isFinite(p.y) || !/^\d{4}-\d{2}-\d{2}$/.test(p.date))) throw new Error("图表含无效观测。");
      });
      host.replaceChildren();
      if (!data.series.length) { host.append(el("p", "report-figure-state", "未选择报告图表，可在编辑报告中选择已有绑定指标。")); return; }
      data.series.forEach((s, index) => {
        const figure = el("figure", "report-figure"), header = el("header");
        header.append(el("h4", "", s.name), el("small", "", s.unit || "原表未注明单位"));
        figure.append(header);
        if (s.points.length) {
          const mid = s.min / 2 + s.max / 2;
          const grid = [[16, s.max], [72, mid], [128, s.min]].map(([y, v]) => `<line x1="52" y1="${y}" x2="484" y2="${y}" stroke="var(--line)" stroke-width=".8"/><text x="45" y="${y + 3}" text-anchor="end" fill="var(--text-muted)" font-size="9">${esc(fmt(v))}</text>`).join("");
          const path = s.path && !s.caliber_changed && !s.invalid_points ? `<polyline points="${s.points.map((p) => `${p.x},${p.y}`).join(" ")}" fill="none" stroke="var(--primary)" stroke-width="1.8" stroke-linejoin="round"/>` : "";
          const dots = s.points.map((p) => `<circle cx="${p.x}" cy="${p.y}" r="2.6" fill="var(--primary)"><title>${esc(p.date)} · ${esc(fmt(p.value))} ${esc(s.unit)}</title></circle>`).join("");
          const first = s.points[0], last = s.points[s.points.length - 1];
          const dates = first.date === last.date ? `<text x="268" y="151" text-anchor="middle" fill="var(--text-muted)" font-size="9">${esc(first.date)}</text>` : `<text x="52" y="151" fill="var(--text-muted)" font-size="9">${esc(first.date)}</text><text x="484" y="151" text-anchor="end" fill="var(--text-muted)" font-size="9">${esc(last.date)}</text>`;
          const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
          svg.setAttribute("viewBox", "0 0 510 164"); svg.setAttribute("role", "img"); svg.setAttribute("aria-label", `${s.name}，${first.date}至${last.date}，${s.points.length}个真实观测`);
          svg.innerHTML = `${grid}${path}${dots}${dates}`;
          figure.append(svg);
        } else figure.append(el("p", "report-figure-state", "截至所选日无可用观测"));
        const caption = el("figcaption");
        if (s.caption) caption.append(el("strong", "", s.caption));
        caption.append(document.createTextNode(`${s.source || "已登记来源"} · ${s.points.length}个有效观测${s.notice ? " · " + s.notice : ""}`));
        figure.append(caption); host.append(figure);
      });
    } catch (error) {
      const state = el("div", "report-figure-state", error.message || "图表读取未完成。");
      const retry = el("button", "report-button", "重试"); retry.type = "button";
      retry.addEventListener("click", () => { loaded = false; load(); }); state.append(retry); host.replaceChildren(state);
    }
  }
  if ("IntersectionObserver" in window) {
    const observer = new IntersectionObserver((entries) => { if (entries.some((x) => x.isIntersecting)) { observer.disconnect(); load(); } }, { rootMargin: "500px" });
    observer.observe(host);
  } else load();
});

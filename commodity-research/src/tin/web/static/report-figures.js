/* Saved report chart list; each visible figure reads through the report's authorized endpoint. */
document.addEventListener("DOMContentLoaded", () => {
  const host = document.getElementById("report-figures"), boot = document.getElementById("report-figure-data");
  if (!host || host.dataset.live !== "true" || !boot) return;
  const figures = JSON.parse(boot.textContent);
  const esc = (s) => String(s ?? "").replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;");
  const fmt = (v) => window.ChartKit?.formatNumber ? window.ChartKit.formatNumber(v) : String(v);
  const el = (tag, cls, value) => { const node = document.createElement(tag); node.className = cls || ""; if (value !== undefined) node.textContent = value; return node; };
  function renderIndicator(plot, s) {
    const limit = host.dataset.period === "weekly" ? 120 : 90;
    if (!s || !Array.isArray(s.points) || s.points.length > limit || s.points.some((p) => !Number.isFinite(p.value) || !Number.isFinite(p.x) || !Number.isFinite(p.y) || !/^\d{4}-\d{2}-\d{2}$/.test(p.date))) throw new Error("图表含无效观测。");
    if (s.points.length) {
      const color = window.ChartPalette.color(0, s.color);
      const mid = s.min / 2 + s.max / 2;
      const grid = (s.max === s.min ? [[72, s.max]] : [[16, s.max], [72, mid], [128, s.min]]).map(([y, v]) => `<line x1="52" y1="${y}" x2="484" y2="${y}" stroke="var(--line)" stroke-width=".8"/><text x="45" y="${y + 3}" text-anchor="end" fill="var(--text-muted)" font-size="9">${esc(fmt(v))}</text>`).join("");
      const path = s.path && !s.caliber_changed && !s.invalid_points ? `<polyline points="${s.points.map((p) => `${p.x},${p.y}`).join(" ")}" fill="none" stroke="${color}" stroke-width="1.8" stroke-linejoin="round"/>` : "";
      const dots = s.points.map((p) => `<circle cx="${p.x}" cy="${p.y}" r="2.6" fill="${color}"><title>${esc(p.date)} · ${esc(fmt(p.value))} ${esc(s.unit)}</title></circle>`).join("");
      const first = s.points[0], last = s.points[s.points.length - 1];
      const dates = first.date === last.date ? `<text x="268" y="151" text-anchor="middle" fill="var(--text-muted)" font-size="9">${esc(first.date)}</text>` : `<text x="52" y="151" fill="var(--text-muted)" font-size="9">${esc(first.date)}</text><text x="484" y="151" text-anchor="end" fill="var(--text-muted)" font-size="9">${esc(last.date)}</text>`;
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("viewBox", "0 0 510 164"); svg.setAttribute("role", "img"); svg.setAttribute("aria-label", `${s.name}，${first.date}至${last.date}，${s.points.length}个真实观测`);
      svg.innerHTML = `${grid}${path}${dots}${dates}`;
      plot.append(svg);
    } else plot.append(el("p", "report-figure-state", "截至所选日无可用观测"));
  }
  const pending = [], queued = new Set();
  let active = false;
  async function drain() {
    if (active) return;
    active = true;
    while (pending.length) {
      const {card, figure} = pending.shift(), plot = card.querySelector("[data-figure-plot]");
      plot.replaceChildren(el("p", "report-figure-state", "正在读取图表…"));
      const request = new URL(host.dataset.url + "/" + encodeURIComponent(figure.key), location.origin);
      request.searchParams.set("date", host.dataset.day); request.searchParams.set("period", host.dataset.period);
      if (host.dataset.revision) request.searchParams.set("revision", host.dataset.revision);
      try {
        const response = await fetch(request, {credentials: "same-origin"});
        const data = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(typeof data.detail === "string" ? data.detail : response.status === 409 ? "报告选图或来源已有变化，请重新打开。" : "图表暂时无法读取，请稍后重试。");
        if (data.status === "unavailable" || data.error || data.reason) throw new Error(data.notice || data.figure?.notice || data.error || data.reason || "引用图表暂不可读。");
        if (data.figure?.key && data.figure.key !== figure.key) throw new Error("图表身份已变化，请重新打开。");
        plot.replaceChildren();
        const actual = data.figure || figure;
        card.querySelector("[data-figure-metadata]").textContent = `单位：${actual.unit || "原表未注明单位"} · 数据截至 ${actual.as_of || host.dataset.day}`;
        if (figure.kind === "chart") {
          const rendered = data.rendered;
          if (!rendered?.payload || !window.ChartKit) throw new Error("引用图表未返回可渲染数据。");
          window.ChartKit.render(plot, rendered.payload, {definition: rendered.definition, overlays: rendered.overlays || [], width: Math.max(240, Math.floor(plot.clientWidth || 800)), height: 300});
          card.querySelector("[data-figure-notice]").textContent = [rendered.notice, rendered.data_notice, rendered.dedup_notice].filter(Boolean).join("；");
        } else {
          const series = Array.isArray(data.series) ? data.series[0] : data.series;
          renderIndicator(plot, series);
          card.querySelector("[data-figure-notice]").textContent = [series.source, `${series.points.length}个有效观测`, series.notice].filter(Boolean).join(" · ");
        }
      } catch (error) {
        const state = el("p", "report-figure-state", error.message || "图表读取未完成。"), retry = el("button", "report-button", "重试");
        retry.type = "button"; retry.addEventListener("click", () => { queued.delete(card); enqueue(card, figure); });
        state.append(retry); plot.replaceChildren(state);
      }
    }
    active = false;
  }
  function enqueue(card, figure) { if (queued.has(card)) return; queued.add(card); pending.push({card, figure}); drain(); }
  const byKey = new Map(figures.map(figure => [figure.key, figure]));
  const cards = [...host.querySelectorAll("[data-report-figure]")];
  if ("IntersectionObserver" in window) {
    const observer = new IntersectionObserver(entries => { for (const entry of entries) { if (!entry.isIntersecting) continue; observer.unobserve(entry.target); const figure = byKey.get(entry.target.dataset.reportFigure); if (figure) enqueue(entry.target, figure); } }, {rootMargin: "0px"});
    cards.forEach(card => observer.observe(card));
  } else cards.forEach(card => { const figure = byKey.get(card.dataset.reportFigure); if (figure) enqueue(card, figure); });
});

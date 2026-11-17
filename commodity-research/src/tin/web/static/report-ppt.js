/* Rasterize the report's ordered chart manifest, then download a server-authored deck. */
document.addEventListener("DOMContentLoaded", () => {
  "use strict";
  const $ = id => document.getElementById(id), boot = $("report-ppt-data"), opener = $("report-ppt-open");
  if (!boot || !opener) return;
  const config = JSON.parse(boot.textContent), manifest = JSON.parse($("report-figure-data")?.textContent || "[]");
  const dialog = $("report-ppt-dialog"), form = $("report-ppt-form"), fields = $("report-ppt-fields");
  const status = $("report-ppt-status"), progress = $("report-ppt-progress"), generate = $("report-ppt-generate");
  let job = null;
  function aborted(signal) { if (signal.aborted) throw new DOMException("生成已取消", "AbortError"); }
  function controls(busy) {
    fields.disabled = busy; generate.disabled = busy;
    $("report-ppt-cancel").textContent = busy ? "取消生成" : "取消";
  }
  function cancel() {
    job?.abort(); dialog.close();
  }
  opener.addEventListener("click", () => { if (!job) status.textContent = ""; dialog.showModal(); });
  $("report-ppt-close").addEventListener("click", cancel);
  $("report-ppt-cancel").addEventListener("click", cancel);
  dialog.addEventListener("cancel", event => { event.preventDefault(); cancel(); });
  window.addEventListener("pagehide", () => job?.abort(), {once: true});
  async function readJSON(response, fallback) {
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw Object.assign(new Error(typeof data.detail === "string" ? data.detail : fallback), {status: response.status});
    return data;
  }
  async function request(url, options) {
    try { return await fetch(url, options); }
    catch (error) {
      if (error.name === "AbortError") throw error;
      throw new Error("网络连接失败，请检查连接后重试。");
    }
  }
  async function draw(figure, density, signal) {
    const url = new URL(config.figures_url + "/" + encodeURIComponent(figure.key), location.origin);
    url.searchParams.set("date", config.date); url.searchParams.set("period", config.period);
    if (config.revision !== null) url.searchParams.set("revision", config.revision);
    const response = await request(url, {credentials: "same-origin", signal});
    const data = await readJSON(response, "图表暂时无法读取，请稍后重试。");
    aborted(signal);
    if (data.figure?.key !== figure.key) throw Object.assign(new Error("报告内容已变化，请刷新后重试"), {status: 409});
    if (data.status === "unavailable" || data.error || data.reason) throw new Error(data.notice || data.figure?.notice || "引用图表暂不可读。");
    if (!window.ChartKit) throw new Error("图表绘制组件未加载，请刷新后重试。");
    // Keep these image regions aligned with tin.ppt.report_deck.chart_slots (inches).
    // One CSS pixel represents one PowerPoint point; 11px export labels stay 11pt.
    // ChartKit rasterizes the SVG at 2x, independently of this layout scale.
    const width = (density === 1 ? 12.533 : 6.1165) * 72, height = (density === 4 ? 1.9 : 4.35) * 72;
    const target = document.createElement("div"); target.className = "report-ppt-render-target";
    target.style.width = width + "px"; target.style.height = height + "px"; document.body.append(target);
    let renderer;
    try {
      let payload, definition, overlays = [], renderTitle = figure.title;
      if (figure.kind === "chart") {
        if (!data.rendered?.payload) throw new Error("引用图表未返回可渲染数据。");
        ({payload, definition, overlays = []} = data.rendered);
      } else {
        const series = Array.isArray(data.series) ? data.series[0] : data.series;
        const limit = config.period === "weekly" ? 120 : 90;
        if (!series || !Array.isArray(series.points) || series.points.length > limit || series.points.some(point => !Number.isFinite(point.value) || !/^\d{4}-\d{2}-\d{2}$/.test(point.date) || point.date > config.date)) throw new Error("指标图含无效观测，请刷新核对。");
        if (!series.points.length) throw new Error("截至所选日无可用观测。");
        const disconnected = series.caliber_changed || series.invalid_points || !series.path;
        // Null separators keep every real observation as an isolated point,
        // matching the report's refusal to join incomparable observations.
        const points = disconnected ? series.points.flatMap(point => [point, {date: point.date, value: null}]) : series.points;
        if (disconnected) renderTitle += "\n" + (series.notice || "观测不可连线，仅展示有效数据点");
        payload = {unit: series.unit, curves: [{series_id: figure.series_id, label: series.name || figure.title,
          unit: series.unit, kind: "line", axis: "left", points}]};
        definition = {title: figure.title};
      }
      renderer = window.ChartKit.render(target, payload, {width, height, exactSize: true, export: true, definition, overlays, title: renderTitle});
      const blob = await renderer.toPNGBlob(); aborted(signal);
      if (blob.type !== "image/png" || !blob.size) throw new Error("未能生成有效 PNG 图片。");
      if (blob.size > 4 * 1024 * 1024) throw new Error("此图生成的 PNG 超过 4 MB，将保留失败占位。");
      return blob;
    } finally { renderer?.destroy(); target.remove(); }
  }
  function filename(response) {
    const header = response.headers.get("Content-Disposition") || "", encoded = /filename\*=UTF-8''([^;]+)/i.exec(header);
    if (encoded) { try { return decodeURIComponent(encoded[1].trim()); } catch (_) { /* Fall back to a plain name. */ } }
    return "研究报告.pptx";
  }
  form.addEventListener("submit", async event => {
    event.preventDefault(); if (job) return;
    const controller = new AbortController(); job = controller; controls(true);
    const signal = controller.signal, density = Number($("report-ppt-density").value);
    const sections = Object.fromEntries(Object.keys(config.sections).map(key => [key, config.sections[key] === true && form.elements[key].checked]));
    const figures = sections.dashboard ? manifest : [], body = new FormData(), warnings = [];
    body.append("options", JSON.stringify({sections, figures_per_slide: density}));
    body.append("revision", JSON.stringify(config.revision)); body.append("keys", JSON.stringify(figures.map(figure => figure.key)));
    progress.max = Math.max(1, figures.length); progress.value = 0; progress.hidden = false;
    try {
      if (!config.frozen) for (let i = 0; i < figures.length; i++) {
        aborted(signal); status.textContent = `正在绘制第 ${i + 1}/${figures.length} 张图`;
        const figure = figures[i];
        try { body.append("images", await draw(figure, density, signal), "figure.png"); }
        catch (error) {
          if (error.name === "AbortError" || error.status === 409 || error.status === 403) throw error;
          const reason = error instanceof TypeError ? "网络连接失败或图表数据无法解析，请稍后重试。" : error.message || "图表绘制失败。";
          warnings.push(figure.title + "：" + reason); body.append("failures", JSON.stringify({key: figure.key, reason}));
        }
        progress.value = i + 1;
      }
      aborted(signal);
      status.textContent = "正在生成 PPT…" + (warnings.length ? ` ${warnings.length} 张图读取失败，将保留占位：${warnings.join("；")}` : "");
      const response = await request(config.url, {method: "POST", credentials: "same-origin", body, signal});
      if (!response.ok) await readJSON(response, "PPT 生成失败，请稍后重试。");
      const blob = await response.blob(); aborted(signal);
      const url = URL.createObjectURL(blob), link = document.createElement("a");
      link.href = url; link.download = filename(response); document.body.append(link); link.click(); link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      status.textContent = "PPT 已生成并开始下载。" + (warnings.length ? ` ${warnings.length} 张图已保留失败占位：${warnings.join("；")}` : "");
      progress.value = progress.max;
    } catch (error) {
      status.textContent = error.name === "AbortError" ? "生成已取消。" : error instanceof TypeError ? "网络连接失败，请稍后重试。" : error.message || "PPT 生成失败，请稍后重试。";
    } finally { job = null; controls(false); progress.hidden = true; }
  });
});

/* Isolated browser fixtures; RESEARCH_PYTHON supplies the Jinja interpreter. */
const F = require('./chart-ui-fixture.cjs');
const seriesId = 'kb-' + 'a'.repeat(48), otherSeriesId = 'kb-' + 'b'.repeat(48);
function slot(id, kind, x, y, width, height, extra = {}) {
  const position = {x: x * 9525, y: y * 9525, cx: width * 9525, cy: height * 9525};
  return {slot_id: id, slide: 1, name: '合成图位 ' + id, kind, position, page_position: position,
    replaceable: true, preserve_reason: null, texts: [], pairs: [], ...extra};
}
function pptRecord() {
  return {id: 'template-1', title: '合成模板 <img src=x onerror=window.__xss=1>', filename: 'synthetic.pptx',
    revision: 1, slide_count: 2, slot_count: 5, updated_at: '2029-09-15T08:00:00Z',
    presentation: {width_emu: 1000 * 9525, height_emu: 500 * 9525, slides: [
      {number: 1, title: '第1页 <script>window.__xss=1</script>', slots: [
        slot('chart-slot', 'chart', 30, 40, 400, 180, {texts: Array.from({length: 25}, (_, i) => i ? '合成文字' + i : '<img onerror=window.__xss=1>')}),
        slot('table-slot', 'table', 550, 40, 280, 150, {pairs: [1, 2, 3].map(i => ({date: '2029-09-0' + i, value: String(i)}))}),
        slot('keep-slot', 'image', 30, 270, 240, 120), slot('pending-slot', 'image', 550, 270, 240, 120)]},
      {number: 2, title: '第2页 2029年', slots: [slot('native-slot', 'image', 30, 40, 400, 180,
        {slide: 2, replaceable: false, preserve_reason: 'native_chart'})]}]},
    bindings: {'keep-slot': {kind: 'keep'}}, candidates: {
      'chart-slot': [{kind: 'chart', chart_id: 'chart-1', title: '首位候选 <img onerror=window.__xss=1>', score: .85},
        {kind: 'chart', chart_id: 'chart-2', title: '第二候选', score: .5}],
      'table-slot': [{kind: 'series', series_id: seriesId, name: '合成指标 <b>文本</b>', rows: 3, matched: 3, total: 3,
        score: 1, numerically_verified: true, label: '数值已核实'}]}, exports: [], warnings: []};
}
function render(template, overrides = {}) {
  F.assert.ok(process.env.RESEARCH_PYTHON, '请用 RESEARCH_PYTHON 指定可导入 Jinja 的 Python');
  const config = {enabled: true, can_write: true, id: 'template-1', write_reason: '', ...overrides};
  return F.render(template, config, {ppt_page: config});
}
function multipart(entry) {
  const boundary = /boundary=(?:"([^"]+)"|([^;]+))/.exec(entry.headers['content-type']);
  F.assert.ok(boundary, '导出必须使用 multipart/form-data');
  const parts = entry.rawBody.toString('latin1').split('--' + (boundary[1] || boundary[2])).slice(1, -1), result = new Map();
  for (const part of parts) {
    const split = part.indexOf('\r\n\r\n'), headers = part.slice(0, split), name = /\bname="([^"]+)"/.exec(headers)[1];
    result.set(name, Buffer.from(part.slice(split + 4, -2), 'latin1'));
  }
  return result;
}
module.exports = {...F, render, slot, pptRecord, seriesId, otherSeriesId, multipart};

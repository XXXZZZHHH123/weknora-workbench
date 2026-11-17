/* Report PPT menu, real PNG rasterization, multipart contract and browser download. */
const {assert, render, launch, open, prefix, definition, cycle} = require('./chart-ui-fixture.cjs');
const {Buffer} = require('node:buffer');
const figures = [
  {key: 'chart:one', kind: 'chart', title: '研判引用图', caption: '引用于：研判', unit: '吨', as_of: '2029-09-15'},
  {key: 'indicator:two', kind: 'indicator', series_id: 'two', title: '手选指标', caption: '研究员图说', unit: '元', as_of: '2029-09-15'},
  {key: 'chart:missing', kind: 'chart', title: '无法读取的图', caption: '引用于：策略', as_of: '2029-09-15'},
];
function report() {
  const panels = Object.fromEntries(['summary', 'research', 'dashboard', 'watch'].map(key => [key, {visible: key !== 'watch', text: '', references: []}]));
  return {report_title: '合成报告', coverage: {}, panels, is_manual_report: true, manual_report: {id: 'report-one', revision: 3},
    briefs: [], judgment: null, conclusion: null, judgment_replay: null, strategy_overview: {text: '暂无策略', triggered: []},
    research_cards: [], strategies: [], strategy_events: [], changes: [], gaps: [], derived: [], observations: [], counts: {observations: 0, derived: 0},
    selected_figures: [], report_figures: figures, automatic_figures: figures.filter(figure => figure.kind === 'chart'), figure_options: [],
    editable_draft: {format: 'research-report-draft.v1', title: '合成报告', date: '2029-09-15', period: 'weekly', references: [], figures: [],
      sections: Object.fromEntries(Object.entries(panels).map(([key, panel]) => [key, {visible: panel.visible, text: ''}]))}};
}
const html = (live = true, config = {}) => render('reports.html', config, {report: report(), live_report: live, files: [], selected: {date: '2029-09-15'}, period_kind: 'weekly', report_start: '2029-09-10', preview: '{}', restricted_data: {briefs: []}})
  .replace('</head>', '<style>#report-research{min-height:1400px}</style></head>');
async function submitted(entry) {
  return await new Response(entry.rawBody, {headers: {'Content-Type': entry.headers['content-type']}}).formData();
}
function assertPNG(blob, width, height) {
  return blob.arrayBuffer().then(value => {
    const bytes = Buffer.from(value);
    assert.equal(blob.type, 'image/png'); assert.equal(bytes.subarray(0, 8).toString('hex'), '89504e470d0a1a0a');
    assert.equal(bytes.readUInt32BE(16), Math.round(width * 72 * 2)); assert.equal(bytes.readUInt32BE(20), Math.round(height * 72 * 2));
  });
}
const downloaded = entry => entry.route.fulfill({status: 200, contentType: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  headers: {'Content-Disposition': "attachment; filename*=UTF-8''" + encodeURIComponent('合成研究台 周报.pptx')}, body: Buffer.from('PK synthetic deck')});
async function openDialog(page) {
  await page.locator('.report-export-menu > summary').click(); await page.getByRole('menuitem', {name: '导出为 PPT', exact: true}).click();
  await page.locator('#report-ppt-dialog').waitFor({state: 'visible'});
}
(async () => {
  const browser = await launch();
  try {
    let upload;
    const fixture = await open(browser, prefix + '/sn/reports', html(), async entry => {
      const {path, url, reply} = entry;
      if (path === '/research-references') return reply({items: []});
      if (path.startsWith('/reports/figures/')) {
        assert.equal(url.searchParams.get('revision'), '3'); assert.equal(url.searchParams.get('period'), 'weekly');
        const figure = figures.find(figure => figure.key === decodeURIComponent(path.split('/').at(-1)));
        if (figure.key === 'chart:missing') return reply({figure, status: 'unavailable', notice: '合成来源已撤回'});
        if (figure.kind === 'chart') return reply({figure, rendered: {definition, payload: cycle()}});
        return reply({figure, series: {name: '手选指标', unit: '元', path: '52,128 484,16', points: [{date: '2029-09-01', value: 2}, {date: '2029-09-15', value: 8}]}});
      }
      if (path === '/reports/2029-09-15/weekly/ppt') { upload = await submitted(entry); return downloaded(entry); }
      return false;
    });
    const page = fixture.page;
    await openDialog(page);
    assert.equal(await page.locator('#report-ppt-form input[value="one_page"]').isChecked(), true);
    let defaultDownload = page.waitForEvent('download');
    await page.getByRole('button', {name: '生成并下载', exact: true}).click(); await defaultDownload;
    assert.deepEqual(JSON.parse(upload.get('options')), {mode: 'one_page', figures_per_slide: 2});
    assert.deepEqual(JSON.parse(upload.get('keys')), []); assert.equal(upload.getAll('images').length, 0);
    assert.equal(await page.locator('.report-ppt-render-target').count(), 0);
    await page.locator('#report-ppt-form input[value="evidence"]').check();
    assert.equal(await page.getByLabel('证据附录每页图数', {exact: true}).inputValue(), '2');
    const downloadPromise = page.waitForEvent('download');
    await page.getByRole('button', {name: '生成并下载', exact: true}).click();
    const download = await downloadPromise;
    assert.equal(download.suggestedFilename(), '合成研究台 周报.pptx');
    await page.locator('#report-ppt-status').filter({hasText: '已生成并开始下载'}).waitFor();
    assert.deepEqual(JSON.parse(upload.get('options')), {mode: 'evidence', figures_per_slide: 2});
    assert.equal(upload.get('revision'), '3'); assert.deepEqual(JSON.parse(upload.get('keys')), figures.map(figure => figure.key));
    assert.equal(upload.getAll('images').length, 2); assert.equal(upload.getAll('failures').length, 1);
    assert.deepEqual(JSON.parse(upload.get('failures')), {key: 'chart:missing', reason: '合成来源已撤回'});
    await Promise.all(upload.getAll('images').map(blob => assertPNG(blob, 6.1165, 4.35)));
    assert.match(await page.locator('#report-ppt-status').innerText(), /合成来源已撤回/);
    assert.equal(await page.locator('.report-ppt-render-target').count(), 0, 'offscreen raster targets are cleaned');
    for (const density of [1, 2]) {
      await page.getByRole('button', {name: '生成并下载', exact: true}).waitFor({state: 'visible'});
      await page.getByLabel('证据附录每页图数', {exact: true}).selectOption(String(density));
      const layoutDownload = page.waitForEvent('download'); await page.getByRole('button', {name: '生成并下载', exact: true}).click(); await layoutDownload;
      assert.equal(JSON.parse(upload.get('options')).figures_per_slide, density);
      assert.equal(upload.getAll('images').length, 2);
      await Promise.all(upload.getAll('images').map(blob => assertPNG(blob, density === 1 ? 12.533 : 6.1165, 4.35)));
    }
    await page.locator('#report-ppt-form input[value="full"]').check();
    const fullDownload = page.waitForEvent('download'); await page.getByRole('button', {name: '生成并下载', exact: true}).click(); await fullDownload;
    assert.equal(JSON.parse(upload.get('options')).mode, 'full');
    assert.deepEqual(JSON.parse(upload.get('keys')), figures.map(figure => figure.key));
    await page.locator('#report-ppt-form input[value="one_page"]').check();
    const noChartsDownload = page.waitForEvent('download'); await page.getByRole('button', {name: '生成并下载', exact: true}).click(); await noChartsDownload;
    assert.deepEqual(JSON.parse(upload.get('keys')), []); assert.equal(upload.getAll('images').length, 0);
    await fixture.close();

    let frozenUpload;
    const frozen = await open(browser, prefix + '/sn/reports', html(false, {can_write: false}), async entry => {
      if (entry.path === '/reports/2029-09-15/weekly/ppt') { frozenUpload = await submitted(entry); return downloaded(entry); }
      throw new Error('冻结归档不得重新读取图表');
    });
    await openDialog(frozen.page); await frozen.page.locator('#report-ppt-form input[value="evidence"]').check(); assert.match(await frozen.page.locator('#report-ppt-dialog').innerText(), /历史归档不重新绘图/);
    const frozenDownload = frozen.page.waitForEvent('download'); await frozen.page.getByRole('button', {name: '生成并下载', exact: true}).click(); await frozenDownload;
    assert.deepEqual(JSON.parse(frozenUpload.get('keys')), figures.map(figure => figure.key)); assert.equal(frozenUpload.getAll('images').length, 0); assert.equal(frozenUpload.getAll('failures').length, 0);
    await frozen.close();

    // P1 leaves incomparable observations unconnected; PPT must not invent a line.
    for (const condition of [{caliber_changed: true}, {invalid_points: 1}, {path: ''}]) {
      let disconnectedUpload;
      const disconnectedReport = report(); disconnectedReport.report_figures = [figures[1]];
      const disconnectedHTML = render('reports.html', {can_write: false}, {report: disconnectedReport, live_report: true, files: [], selected: {date: '2029-09-15'}, period_kind: 'weekly', report_start: '2029-09-10', preview: '{}'})
        .replace('</head>', '<style>#report-research{min-height:1400px}</style></head>');
      const disconnected = await open(browser, prefix + '/sn/reports', disconnectedHTML, async entry => {
        if (entry.path.startsWith('/reports/figures/')) return entry.reply({figure: figures[1], series: {name: '手选指标', unit: '元', path: '52,128 484,16', points: [{date: '2029-09-01', value: 2}, {date: '2029-09-15', value: 8}], ...condition}});
        if (entry.path.endsWith('/ppt')) { disconnectedUpload = await submitted(entry); return downloaded(entry); }
        return false;
      });
      await openDialog(disconnected.page);
      await disconnected.page.locator('#report-ppt-form input[value="evidence"]').check();
      await disconnected.page.evaluate(() => {
        const original = window.ChartKit;
        window.ChartKit = {...original, render(...args) {
          const result = original.render(...args); window.disconnectedSVG = result.toSVG(); return result;
        }};
      });
      const placeholderDownload = disconnected.page.waitForEvent('download'); await disconnected.page.getByRole('button', {name: '生成并下载', exact: true}).click(); await placeholderDownload;
      assert.equal(disconnectedUpload.getAll('images').length, 1);
      assert.equal(disconnectedUpload.getAll('failures').length, 0);
      const scatter = await disconnected.page.evaluate(() => {
        const svg = new DOMParser().parseFromString(window.disconnectedSVG, 'image/svg+xml');
        return {runs: [...svg.querySelectorAll('[data-curve-run]')].map(path => path.getAttribute('d')),
          points: svg.querySelector('[data-series]').querySelectorAll('circle').length, text: svg.documentElement.textContent};
      });
      assert.equal(scatter.runs.length, 2); assert.ok(scatter.runs.every(path => !path.includes('L')));
      assert.equal(scatter.points, 2); assert.match(scatter.text, /观测不可连线/);
      await disconnected.close();
    }

    const conflict = await open(browser, prefix + '/sn/reports', html(true, {can_write: false}), async ({path, reply}) => {
      if (path.startsWith('/reports/figures/')) return reply({detail: '报告内容已变化，请刷新后重试'}, 409);
      return false;
    });
    await openDialog(conflict.page); await conflict.page.locator('#report-ppt-form input[value="evidence"]').check(); await conflict.page.getByRole('button', {name: '生成并下载', exact: true}).click();
    await conflict.page.locator('#report-ppt-status').filter({hasText: '报告内容已变化，请刷新后重试'}).waitFor();
    assert.equal(conflict.calls.some(call => call.method === 'POST'), false);
    await conflict.close();

    let releaseRead, startedRead, holdReads = false;
    const readStarted = new Promise(resolve => { startedRead = resolve; });
    const cancelFixture = await open(browser, prefix + '/sn/reports', html(true, {can_write: false}), async ({path, reply}) => {
      if (path.startsWith('/reports/figures/')) {
        if (holdReads) { startedRead(); await new Promise(resolve => { releaseRead = resolve; }); }
        return reply({figure: figures[0], rendered: {definition, payload: cycle()}});
      }
      return false;
    });
    await openDialog(cancelFixture.page); holdReads = true; await cancelFixture.page.locator('#report-ppt-form input[value="evidence"]').check(); await cancelFixture.page.getByRole('button', {name: '生成并下载', exact: true}).click(); await readStarted;
    assert.match(await cancelFixture.page.locator('#report-ppt-status').innerText(), /正在绘制第 1\/3 张图/);
    await cancelFixture.page.getByRole('button', {name: '取消生成', exact: true}).click(); releaseRead();
    await cancelFixture.page.locator('#report-ppt-dialog').waitFor({state: 'hidden'});
    assert.equal(cancelFixture.calls.some(call => call.method === 'POST'), false); await cancelFixture.close();

    const restricted = await open(browser, prefix + '/sn/reports', html(true, {restricted: true, can_write: false}), () => false);
    assert.equal(await restricted.page.locator('#report-ppt-open').count(), 0); assert.equal(await restricted.page.locator('#report-ppt-data').count(), 0);
    await restricted.close();
    console.log('PASS: report PPT options, 2x PNG dimensions, ordered multipart, failed placeholder, download, frozen readonly, conflict, cancellation and restricted visibility');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });

/* Exercise both the initial catalog template and the same row fragment after paging. */
const {assert, render, renderLibrary, launch, open, prefix} = require('./chart-ui-fixture.cjs');

const note = '数值指标; 单位为人工确认，未在原表找到对应文字; 单位待核对; 频率未注明'.repeat(8);
const rows = ['a', 'b'].map(letter => ({series_id: 'kb-' + letter.repeat(48),
  name: '备注：' + '为了核对原表口径保留的长字段名称'.repeat(20) + letter,
  vendor_code: 'SOURCE' + letter.repeat(200), note, macro: false, source: '来源.xlsx',
  unit: '千克', category: '知识库字段', frequency: '未注明', caliber: {}, registered: true}));
const groups = row => [{name: row.category, rows: [row]}];
const hint = {message: '知识库还有待确认工作表', links: [{url: '#fixture', source_file: '来源.xlsx'}]};
const html = renderLibrary({enabled: false}, {rows: [rows[0]], groups: groups(rows[0]),
  catalog_total: 2, catalog_next: 1, source_status_hint: hint});
const pages = rows.map((row, offset) => {
  const ctx = {rows: [row], groups: groups(row), catalog_offset: offset, visible_catalog_total: 2};
  return {indicators: [row], total: 2, offset, next_offset: offset ? null : 1,
    html: render('_indicator_catalog_groups.html', {}, ctx),
    nav_html: render('_indicator_catalog_links.html', {}, ctx)};
});

async function assertRow(page, row, width) {
  const tr = page.locator(`tr[data-series-id="${row.series_id}"]`);
  await tr.waitFor();
  const name = tr.getByRole('link', {name: row.name, exact: true});
  assert.equal(await name.getAttribute('title'), row.name);
  assert.equal(await tr.locator('td').nth(2).innerText(), row.unit);
  assert.equal(await tr.locator('.indicator-code + .indicator-quality-note').getAttribute('title'), row.note);
  const layout = await tr.evaluate(element => {
    const table = element.closest('table'), container = table.parentElement;
    const name = element.querySelector('.indicator-name');
    const code = element.querySelector('.indicator-code'), quality = element.querySelector('.indicator-quality-note');
    return {table: table.getBoundingClientRect().width, container: container.clientWidth,
      overflow: container.scrollWidth - container.clientWidth,
      nameWrap: getComputedStyle(name).overflowWrap, nameClamp: getComputedStyle(name).webkitLineClamp,
      nameClipped: name.scrollHeight > name.clientHeight,
      codeWrap: getComputedStyle(code).overflowWrap, codeOverflow: code.scrollWidth - code.clientWidth,
      qualityEllipsis: getComputedStyle(quality).textOverflow, qualityWrap: getComputedStyle(quality).whiteSpace};
  });
  assert.ok(layout.table <= layout.container + 1 && layout.overflow <= 1, `Catalog fits at ${width}px: ${JSON.stringify(layout)}`);
  assert.equal(layout.nameWrap, 'anywhere');
  assert.equal(layout.nameClamp, '2');
  assert.equal(layout.nameClipped, true);
  assert.equal(layout.codeWrap, 'anywhere');
  assert.ok(layout.codeOverflow <= 1);
  assert.equal(layout.qualityEllipsis, 'ellipsis');
  assert.equal(layout.qualityWrap, 'nowrap');
}

(async () => {
  const browser = await launch();
  try {
    const view = await open(browser, prefix + '/sn/indicators?tab=catalog', html, event => {
      if (event.path !== '/indicators/catalog') return false;
      return event.reply(pages[Number(event.url.searchParams.get('offset'))]);
    });
    const {page} = view;
    assert.equal(await page.locator('[data-source-status-hint]').evaluate(node => getComputedStyle(node).marginBottom), '12px');
    for (const width of [1280, 1440]) {
      await page.setViewportSize({width, height: 900});
      await assertRow(page, rows[0], width);
      await page.locator('#indicator-next-page-top').click();
      await assertRow(page, rows[1], width);
      await page.locator('#indicator-prev-page-top').click();
      await page.locator(`tr[data-series-id="${rows[0].series_id}"]`).waitFor();
    }
    await view.close();
    console.log('indicator-catalog-layout-ui: 4 viewport and render path checks passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });

/* Real ChartKit and hover component, in-process browser fixture; no running service. */
const path = require('node:path');
const {assert, launch, cycle, record} = require('./chart-ui-fixture.cjs');
const staticRoot = path.resolve(__dirname, '../src/tin/web/static');
const fixture = {data: cycle(), definition: record().definition};
const html = `<!doctype html><html><head><meta charset="utf-8"><style>
  [hidden]{display:none!important}body{margin:0;min-height:2400px;--surface:white;--text:#222;--line:#ddd;--line-strong:#aaa;--text-muted:#666}
  a{position:absolute;left:20px;display:block;width:140px;height:26px;padding:8px;background:#eee}
  #first{top:80px}#second{top:540px}#stale{top:590px}#failure{top:640px}#pending{top:690px}#navigate{top:740px}
  #right{left:1100px;top:80px}#destination{position:absolute;top:1500px}
</style></head><body>
  <a id="first" href="#destination">第一张图</a><a id="second" href="#destination">第二张图</a>
  <a id="stale" href="#destination">慢请求</a><a id="failure" href="#destination">失败请求</a>
  <a id="pending" href="#destination">关闭时待处理</a><a id="navigate" href="#destination">保留原生跳转</a>
  <a id="right" href="#destination">右侧触发</a><div id="destination">原链接目的地</div>
</body></html>`;

async function setup(browser, touch = false) {
  const context = await browser.newContext({viewport: {width: 1280, height: 900}, ...(touch ? {isMobile: true, hasTouch: true} : {})});
  const page = await context.newPage(), errors = [];
  page.setDefaultTimeout(10000);
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/*', route => route.fulfill({contentType: 'text/html', body: html}));
  await page.goto('http://chart-hover.fixture/');
  await page.addStyleTag({path: path.join(staticRoot, 'charts.css')});
  await page.addScriptTag({path: path.join(staticRoot, 'chart-kit.js')});
  await page.addScriptTag({path: path.join(staticRoot, 'chart-hover.js')});
  await page.evaluate(value => {
    window.fixture = value;
    window.loads = {}; window.renders = 0; window.destroys = 0;
    const original = window.ChartKit;
    window.ChartKit = {...original, render(...args) {
      window.renders++;
      const renderer = original.render(...args);
      return {...renderer, destroy() { window.destroys++; renderer.destroy(); }};
    }};
    for (const id of ['first', 'second', 'stale', 'failure', 'pending', 'navigate', 'right']) {
      window.ChartHover.attach(document.getElementById(id), {key: id, title: id === 'first' ? '第一张图' : id,
        load: async () => {
          window.loads[id] = (window.loads[id] || 0) + 1;
          if (id === 'first') await new Promise(resolve => setTimeout(resolve, 180));
          if (id === 'stale' || id === 'pending') return new Promise(resolve => { window['resolve_' + id] = resolve; });
          if (id === 'failure') throw new Error('来源已撤回，请重新选择图表。');
          return {...window.fixture, title: id === 'first' ? '服务端图名 <安全文本>' : id};
        }});
    }
  }, fixture);
  return {context, page, errors};
}
const visible = page => page.locator('.chart-hover:visible');
async function closed(page) {
  await page.waitForFunction(() => ![...document.querySelectorAll('.chart-hover')].some(node => node.getClientRects().length));
}
async function expectChart(page, title) {
  await visible(page).locator('svg').waitFor();
  await page.waitForFunction(title => document.querySelector('.chart-hover:not([hidden])')?.getAttribute('aria-label') === title, title);
  assert.equal(await visible(page).count(), 1);
}
async function outside(page) { await page.mouse.click(1260, 870); await closed(page); }

(async () => {
  const browser = await launch();
  try {
    const {context, page: p, errors} = await setup(browser);
    await p.locator('#first').hover();
    await p.waitForTimeout(100);
    assert.equal(await visible(p).count(), 0, '短暂经过触发元素时不打开');
    assert.equal(await p.evaluate(() => window.loads.first || 0), 0, '延迟结束前不读取');
    await visible(p).getByText('正在读取…', {exact: true}).waitFor();
    await expectChart(p, '服务端图名 <安全文本>');
    assert.equal(await visible(p).getAttribute('role'), 'dialog');
    assert.equal(await visible(p).locator('img').count(), 0, '图名按文本呈现');
    assert.equal(await visible(p).locator('[data-legend]').count(), 4, '使用正常图表的交互图例');
    const before = await visible(p).locator('[data-series]').count();
    await visible(p).locator('[data-legend]').first().click();
    assert.equal(await visible(p).locator('[data-legend]').first().getAttribute('aria-pressed'), 'false');
    assert.equal(await visible(p).locator('[data-series]').count(), before - 1);
    const plot = await visible(p).locator('[data-chart-hover]').boundingBox();
    await p.mouse.move(plot.x + plot.width / 3, plot.y + plot.height / 2);
    await visible(p).locator('.chart-kit-tooltip:visible').waitFor();
    assert.match(await visible(p).locator('.chart-kit-tooltip').innerText(), /第 \d+ 天/);
    await p.mouse.move(1260, 870); await p.waitForTimeout(350);
    assert.equal(await visible(p).count(), 1, '离开触发元素与浮窗后保持打开');
    const originalBox = await visible(p).boundingBox();
    assert.ok(originalBox.x >= 176, '首次在触发元素右侧显示');
    const documentTop = await visible(p).evaluate(node => parseFloat(node.style.top));
    await p.evaluate(() => window.scrollTo(0, 60));
    const scrolledBox = await visible(p).boundingBox();
    assert.ok(Math.abs(scrolledBox.y - originalBox.y + 60) < 2, '浮窗随文档滚动');
    assert.equal(await visible(p).evaluate(node => parseFloat(node.style.top)), documentTop);
    await p.evaluate(() => window.scrollTo(0, 0));
    await outside(p);
    assert.equal(await p.evaluate(() => window.destroys), 1, '关闭时销毁图表实例');

    // Focus opens a cached chart; Escape restores focus without immediately reopening it.
    await p.locator('#first').focus(); await expectChart(p, '服务端图名 <安全文本>');
    assert.equal(await p.evaluate(() => window.loads.first), 1, '按 key 复用读取结果');
    await visible(p).locator('[data-legend]').first().focus();
    await p.keyboard.press('Escape'); await closed(p);
    assert.equal(await p.evaluate(() => document.activeElement.id), 'first');
    await p.waitForTimeout(350); await closed(p);

    await p.mouse.move(1260, 870); await p.locator('#first').hover();
    await expectChart(p, '服务端图名 <安全文本>');
    await p.locator('#second').hover(); await expectChart(p, 'second');
    assert.equal(await p.locator('.chart-hover:visible svg').count(), 1, '悬停另一触发元素时替换浮窗');
    assert.equal(await p.evaluate(() => window.renders - window.destroys), 1, '同时只有一个存活的 ChartKit 实例');

    // A second trigger is outside the currently open dialog and its own trigger.
    await p.locator('#first').hover(); await expectChart(p, '服务端图名 <安全文本>');
    await p.locator('#second').hover(); await p.waitForTimeout(80);
    await p.locator('#second').dispatchEvent('pointerdown'); await closed(p);
    await p.waitForTimeout(300); await closed(p);
    assert.equal(await p.evaluate(() => window.renders - window.destroys), 0, '点击待打开的另一触发元素时关闭原浮窗并取消延迟请求');

    await p.locator('#stale').hover(); await visible(p).getByText('正在读取…', {exact: true}).waitFor();
    await p.locator('#second').hover(); await expectChart(p, 'second');
    await p.evaluate(() => window.resolve_stale({...window.fixture, title: '已过期结果'}));
    await p.waitForTimeout(100); await expectChart(p, 'second');
    assert.equal(await p.evaluate(() => window.renders - window.destroys), 1, '过期请求不创建额外图表');
    await p.locator('#pending').hover(); await visible(p).getByText('正在读取…', {exact: true}).waitFor();
    await outside(p);
    await p.evaluate(() => window.resolve_pending({...window.fixture, title: '关闭后的结果'}));
    await p.waitForTimeout(100); await closed(p);

    await p.locator('#failure').hover();
    await visible(p).getByText('来源已撤回，请重新选择图表。', {exact: true}).waitFor();
    await outside(p); await p.locator('#failure').hover();
    await visible(p).getByText('来源已撤回，请重新选择图表。', {exact: true}).waitFor();
    assert.equal(await p.evaluate(() => window.loads.failure), 2, '失败读取可以重试');
    await outside(p);

    await p.locator('#right').hover(); await expectChart(p, 'right');
    const rightTrigger = await p.locator('#right').boundingBox(), leftBox = await visible(p).boundingBox();
    assert.ok(leftBox.x + leftBox.width <= rightTrigger.x, '右侧空间不足时在左侧显示');
    await p.locator('#right').dispatchEvent('dragstart'); await closed(p);
    assert.equal(await p.evaluate(() => window.renders), await p.evaluate(() => window.destroys), '拖拽开始时关闭并销毁浮窗');
    await p.locator('#right').dispatchEvent('dragend');

    // Canceled native dragstart does not dispatch dragend (for example while favorites save).
    await p.mouse.move(1260, 870); await p.locator('#right').hover(); await expectChart(p, 'right');
    await p.evaluate(() => {
      const trigger = document.getElementById('right');
      trigger.addEventListener('dragstart', event => event.preventDefault(), {once: true});
      trigger.dispatchEvent(new Event('dragstart', {bubbles: true, cancelable: true}));
    });
    await closed(p); await p.waitForTimeout(20);
    await p.mouse.move(1260, 870); await p.locator('#right').hover(); await expectChart(p, 'right');
    assert.equal(await p.evaluate(() => window.renders - window.destroys), 1, '取消拖拽后无需dragend即可再次打开浮窗');
    await outside(p);

    // A narrow viewport keeps the complete popover inside the viewport, below the trigger.
    await p.setViewportSize({width: 360, height: 720});
    await p.locator('#first').focus(); await expectChart(p, '服务端图名 <安全文本>');
    const narrow = await visible(p).boundingBox(), trigger = await p.locator('#first').boundingBox();
    assert.ok(narrow.x >= 0 && narrow.x + narrow.width <= 360, '窄屏按视口收缩');
    assert.ok(narrow.y >= trigger.y + trigger.height, '两侧不足时显示在下方');
    assert.ok(narrow.y + narrow.height <= 720, '浮窗不超出视口底部');
    await p.keyboard.press('Escape'); await closed(p);
    await p.setViewportSize({width: 1280, height: 900});
    await p.locator('#navigate').hover(); await expectChart(p, 'navigate');
    await p.locator('#navigate').click(); await p.waitForURL(/#destination$/);
    assert.equal(new URL(p.url()).hash, '#destination', '点击触发元素保留原生跳转');
    assert.deepEqual(errors, []); await context.close();

    const touch = await setup(browser, true);
    assert.equal(await touch.page.evaluate(() => matchMedia('(hover: none)').matches), true);
    await touch.page.locator('#first').focus();
    await touch.page.locator('#first').dispatchEvent('mouseenter');
    await touch.page.waitForTimeout(500);
    assert.equal(await visible(touch.page).count(), 0, '无悬停能力时不启用浮窗');
    assert.deepEqual(await touch.page.evaluate(() => window.loads), {});
    assert.deepEqual(touch.errors, []); await touch.context.close();
    console.log('chart-hover-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });

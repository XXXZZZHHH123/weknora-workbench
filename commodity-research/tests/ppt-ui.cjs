/* Isolated browser test: every document, asset and API request is fulfilled in-process. */
const {assert, render, launch, open, prefix, record: chartRecord, cycle, pptRecord, seriesId, otherSeriesId, slot, multipart} = require('./ppt-ui-fixture.cjs');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
// Match the bounded list response: no presentation, bindings or candidates.
const listItem = value => ({id: value.id, title: value.title, filename: value.filename, revision: value.revision,
  slide_count: value.slide_count, slot_count: value.slot_count, updated_at: value.updated_at,
  slot_counts: {bound: 0, kept: 2, pending: 3}, last_export: value.last_export || null,
  source: value.source, source_state: value.source_state});
const pptSource = (id = 'knowledge-1') => ({knowledge_base_id: 'kb-authorized', knowledge_id: id,
  filename: id + ' 周报 <img onerror=window.__xss=1>.pptx', created_at: '2029-09-15T08:00:00Z', size: 1024 * 1024,
  slide_count: 12, file_hash: 'a'.repeat(64)});
const sourceRecord = (status = 'ready') => ({...pptRecord(), source: {kind: 'knowledge_base',
  knowledge_base_id: 'kb-authorized', knowledge_id: 'knowledge-1', file_hash: 'a'.repeat(64)}, source_state: {status}});
(async () => {
  const browser = await launch();
  try {
    let saved = {...pptRecord(), last_export: {created_at: '2029-09-15T08:01:00Z', export_id: 'export-last',
      download_url: '/api/sn/ppt-templates/template-1/exports/export-last'}}, deletionConflict = true, uploadFail = true, deletedRevision, createdSource, createFail = true;
    const list = await open(browser, '/sn/reports?date=2029-09-15', render('reports.html', {id: null, date: '2029-09-15'}), async entry => {
      const {path, method, url, reply, body} = entry;
      if (path === '/ppt-templates/sources') {
        const later = url.searchParams.get('offset') === '20', searching = url.searchParams.get('search') === '周报';
        return reply({items: [pptSource(later || searching ? 'knowledge-2' : 'knowledge-1')], total: searching ? 1 : 21,
          next_offset: later || searching ? null : 20});
      }
      if (path === '/ppt-templates/from-source') {
        createdSource = body;
        if (createFail) { createFail = false; return reply({detail: {code: 'source_changed', message: '知识库原件已变化 <img onerror=window.__xss=1>，请重新选择'}}, 409); }
        return reply(saved, 201);
      }
      if (path === '/ppt-templates' && method === 'POST') {
        assert.ok(multipart(entry).has('file')); await delay(100);
        if (uploadFail) { uploadFail = false; return reply({detail: '合成解析错误 <img onerror=window.__xss=1>'}, 400); }
        return reply(saved, 201);
      }
      if (path === '/ppt-templates') return reply({items: [listItem(saved)], total: 1, next_offset: null});
      if (method === 'DELETE') {
        deletedRevision = Number(url.searchParams.get('revision'));
        if (deletionConflict) { deletionConflict = false; saved.revision = 2; return reply({detail: 'PPT模板已更新，请刷新后继续'}, 409); }
        return reply({revision: 3, status: 'deleted'});
      }
      return false;
    });
    const lp = list.page;
    assert.equal(await lp.locator('#report-ppt').getAttribute('open'), null, 'PPT 区域默认折叠');
    assert.match(await lp.locator('#report-ppt > summary').innerText(), /按我的 PPT 版式导出/);
    assert.equal(list.calls.some(call => call.path === '/ppt-templates'), false, '折叠时不读取个人模板列表');
    await lp.locator('#report-ppt > summary').click();
    await lp.locator('[data-ppt-counts]').filter({hasText: '已绑定 0 / 保留原图 2 / 待确认 3'}).waitFor();
    assert.equal(await lp.locator('[data-ppt-updated]').innerText(), '2029-09-15 16:00');
    assert.match(await lp.locator('[data-ppt-last-export]').innerText(), /^2029-09-15 16:01/);
    assert.equal(await lp.locator('[data-ppt-download]').getAttribute('href'), prefix + '/api/sn/ppt-templates/template-1/exports/export-last');
    assert.equal(await lp.locator('[data-ppt-bind]').getAttribute('href'), prefix + '/sn/reports/ppt/template-1?date=2029-09-15');
    assert.equal(await lp.locator('[data-ppt-export]').getAttribute('href'), prefix + '/sn/reports/ppt/template-1/export?date=2029-09-15');
    assert.match(await lp.locator('.ppt-list thead').innerText(), /更新时间（北京时间）/);
    assert.equal(list.calls.some(call => call.method === 'GET' && call.path === '/ppt-templates/template-1'), false);
    assert.doesNotMatch(await lp.locator('#ppt-list-rows').innerText(), /正在读取图位统计|T08:00/);
    assert.equal(await lp.locator('#ppt-list-rows img').count(), 0);
    assert.equal(list.calls.some(call => call.path === '/ppt-templates/sources'), false, '新建前不读取知识库文件');
    assert.equal(await lp.locator('#ppt-source-dialog').isVisible(), false);
    await lp.locator('#ppt-new-template').click();
    await lp.locator('#ppt-source-dialog[open]').waitFor();
    await lp.locator('#ppt-source-rows label').filter({hasText: 'knowledge-1'}).waitFor();
    assert.match(await lp.locator('#ppt-source-rows').innerText(), /2029-09-15 16:00[\s\S]*1.0 MB[\s\S]*12/);
    assert.equal(await lp.locator('#ppt-source-rows img').count(), 0);
    assert.equal(await lp.locator('#ppt-source-create').isDisabled(), true);
    await lp.locator('#ppt-source-next').click();
    await lp.locator('#ppt-source-rows label').filter({hasText: 'knowledge-2'}).waitFor();
    assert.equal(await lp.locator('#ppt-source-next').isDisabled(), true);
    await lp.locator('#ppt-source-prev').click();
    await lp.locator('#ppt-source-rows label').filter({hasText: 'knowledge-1'}).waitFor();
    await lp.locator('#ppt-source-query').fill('周报'); await lp.locator('#ppt-source-search button').click();
    await lp.locator('#ppt-source-rows label').filter({hasText: 'knowledge-2'}).waitFor();
    assert.equal(await lp.locator('#ppt-source-prev').isDisabled(), true);
    await lp.locator('#ppt-source-rows input').check(); await lp.locator('#ppt-source-create').click();
    await lp.locator('#ppt-source-status').filter({hasText: '知识库原件已变化'}).waitFor();
    assert.equal(await lp.locator('#ppt-source-status img').count(), 0);
    await lp.locator('#ppt-source-create').click(); await lp.locator('#ppt-source-status a').waitFor();
    assert.deepEqual(createdSource, {knowledge_base_id: 'kb-authorized', knowledge_id: 'knowledge-2', file_hash: 'a'.repeat(64)});
    assert.equal(await lp.locator('#ppt-source-status a').getAttribute('href'), prefix + '/sn/reports/ppt/template-1?date=2029-09-15');
    await lp.locator('#ppt-local-upload > summary').click();
    await lp.locator('#ppt-file').setInputFiles({name: 'fixture.pptx', mimeType: 'application/vnd.openxmlformats-officedocument.presentationml.presentation', buffer: Buffer.from('synthetic')});
    await lp.locator('#ppt-upload-form button').click();
    await lp.locator('#ppt-upload-status').filter({hasText: '合成解析错误'}).waitFor();
    assert.equal(await lp.locator('#ppt-upload-status img').count(), 0);
    await lp.locator('#ppt-upload-form button').click();
    await lp.locator('#ppt-upload-status a').waitFor();
    assert.equal(await lp.locator('#ppt-upload-status a').getAttribute('href'), prefix + '/sn/reports/ppt/template-1?date=2029-09-15');
    assert.equal(await lp.locator('#ppt-upload-bar').getAttribute('value'), '100');
    await lp.locator('#ppt-source-close').click();
    lp.once('dialog', dialog => dialog.accept()); await lp.locator('[data-ppt-delete]').click();
    await lp.locator('#ppt-list-conflict').waitFor({state: 'visible'}); assert.equal(deletedRevision, 1);
    await lp.locator('#ppt-list-reload').click();
    await lp.locator('[data-ppt-counts]').filter({hasText: '待确认 3'}).waitFor();
    lp.once('dialog', dialog => dialog.accept()); await lp.locator('[data-ppt-delete]').click();
    await lp.waitForFunction(() => !document.querySelector('#ppt-list-rows tr')); assert.equal(deletedRevision, 2);
    assert.equal(await lp.evaluate(() => window.__xss), undefined); await list.close();

    for (const [period, date, lastExport] of [['weekly', '2030-01-04', null], ['daily', null,
      {created_at: '2030-01-04T08:00:00Z', export_id: 'unsafe', download_url: 'javascript:alert(1)'}]]) {
      const reports = await open(browser, '/sn/reports?period=' + period, render('reports.html', {id: null, period, date}), ({path, reply}) => {
        if (path === '/ppt-templates') return reply({items: [listItem({...pptRecord(), last_export: lastExport})], total: 1, next_offset: null});
        return false;
      });
      await reports.page.locator('#report-ppt > summary').click();
      await reports.page.locator('[data-ppt-export]').waitFor();
      assert.equal(await reports.page.locator('[data-ppt-export]').getAttribute('href'), prefix + '/sn/reports/ppt/template-1/export' + (date ? '?date=' + date : ''));
      if (lastExport) {
        assert.equal(await reports.page.locator('[data-ppt-download]').getAttribute('href'), prefix + '/api/sn/ppt-templates/template-1/exports/unsafe', '下载链接依据真实 ID 构建，不采用外部地址');
      } else {
        assert.equal(await reports.page.locator('[data-ppt-download]').count(), 0);
        assert.match(await reports.page.locator('#ppt-list-rows').innerText(), /尚未导出/);
      }
      await reports.close();
    }

    const anchored = await open(browser, '/sn/reports#report-ppt', render('reports.html', {id: null}), ({path, reply}) => {
      if (path === '/ppt-templates') return reply({items: [], total: 0, next_offset: null});
      return false;
    });
    assert.equal(await anchored.page.locator('#report-ppt').getAttribute('open'), '', '旧入口跳到区域锚点时自动展开');
    await anchored.page.locator('#ppt-list-status').filter({hasText: '还没有模板'}).waitFor();
    await anchored.close();

    saved = pptRecord(); let patch, shouldConflict = true;
    const detail = await open(browser, '/sn/reports/ppt/template-1', render('ppt_detail.html'), async ({path, method, body, reply}) => {
      if (path === '/ppt-templates/template-1') return reply(saved);
      if (path.endsWith('/bindings')) {
        patch = body;
        if (shouldConflict) { shouldConflict = false; saved.revision = 2; return reply({detail: 'PPT模板已更新，请刷新后继续'}, 409); }
        body.slots.forEach(item => { saved.bindings[item.slot_id] = item.binding; }); saved.revision++;
        return reply(saved);
      }
      if (path === '/charts') return reply({items: [chartRecord('manual-chart')], next_offset: null});
      if (path === '/indicators/catalog') return reply({indicators: [{series_id: otherSeriesId, name: '手动指标', unit: '吨'}], next_offset: null});
      if (path.endsWith('/data')) return reply(cycle());
      if (path.startsWith('/charts/')) return reply(chartRecord());
      return false;
    });
    const dp = detail.page;
    await dp.locator('.ppt-board .ppt-slot').first().waitFor();
    assert.equal(await dp.locator('#ppt-breadcrumb-title').innerText(), saved.title);
    assert.equal(await dp.locator('#ppt-breadcrumb-title img').count(), 0);
    assert.equal(await dp.locator('#ppt-export-link').getAttribute('href'), prefix + '/sn/reports/ppt/template-1/export');
    const board = await dp.locator('.ppt-board').first().boundingBox(); assert.ok(Math.abs(board.width / board.height - 2) < .03);
    await dp.locator('.ppt-board [data-slot-id="chart-slot"]').click();
    assert.equal(await dp.locator('.ppt-texts li').count(), 20);
    await dp.locator('#ppt-candidates svg').first().waitFor();
    assert.match(await dp.locator('#ppt-candidates').innerText(), /0.85/);
    await dp.locator('#ppt-keep').click();
    await dp.locator('[data-slide="1"] [data-ppt-edit]').click();
    assert.match(await dp.locator('#ppt-summary').innerText(), /已绑定 1 \/ 保留原图 3 \/ 待确认 1/);
    await dp.locator('.ppt-board [data-slot-id="table-slot"]').click();
    assert.match(await dp.locator('#ppt-candidates').innerText(), /数值已核实[\s\S]*3\/3（100.0%）/);
    await dp.locator('#ppt-save').click();
    await dp.locator('#ppt-conflict').waitFor({state: 'visible'}); assert.equal(patch.revision, 1);
    assert.equal(await dp.locator('#ppt-keep').isDisabled(), true);
    await dp.locator('#ppt-reload').click();
    await dp.locator('#ppt-meta').filter({hasText: 'v2'}).waitFor();
    await dp.locator('.ppt-board [data-slot-id="chart-slot"]').click();
    await dp.locator('#ppt-candidates button').first().click();
    await dp.locator('#ppt-chart-search input').fill('手动图表'); await dp.locator('#ppt-chart-search button').click();
    await dp.locator('#ppt-chart-results button').click();
    await dp.locator('.ppt-board [data-slot-id="table-slot"]').click();
    await dp.locator('#ppt-series-rows').fill('1'); await dp.locator('#ppt-series-search input[name=q]').fill('手动指标');
    await dp.locator('#ppt-series-search button').click(); await dp.locator('#ppt-series-results button').click();
    await dp.locator('#ppt-save').click();
    await dp.locator('#ppt-detail-status').filter({hasText: '已保存图位绑定'}).waitFor();
    assert.equal(patch.revision, 2); assert.deepEqual(patch.slots, [
      {slot_id: 'chart-slot', binding: {kind: 'chart', chart_id: 'manual-chart'}},
      {slot_id: 'table-slot', binding: {kind: 'series', series_id: otherSeriesId, rows: 1}}]);
    await dp.locator('#ppt-series-rows').fill('2'); await dp.locator('#ppt-series-rows').blur();
    await dp.locator('#ppt-save').click(); await dp.locator('#ppt-meta').filter({hasText: 'v4'}).waitFor();
    assert.deepEqual(patch.slots, [{slot_id: 'table-slot', binding: {kind: 'series', series_id: otherSeriesId, rows: 2}}]);
    await dp.locator('.ppt-board [data-slot-id="native-slot"]').click();
    assert.equal(await dp.locator('#ppt-chart-search input').isDisabled(), true);
    assert.match(await dp.locator('#ppt-inspector').innerText(), /原生图表暂不支持替换/);
    await dp.locator('.ppt-board [data-slot-id="chart-slot"]').click(); await dp.locator('#ppt-clear').click();
    assert.match(await dp.locator('#ppt-current-binding').innerText(), /尚未绑定/);
    assert.equal(await dp.evaluate(() => window.__xss), undefined);
    await dp.evaluate(() => document.documentElement.classList.add('dark'));
    assert.equal(await dp.locator('.ppt-slot').first().evaluate(el => getComputedStyle(el).color === getComputedStyle(el).backgroundColor), false);
    await detail.close();

    for (const template of ['reports.html', 'ppt_detail.html', 'ppt_export.html']) {
      const pathname = template === 'reports.html' ? '/sn/reports' : '/sn/reports/ppt/template-1' + (template === 'ppt_export.html' ? '/export' : '');
      const ro = await open(browser, pathname, render(template, {can_write: false, write_reason: '当前账号没有研究工作台编辑权限'}), ({path, reply}) => {
        if (path === '/ppt-templates') return reply({items: [listItem(pptRecord())], total: 1, next_offset: null});
        return reply(pptRecord());
      });
      if (template === 'reports.html') await ro.page.locator('#report-ppt > summary').click();
      assert.match(await ro.page.locator('#ppt-write-reason').innerText(), /没有研究工作台编辑权限/);
      const selector = template === 'reports.html' ? '#ppt-new-template' : template === 'ppt_export.html' ? '#ppt-export-date' : '#ppt-save';
      assert.equal(await ro.page.locator(selector).isDisabled(), true);
      assert.equal(ro.calls.some(call => call.method !== 'GET'), false); await ro.close();
    }

    for (const [period, state, expected] of [['weekly', 'changed', '知识库原件已变化'], ['daily', 'deleted', '知识库原件已删除']]) {
      let current = sourceRecord(state), replacement;
      const blocked = await open(browser, '/sn/reports?period=' + period, render('reports.html', {period, id: null}), ({path, method, body, reply}) => {
        if (path === '/ppt-templates') return reply({items: [listItem(current)], total: 1, next_offset: null});
        if (path === '/ppt-templates/sources') return reply({items: [pptSource('replacement')], total: 1, next_offset: null});
        if (path.endsWith('/source') && method === 'PATCH') {
          replacement = body; current = {...sourceRecord(), revision: 2, bindings: {}}; return reply(current);
        }
        return false;
      });
      const bp = blocked.page;
      await bp.locator('#report-ppt > summary').click();
      await bp.locator('[data-ppt-source-warning]').filter({hasText: expected}).waitFor();
      assert.equal(await bp.locator('[data-ppt-export]').getAttribute('aria-disabled'), 'true');
      assert.equal(await bp.locator('[data-ppt-export]').getAttribute('href'), null);
      await bp.locator('[data-ppt-reselect]').click();
      assert.match(await bp.locator('#ppt-source-reset-warning').innerText(), /重新解析 PPT 并清空现有图位绑定/);
      assert.equal(await bp.locator('#ppt-local-upload').isVisible(), false);
      await bp.locator('#ppt-source-rows input').check(); await bp.locator('#ppt-source-create').click();
      await bp.locator('#ppt-source-status a').waitFor(); await bp.locator('#ppt-source-close').click();
      assert.deepEqual(replacement, {revision: 1, knowledge_base_id: 'kb-authorized', knowledge_id: 'replacement', file_hash: 'a'.repeat(64)});
      await bp.locator('[data-ppt-export][href]').waitFor();
      assert.equal(await bp.locator('[data-ppt-source-warning]').count(), 0); await blocked.close();
    }

    for (const [state, expected] of [['changed', '知识库原件已变化'], ['deleted', '知识库原件已删除'],
      ['forbidden', '授权已失效'], ['unavailable', '暂时无法核验']]) {
      const blocked = await open(browser, '/sn/reports/ppt/template-1/export', render('ppt_export.html'), ({reply}) => reply(sourceRecord(state)));
      await blocked.page.locator('#ppt-source-state').filter({hasText: expected}).waitFor();
      assert.equal(await blocked.page.locator('#ppt-export-date').isDisabled(), true);
      assert.equal(blocked.calls.some(call => call.method === 'POST'), false); await blocked.close();
    }

    let replacedDetail = sourceRecord('deleted'), detailReplacement;
    const missingDetail = await open(browser, '/sn/reports/ppt/template-1', render('ppt_detail.html'), ({path, method, body, reply}) => {
      if (path === '/ppt-templates/template-1') return reply(replacedDetail);
      if (path === '/ppt-templates/sources') return reply({items: [pptSource()], total: 1, next_offset: null});
      if (path.endsWith('/source') && method === 'PATCH') {
        detailReplacement = body; replacedDetail = {...sourceRecord(), bindings: {}, revision: 2}; return reply(replacedDetail);
      }
      return false;
    });
    await missingDetail.page.locator('#ppt-source-state').filter({hasText: '知识库原件已删除'}).waitFor();
    assert.equal(await missingDetail.page.locator('#ppt-export-link').getAttribute('aria-disabled'), 'true');
    await missingDetail.page.locator('#ppt-source-reselect').click(); await missingDetail.page.locator('#ppt-source-rows input').check();
    await missingDetail.page.locator('#ppt-source-create').click(); await missingDetail.page.locator('#ppt-source-status a').waitFor();
    await missingDetail.page.locator('#ppt-source-close').click();
    assert.equal(detailReplacement.revision, 1); assert.equal(await missingDetail.page.locator('#ppt-source-state').isVisible(), false);
    assert.equal(await missingDetail.page.locator('#ppt-export-link').getAttribute('aria-disabled'), 'false'); await missingDetail.close();

    for (const detectedAt of ['recheck', 'export']) {
      let reads = 0;
      const changedSource = await open(browser, '/sn/reports/ppt/template-1/export', render('ppt_export.html'), ({method, reply}) => {
        if (method === 'GET') return reply(sourceRecord(++reads > 1 && detectedAt === 'recheck' ? 'changed' : 'ready'));
        return reply({detail: {code: 'source_changed', message: '知识库原件已变化，请重新选择并确认后导出。'}}, 409);
      });
      await changedSource.page.waitForFunction(() => !document.querySelector('#ppt-export-fields').disabled);
      await changedSource.page.locator('#ppt-export-form button').click();
      await changedSource.page.locator('#ppt-source-state').filter({hasText: '知识库原件已变化'}).waitFor();
      assert.equal(await changedSource.page.locator('#ppt-export-date').isDisabled(), true);
      assert.equal(await changedSource.page.locator('#ppt-export-conflict').isVisible(), false, '来源变化显示重新选择提示');
      assert.equal(changedSource.calls.some(call => call.method === 'POST'), detectedAt === 'export'); await changedSource.close();
    }

    saved = pptRecord(); saved.bindings['chart-slot'] = {kind: 'chart', chart_id: 'chart-1'};
    saved.bindings['table-slot'] = {kind: 'series', series_id: seriesId, rows: 1};
    const extraSlots = Array.from({length: 5}, (_, i) => slot('extra-' + i, 'chart', 30, 40, 400, 180, {slide: 2}));
    saved.presentation.slides[1].slots.push(...extraSlots); saved.slot_count += extraSlots.length;
    extraSlots.forEach((value, i) => { saved.bindings[value.slot_id] = {kind: 'chart', chart_id: i === 3 ? 'broken' : 'chart-' + (i + 2)}; });
    let active = 0, maximum = 0, form;
    const exporting = await open(browser, '/sn/reports/ppt/template-1/export?date=2029-09-15', render('ppt_export.html', {date: '2029-09-15'}), async entry => {
      const {path, method, reply, url} = entry;
      if (path === '/ppt-templates/template-1') return reply(saved);
      if (path.endsWith('/export') && method === 'POST') {
        form = multipart(entry); const meta = JSON.parse(form.get('meta').toString());
        assert.equal(form.get('date').toString(), '2029-09-15'); assert.equal(form.get('mark_headlines').toString(), 'true');
        assert.equal(form.get('revision').toString(), '1');
        assert.equal(meta['table-slot'].data_as_of, '2029-09-10'); assert.match(meta['extra-3'].error, /合成来源不可读/);
        assert.equal(form.has('extra-3'), false); assert.equal(form.has('keep-slot'), false);
        for (const id of ['chart-slot', 'table-slot', 'extra-0']) {
          const png = form.get(id); assert.equal(png.subarray(0, 8).toString('hex'), '89504e470d0a1a0a');
          assert.equal(png.readUInt32BE(16), id === 'table-slot' ? 560 : 800);
          assert.equal(png.readUInt32BE(20), id === 'table-slot' ? 300 : 360);
        }
        saved.revision++;
        const updated = Object.keys(meta).filter(id => form.has(id)).map(id => ({slot_id: id, slide: id.startsWith('extra') ? 2 : 1, data_as_of: meta[id].data_as_of}));
        return reply({export_id: 'export-1', date: '2029-09-15', revision: saved.revision, mark_headlines: true, updated_slots: updated,
          kept_slots: [{slot_id: 'extra-3', slide: 2, reason: 'data_read_failed', detail: meta['extra-3'].error},
            {slot_id: 'keep-slot', slide: 1, reason: 'keep'}, {slot_id: 'pending-slot', slide: 1, reason: 'unbound'},
            {slot_id: 'native-slot', slide: 2, reason: 'native_chart'}],
          headlines_to_check: [{slide: 1, title: '<img onerror=window.__xss=1>2029年'}],
          download_url: '/api/sn/ppt-templates/template-1/exports/export-1'});
      }
      if (path.startsWith('/charts/') || path.startsWith('/series/')) {
        active++; maximum = Math.max(maximum, active); await delay(40); active--;
        if (path.includes('/broken')) return reply({detail: '合成来源不可读'}, 503);
        if (path.startsWith('/series/')) {
          assert.equal(url.searchParams.get('limit'), '2'); assert.equal(url.searchParams.get('date'), '2029-09-15');
          return reply({name: '合成数据小表', points: [{date: '2029-09-09', value: 1}, {date: '2029-09-10', value: 0}]});
        }
        if (path.endsWith('/data')) { assert.equal(url.searchParams.get('date'), '2029-09-15'); return reply({...cycle(), revision: 1}); }
        return reply(chartRecord());
      }
      return false;
    });
    const ep = exporting.page;
    await ep.waitForFunction(() => !document.querySelector('#ppt-export-fields').disabled);
    assert.equal(await ep.locator('#ppt-export-date').inputValue(), '2029-09-15');
    assert.equal(await ep.locator('#ppt-breadcrumb-title').innerText(), saved.title);
    await ep.evaluate(() => {
      // A dark host theme must still export opaque white slides with dark type.
      document.documentElement.style.setProperty('--surface', '#111827');
      document.documentElement.style.setProperty('--text', '#f9fafb');
      document.documentElement.style.setProperty('--text-muted', '#9ca3af');
      const kit = window.ChartKit; window.__pptExports = [];
      const capture = (kind, draw) => (target, data, options) => {
        const handle = draw(target, data, options);
        window.__pptExports.push({kind, export: options.export, title: options.title, svg: handle.toSVG()});
        return handle;
      };
      window.ChartKit = {...kit, render: capture('chart', kit.render), renderTable: capture('table', kit.renderTable)};
    });
    await ep.locator('#ppt-export-date').fill('2029-09-15'); await ep.locator('#ppt-mark-headlines').check();
    await ep.locator('#ppt-export-form button').click(); await ep.locator('#ppt-download').waitFor();
    assert.ok(maximum <= 4 && maximum > 1); assert.ok(form);
    assert.match(await ep.locator('#ppt-export-report').innerText(), /已更新 6 个图位 \/ 保留原图 4 个图位/);
    assert.match(await ep.locator('#ppt-export-report').innerText(), /合成来源不可读/);
    assert.deepEqual(await ep.locator('[data-report-page]').evaluateAll(elements => elements.map(element => element.dataset.reportPage)), ['1', '2']);
    assert.match(await ep.locator('[data-report-page="1"]').innerText(), /table-slot · 数据截至 2029-09-10/);
    const keptSummary = await ep.locator('[data-report-kept-summary]').innerText();
    assert.match(keptSummary, /尚未绑定 1 个/); assert.match(keptSummary, /研究员选择保留 1 个/);
    assert.match(keptSummary, /数据读取或图片生成失败 1 个/);
    assert.equal(await ep.locator('.ppt-report-kept').count(), 4);
    assert.equal(await ep.locator('.ppt-report-kept[open]').count(), 1);
    assert.equal(await ep.locator('[data-report-reason="data_read_failed"]').getAttribute('open'), '');
    for (const reason of ['keep', 'unbound', 'native_chart']) {
      assert.equal(await ep.locator(`[data-report-reason="${reason}"]`).getAttribute('open'), null);
      assert.equal(await ep.locator(`[data-report-reason="${reason}"] ul`).isVisible(), false);
    }
    await ep.locator('[data-report-reason="unbound"] summary').click();
    assert.match(await ep.locator('[data-report-reason="unbound"] ul').innerText(), /pending-slot/);
    const drawings = await ep.evaluate(() => window.__pptExports.map(item => {
      const svg = new DOMParser().parseFromString(item.svg, 'image/svg+xml').documentElement;
      const texts = [...svg.querySelectorAll('text')];
      return {kind: item.kind, export: item.export, title: item.title,
        background: svg.querySelector('rect').getAttribute('fill'),
        textStyles: texts.map(node => ({size: parseFloat(node.style.fontSize), fill: node.style.fill})),
        titleText: [...svg.querySelectorAll('[data-chart-title]')].map(node => node.textContent).join(''),
        titleSizes: [...svg.querySelectorAll('[data-chart-title]')].map(node => Number(node.getAttribute('font-size'))),
        lineWidths: [...svg.querySelectorAll('[data-curve-run]')].map(node => Number(node.getAttribute('stroke-width'))),
        scaled: [...svg.querySelectorAll('[transform]')].some(node => /scale\(/.test(node.getAttribute('transform')))};
    }));
    assert.equal(drawings.length, 6);
    for (const drawing of drawings) {
      assert.equal(drawing.export, true); assert.equal(drawing.background, '#ffffff'); assert.equal(drawing.scaled, false);
      assert.ok(drawing.textStyles.every(text => text.size >= 11 && ['rgb(48, 52, 59)', '#30343b'].includes(text.fill)));
      if (drawing.kind === 'chart') {
        assert.equal(drawing.titleText, chartRecord().definition.title);
        assert.ok(drawing.titleSizes.length && drawing.titleSizes.every(size => size >= 13));
        assert.ok(drawing.lineWidths.length && drawing.lineWidths.every(width => width >= 1.5));
        assert.ok(drawing.lineWidths.some(width => width >= 2.5));
      }
    }
    assert.equal(await ep.locator('#ppt-download').getAttribute('href'), prefix + '/api/sn/ppt-templates/template-1/exports/export-1');
    assert.equal(await ep.locator('#ppt-export-report img').count(), 0); assert.equal(await ep.evaluate(() => window.__xss), undefined);
    assert.equal(await ep.locator('[data-preview-slot="table-slot"] img').count(), 1); await exporting.close();

    let reads = 0, posted = false;
    const stale = await open(browser, '/sn/reports/ppt/template-1/export', render('ppt_export.html'), ({method, reply}) => {
      if (method === 'POST') posted = true;
      const value = pptRecord(); value.revision = ++reads;
      return reply(value);
    });
    await stale.page.waitForFunction(() => !document.querySelector('#ppt-export-fields').disabled);
    assert.equal(await stale.page.locator('#ppt-export-date').inputValue(), await stale.page.evaluate(() => window.PptUI.today()));
    await stale.page.locator('#ppt-export-form button').click(); await stale.page.locator('#ppt-export-conflict').waitFor({state: 'visible'});
    assert.equal(posted, false); await stale.close();

    const changing = pptRecord();
    const raced = await open(browser, '/sn/reports/ppt/template-1/export', render('ppt_export.html', {date: '2029-02-30'}), ({method, reply}) => {
      if (method !== 'POST') return reply(changing);
      changing.revision = 3;
      const report = {revision: 3, date: '2029-09-15', export_id: 'raced-export', updated_slots: [], kept_slots: [], headlines_to_check: [],
        download_url: '/api/sn/ppt-templates/template-1/exports/raced-export'};
      changing.exports.push(report); return reply(report);
    });
    await raced.page.waitForFunction(() => !document.querySelector('#ppt-export-fields').disabled);
    assert.equal(await raced.page.locator('#ppt-export-date').inputValue(), await raced.page.evaluate(() => window.PptUI.today()));
    await raced.page.locator('#ppt-export-form button').click(); await raced.page.locator('#ppt-export-conflict').waitFor({state: 'visible'});
    assert.equal(await raced.page.locator('#ppt-download').count(), 0);
    await raced.page.locator('#ppt-export-reload').click(); await raced.page.locator('#ppt-meta').filter({hasText: 'v3'}).waitFor();
    assert.equal(await raced.page.locator('#ppt-download').count(), 0, '已知版本冲突的导出在重新加载后也不能作为有效结果下载');
    await raced.close();
    console.log('ppt-ui: passed');
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });

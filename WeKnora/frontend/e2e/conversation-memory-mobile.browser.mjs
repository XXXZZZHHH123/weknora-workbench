// Isolated browser check: API calls below use in-memory fixtures only.
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const fs = require('fs'), assert = require('assert/strict'), root = process.env.REVIEW_EVIDENCE_DIR;
if (!root || !process.env.PLAYWRIGHT_PACKAGE)
    throw new Error('Set REVIEW_EVIDENCE_DIR and PLAYWRIGHT_PACKAGE');
fs.mkdirSync(root, { recursive: true });
const { chromium } = require(process.env.PLAYWRIGHT_PACKAGE);
const a = { token: 'isolated-ui-fixture-token', refresh_token: 'isolated-ui-fixture-refresh' }, me = { user: { id: '11111111-1111-4111-8111-111111111111', username: 'Interface fixture', email: 'review@example.invalid', is_active: true, is_system_admin: false }, tenant: { id: 10000, name: 'Isolated interface fixture', role: 'owner', is_active: true }, memberships: [{ tenant_id: 10000, role: 'owner' }], tenant_required: false, capabilities: { can_create_tenant: true } };
let browser, page;
(async () => {
    const b = await chromium.launch({ args: ['--no-sandbox', '--disable-dev-shm-usage'] });
    browser = b;
    const c = await b.newContext({ viewport: { width: 390, height: 844 }, timezoneId: 'Asia/Shanghai' });
    await c.addInitScript(({ a, me }) => { for (const [k, v] of Object.entries({ weknora_token: a.token, weknora_refresh_token: a.refresh_token, weknora_user: JSON.stringify(me.user), weknora_tenant: JSON.stringify(me.tenant), weknora_memberships: JSON.stringify(me.memberships), weknora_selected_tenant_id: String(me.tenant.id), 'weknora:new-user-guide-done:v1': '1', 'weknora:assistant-entry-guide-done:v0.9.0': '1', sidebar_collapsed: 'false', sidebar_width: '300', 'weknora:contextual-guide-kb-list:v2': '1', 'weknora:contextual-guide-chat:v1': '1' }))
        localStorage.setItem(k, v); }, { a, me });
    const p = await c.newPage(), errors = [], writes = [], results = [];
    p.on('pageerror', e => errors.push(e.message));
    page = p;
    let fakeSession = null;
    p.on('console', m => { if (m.type() === 'error')
        console.log('console', m.text()); });
    p.on('requestfailed', r => console.log('requestfail', r.url(), r.failure()));
    await p.route(new URL('/api/**', process.env.REVIEW_UI_URL || 'http://127.0.0.1:18291').href, async (route) => {
        const r = route.request(), u = new URL(r.url());
        try {
            if (r.method() === 'GET') {
                if (u.pathname === '/api/v1/organizations')
                    return route.fulfill({ json: { success: true, data: { organizations: [], resource_counts: {} } } });
                if (u.pathname === '/api/v1/auth/me')
                    return route.fulfill({ json: { success: true, data: me } });
                if (u.pathname === '/api/v1/auth/tenant')
                    return route.fulfill({ json: { success: true, data: me.tenant } });
                if (u.pathname === '/api/v1/auth/config')
                    return route.fulfill({ json: { success: true, registration_mode: 'self_serve' } });
                if (u.pathname === '/api/v1/models')
                    return route.fulfill({ json: { success: true, data: [{ id: 'fixture-model', name: 'Fixture Model', type: 'KnowledgeQA', source: 'remote', is_default: true, parameters: { base_url: 'https://example.invalid', temperature: 0.7, context_window: 200000 } }] } });
                if (u.pathname === '/api/v1/agents')
                    return route.fulfill({ json: { success: true, data: [{ id: 'builtin-quick-answer', name: '快速问答', is_builtin: true, config: { agent_mode: 'quick-answer', model_id: 'fixture-model', multi_turn_enabled: true } }] } });
                if (u.pathname === '/api/v1/sessions/review-private-fixture')
                    return route.fulfill({ json: { success: true, data: { id: 'review-private-fixture', title: '临时记忆界面验收', memory_disabled: true, has_user_message: false } } });
                return route.fulfill({ json: { success: true, data: [], total: 0, unread_count: 0 } });
            }
            writes.push({ method: r.method(), path: u.pathname });
            if (r.method() === 'POST' && u.pathname === '/api/v1/sessions') {
                fakeSession = r.postDataJSON();
                return route.fulfill({ status: 201, json: { success: true, data: { id: 'review-private-fixture', title: '临时记忆界面验收', memory_disabled: fakeSession.memory_disabled } } });
            }
            if (u.pathname.startsWith('/api/v1/knowledge-chat/') || u.pathname.startsWith('/api/v1/agent-chat/'))
                return route.fulfill({ status: 503, json: { error: 'isolated UI fixture: model calls disabled' } });
            return route.fulfill({ status: 200, json: { success: true, data: [] } });
        }
        catch (e) {
            return route.fulfill({ status: 502, json: { error: 'UI fixture upstream unavailable' } });
        }
    });
    await p.goto(new URL('/platform/knowledge-bases', process.env.REVIEW_UI_URL || 'http://127.0.0.1:18291').href, { waitUntil: 'domcontentloaded' });
    await p.locator('.aside_box--collapsed').waitFor({ timeout: 60000 });
    results.push('390px initial drawer collapsed despite saved desktop expanded');
    assert.equal(await p.evaluate(() => localStorage.getItem('sidebar_collapsed')), 'false');
    await p.locator('.mobile-sidebar-toggle-trigger').click();
    await p.locator('.mobile-sidebar-backdrop').waitFor();
    await p.keyboard.press('Escape');
    await p.locator('.aside_box--collapsed').waitFor();
    assert.equal(await p.locator('.mobile-sidebar-toggle-trigger').evaluate(el => el === document.activeElement), true);
    results.push('Escape closes drawer and restores focus');
    await p.locator('.mobile-sidebar-toggle-trigger').click();
    await p.locator('.mobile-sidebar-backdrop').click({ position: { x: 360, y: 300 } });
    await p.locator('.aside_box--collapsed').waitFor();
    results.push('backdrop closes drawer');
    await p.screenshot({ path: root + '/mobile-sidebar-fixed.png', fullPage: true });
    await p.setViewportSize({ width: 1440, height: 1000 });
    await p.locator('.aside_box:not(.aside_box--collapsed)').waitFor();
    await p.waitForFunction(() => Math.round(document.querySelector('.aside_box').getBoundingClientRect().width) === 280);
    assert.equal(await p.locator('.aside_box').evaluate(el => Math.round(el.getBoundingClientRect().width)), 280);
    results.push('desktop expanded state and saved width restored within existing 280px maximum');
    await p.goto(new URL('/platform/creatChat', process.env.REVIEW_UI_URL || 'http://127.0.0.1:18291').href, { waitUntil: 'domcontentloaded' });
    await p.locator('.conversation-memory-toggle').waitFor({ timeout: 30000 });
    await p.locator('.conversation-memory-toggle').click();
    assert.equal(await p.locator('.conversation-memory-toggle').getAttribute('aria-pressed'), 'true');
    results.push('new conversation memory opt-out can be selected');
    await p.screenshot({ path: root + '/new-chat-no-memory.png', fullPage: true });
    await p.goto(new URL('/platform/chat/review-private-fixture', process.env.REVIEW_UI_URL || 'http://127.0.0.1:18291').href, { waitUntil: 'domcontentloaded' });
    await p.locator('.conversation-memory-toggle[aria-disabled="true"]').waitFor({ timeout: 30000 });
    await p.reload({ waitUntil: 'domcontentloaded' });
    await p.locator('.conversation-memory-toggle[aria-disabled="true"]').waitFor({ timeout: 30000 });
    results.push('stored conversation policy restored and locked after refresh');
    await p.screenshot({ path: root + '/persisted-no-memory.png', fullPage: true });
    fs.writeFileSync(root + '/main-ui-result.json', JSON.stringify({ results, errors, writes }, null, 2));
    console.log(JSON.stringify({ results, errors, writes }));
    assert.equal(errors.length, 0);
    await b.close();
})().catch(async (e) => { console.error(e); if (page) {
    console.error('PAGE', page.url(), await page.locator('body').innerText());
    await page.screenshot({ path: root + '/main-ui-failure.png', fullPage: true }).catch(() => { });
} process.exitCode = 1; }).finally(async () => { if (browser)
    await browser.close(); });

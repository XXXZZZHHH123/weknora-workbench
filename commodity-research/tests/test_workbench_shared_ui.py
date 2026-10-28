"""The original research UI must remain isolated by the verified workbench."""
import pytest
from sqlalchemy import select

from tests.test_isolated_workbenches import isolated, WB, OTHER


def page(suffix="/", workbench=WB):
    return f"/research/workbenches/{workbench}{suffix}"


def login(client, headers):
    response = client.post("/research/auth/session", headers=headers)
    assert response.status_code == 200, response.text


def no_legacy(monkeypatch):
    from tin import db
    def forbidden(*args, **kwargs):
        raise AssertionError("An isolated page opened the legacy tenant database")
    monkeypatch.setattr(db, "tenant_session_factory", forbidden)


def test_original_pages_share_layout_and_scope_every_link(isolated, monkeypatch):
    client, headers, state, tmp = isolated
    login(client, headers)
    no_legacy(monkeypatch)
    for suffix in ("/", "/sn", "/sn/indicators", "/sn/judgment", "/sn/strategies", "/sn/diagram", "/sn/reports", "/sn/entry"):
        response = client.get(page(suffix))
        assert response.status_code == 200, (suffix, response.text)
        assert 'class="research-nav-main"' in response.text
        assert 'class="research-main max-w-7xl' in response.text
        assert f'window.RESEARCH_PREFIX = "/research/workbenches/{WB}"' in response.text
        assert f'href="/research/workbenches/{WB}/sn/indicators"' in response.text
        assert 'src="/research/static/app.js' in response.text
        assert 'href="/research/sn' not in response.text
        assert '/platform/materials' not in response.text
        assert '锡 · SN' not in response.text
        assert '锡研究工作台' not in response.text
    assert not (tmp / "workspaces").exists()
    assert client.get('/research/static/app.js').status_code == 200


def test_bootstrap_targets_scope_without_exposing_bearer(isolated):
    client, headers, state, tmp = isolated
    response = client.get('/research/auth/bootstrap', params={'workbench_id': WB})
    assert response.status_code == 200
    assert f'/research/workbenches/{WB}/' in response.text
    assert 'writer' not in response.text
    assert 'location.replace' in response.text
    assert not list(tmp.rglob('*.db'))


def test_every_page_revalidates_scope_and_refuses_unknown_or_revoked(isolated):
    client, headers, state, tmp = isolated
    assert client.get(page()).status_code == 401
    login(client, headers)
    assert client.get(page()).status_code == 200
    assert client.get(page(workbench='10000000-0000-4000-8000-000000000099')).status_code == 404
    state['revoked_kb'] = True
    assert client.get(page()).status_code == 403
    assert client.get(page('/api/sn/indicators/catalog')).status_code == 403
    assert not (tmp / 'workspaces').exists()


def test_two_workbenches_have_distinct_original_ui_records_and_downloads(isolated, monkeypatch):
    client, headers, state, tmp = isolated
    login(client, headers)
    no_legacy(monkeypatch)
    layout = {'name': 'User structure', 'nodes': [], 'edges': []}
    response = client.post(page('/api/sn/diagram/templates'), headers={'Origin':'http://testserver'},
                           json={'name':'First workbench only','layout':layout})
    assert response.status_code == 200, response.text
    first = response.json()['saved']['id']
    assert client.get(page('/sn/diagram')).status_code == 200
    other = client.get(page('/sn/diagram', OTHER))
    assert other.status_code == 200, other.text
    assert 'First workbench only' not in other.text
    assert client.get(page(f'/api/sn/diagram/markdown?template_id={first}', OTHER)).status_code == 200
    # Each scope gets only its own empty/user-authored structure, never the seed graph.
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import Indicator, DiagramTemplate
    with isolated_session_factory(17, WB)() as session:
        assert session.scalar(select(Indicator)) is None
        assert session.scalar(select(DiagramTemplate)).name == 'First workbench only'
    with isolated_session_factory(17, OTHER)() as session:
        assert session.scalar(select(Indicator)) is None
        assert not any(row.layout.get('nodes') for row in session.scalars(select(DiagramTemplate)))
    assert not (tmp / 'workspaces').exists()


@pytest.mark.parametrize('suffix,method', [('/api/sn/refresh/run','post'),('/api/sn/refresh/settings','put'),
    ('/api/sn/import/terminal/commit','post'),('/api/sn/import/preview','post'),('/api/sn/import/commit','post'),
    ('/materials','get'),('/api/materials/library','post'),('/files','get'),('/api/library/assets','get')])
def test_unscoped_side_effects_cannot_escape_through_scoped_urls(isolated, suffix, method):
    client, headers, state, tmp = isolated
    login(client, headers)
    response = getattr(client, method)(page(suffix), headers={'Origin':'http://testserver'})
    assert response.status_code in {404,409}, (suffix,response.text)
    assert not (tmp / 'workspaces').exists()


def test_viewer_and_csrf_are_still_enforced_for_original_writes(isolated):
    client, headers, state, tmp = isolated
    assert client.post(page('/api/sn/diagram/templates'), headers=headers, json={}).status_code != 200
    viewer = {**headers, 'Authorization':'Bearer viewer', 'X-Research-User':'viewer'}
    login(client, viewer)
    assert client.post(page('/api/sn/diagram/templates'), headers={'Origin':'http://testserver'}, json={}).status_code == 403
    login(client, headers)
    assert client.post(page('/api/sn/diagram/templates'), json={}).status_code == 403


def test_confirmed_native_source_appears_in_original_cards_and_history(isolated, monkeypatch):
    from tests.test_isolated_workbenches import confirm_kb_source
    client, headers, state, tmp = isolated
    series = confirm_kb_source(state)
    login(client, headers)
    no_legacy(monkeypatch)
    state['calls'].clear()
    with monkeypatch.context() as home_patch:
        def forbidden(*args, **kwargs):
            raise AssertionError('The affairs homepage opened a market fact session')
        home_patch.setattr('tin.web.app._page_session', forbidden)
        homepage = client.get(page('/'))
    assert homepage.status_code == 200
    assert '事务中心' in homepage.text and '全景数据墙' not in homepage.text
    assert not any(path in {'/api/research/facts', '/api/research/export'} for method, path in state['calls'])
    # Facts stay visible in the market/indicator pages, independently of inbox metadata.
    for suffix in ('/sn', '/sn/indicators', '/sn/indicators/' + series):
        response = client.get(page(suffix))
        assert response.status_code == 200, (suffix, response.text)
        assert 'Quantity' in response.text
        assert 'SHFE.SN' not in response.text and 'MACRO.VIX' not in response.text
    for workbench in (WB, OTHER):
        history = client.get(page('/api/sn/series/' + series + '/history', workbench))
        assert history.status_code == 200, history.text
        assert history.json()['points'] == [{'date': '2026-01-01', 'value': 7.5}]
        catalog = client.get(page('/api/sn/indicators/catalog', workbench)).json()['indicators']
        assert [row['series_id'] for row in catalog] == [series]
    assert not (tmp / 'workspaces').exists()


@pytest.mark.parametrize('view,suffix', [('home','/'),('diagram','/sn/diagram'),('indicators','/sn/indicators'),
    ('judgment','/sn/judgment'),('strategies','/sn/strategies'),('reports','/sn/reports')])
def test_bootstrap_scope_and_view_are_fixed_protocol(isolated, view, suffix):
    client, headers, state, tmp = isolated
    response = client.get('/research/auth/bootstrap', params={'workbench_id':WB,'view':view})
    assert response.status_code == 200
    assert f'location.replace("/research/workbenches/{WB}{suffix}")' in response.text
    assert 'event.data.workbenchId !== expectedWorkbench' in response.text
    assert 'const expectedWorkbench = "' + WB + '"' in response.text
    assert not list(tmp.rglob('*.db'))


def test_bootstrap_rejects_arbitrary_redirect_and_invalid_scope(isolated):
    client, headers, state, tmp = isolated
    assert client.get('/research/auth/bootstrap', params={'workbench_id':WB,'view':'https://outside.test'}).status_code == 400
    assert client.get('/research/auth/bootstrap', params={'workbench_id':'invalid'}).status_code == 400


@pytest.mark.parametrize('tail', ['/workbenches/', '/workbenches/invalid/', '/workbenches/' + WB + '//sn',
    '/workbenches/' + WB + '/%252e%252e/sn', '/workbenches/' + WB + '/api/library/assets'])
def test_malformed_and_unrelated_scope_paths_are_rejected(isolated, tail):
    client, headers, state, tmp = isolated
    login(client, headers)
    assert client.get('/research' + tail).status_code == 404
    assert not (tmp / 'workspaces').exists()


def test_original_judgment_form_preserves_private_input_without_saving_versions(isolated, monkeypatch):
    client, headers, state, tmp = isolated
    login(client, headers)
    no_legacy(monkeypatch)
    text = '## 主逻辑\nFirst workbench user-authored evidence. <script>unsafe()</script>'
    response = client.post(page('/sn/judgment/import'), headers={'Origin':'http://testserver'},
                           data={'text':text,'author':'forged'}, follow_redirects=False)
    assert response.status_code == 200, response.text
    assert 'First workbench user-authored evidence.' in response.text
    assert '&lt;script&gt;unsafe()&lt;/script&gt;' in response.text
    assert '<script>unsafe()</script>' not in response.text
    assert '管理研究逻辑' in response.text and 'logic-input' in response.text
    assert not response.headers.get('location'), 'Private source must not be put in a redirect URL'
    assert 'First workbench user-authored evidence.' not in client.get(page('/sn/judgment', OTHER)).text
    second = client.post(page('/sn/judgment/import'), headers={'Origin':'http://testserver'},
                         data={'text':'## 主逻辑\nRevised evidence.','author':'writer'}, follow_redirects=False)
    assert second.status_code == 200 and 'Revised evidence.' in second.text
    from tin.integration.workbench_scope import isolated_session_factory
    from tin.models import Judgment
    with isolated_session_factory(17, WB)() as session:
        assert session.scalars(select(Judgment)).all() == [], 'Model preview and explicit confirmation are required'


def test_source_indicator_manual_edit_and_report_export_use_original_scope(isolated, monkeypatch):
    from tests.test_isolated_workbenches import confirm_kb_source
    client, headers, state, tmp = isolated
    series = confirm_kb_source(state, [{'date': '2026-01-02', 'value': 8.5, 'value_status': 'valid',
                                     'source_cell': 'B3', 'date_cell': 'A3'}])
    login(client, headers)
    no_legacy(monkeypatch)
    entry = client.post(page('/sn/entry'), headers={'Origin':'http://testserver'}, data={
        'series_id':series,'value':'99','as_of':'2026-01-02T15:00','entered_by':'forged',
        'note':'Attempted separate research copy.','return_to':'/sn/entry'}, follow_redirects=False)
    assert entry.status_code == 409 and '知识库' in entry.text
    reports = client.get(page('/sn/reports'))
    assert reports.status_code == 200 and '2026-01-02' in reports.text
    for workbench in (WB, OTHER):
        snapshot = client.get(page('/sn/reports/2026-01-02.json', workbench))
        assert snapshot.status_code == 200, snapshot.text
        assert any(row['series_id'] == series and row['value'] == 8.5 for row in snapshot.json()['observations'])
    exported = client.post(page('/api/sn/export/excel'), headers={'Origin':'http://testserver'}, json={
        'actor':'forged','columns':[{'field':series,'kind':'observation','label':'Quantity'}], 'date_range':'recent_30_trade_days'})
    assert exported.status_code == 200, exported.text
    from openpyxl import load_workbook
    import io
    workbook = load_workbook(io.BytesIO(exported.content))
    assert any(8.5 in row for sheet in workbook for row in sheet.iter_rows(values_only=True))
    assert not (tmp / 'workspaces').exists()


def test_original_strategy_drafts_stay_in_current_workbench(isolated, monkeypatch):
    from tests.test_research_ownership import plan
    client, headers, state, tmp = isolated
    login(client, headers)
    no_legacy(monkeypatch)
    draft = plan().model_dump(mode='json')
    draft['title'] = 'User-authored independent draft'
    created = client.post(page('/api/sn/strategies'), headers={'Origin':'http://testserver'}, json=draft)
    assert created.status_code == 201, created.text
    identifier = created.json()['id']
    assert created.json()['status'] == 'draft'
    assert 'User-authored independent draft' in client.get(page('/sn/strategies')).text
    assert 'User-authored independent draft' not in client.get(page('/sn/strategies', OTHER)).text
    assert client.get(page(f'/api/sn/strategies/{identifier}', OTHER)).status_code == 404
    assert client.get(page(f'/sn/strategies/{identifier}')).status_code == 200
    rejected = client.post(page(f'/api/sn/strategies/{identifier}/publish'), headers={'Origin':'http://testserver'}, json={})
    assert rejected.status_code == 400  # Missing real quotes are not fabricated to publish a draft.
    missing_model = client.post(page('/api/sn/strategies/generate'), headers={'Origin':'http://testserver'}, json={})
    assert missing_model.status_code == 400 and '模型' in missing_model.json()['detail']
    assert not (tmp / 'workspaces').exists()


def _html_elements(html):
    from html.parser import HTMLParser
    class Elements(HTMLParser):
        def __init__(self):
            super().__init__()
            self.rows = []
        def handle_starttag(self, tag, attrs):
            self.rows.append((tag, dict(attrs)))
    parsed = Elements()
    parsed.feed(html)
    return parsed.rows


def test_empty_workbench_guidance_and_quick_entry_lead_to_scoped_sources(isolated):
    client, headers, state, tmp = isolated
    login(client, headers)
    for suffix in ('/', '/sn'):
        response = client.get(page(suffix))
        assert response.status_code == 200
        if suffix == '/sn':
            assert '请在「资料与来源」准备并确认本知识库资料' in response.text
        else:
            assert '事务中心' in response.text and '尚未进行本次检查' in response.text
        assert '「数据更新」中立即更新' not in response.text
        assert '下方「数据更新」中立即更新' not in response.text
        elements = _html_elements(response.text)
        quick = next(attrs for tag, attrs in elements if tag == 'button' and attrs.get('id') == 'quick-entry')
        assert "research-open-sources" in quick['onclick']
        assert 'disabled' not in quick
        assert 'px-3 py-1.5' in quick['class']
        assert not any(attrs.get('id') == 'drawer-form' for tag, attrs in elements)
    error = client.get(page('/api/sn/snapshot'))
    assert error.status_code == 404
    assert '资料与来源' in error.json()['detail']


def test_empty_viewer_cannot_use_quick_entry_to_write(isolated):
    client, headers, state, tmp = isolated
    login(client, {**headers, 'Authorization':'Bearer viewer', 'X-Research-User':'viewer'})
    response = client.get(page())
    assert response.status_code == 200
    elements = _html_elements(response.text)
    quick = next(attrs for tag, attrs in elements if tag == 'button' and attrs.get('id') == 'quick-entry')
    assert 'disabled' in quick
    assert not any(attrs.get('id') == 'drawer-form' for tag, attrs in elements)


def test_confirmed_manual_indicator_keeps_original_quick_entry_form(isolated):
    from tests.test_isolated_workbenches import confirm_kb_source
    client, headers, state, tmp = isolated
    confirm_kb_source(state)
    login(client, headers)
    response = client.get(page())
    elements = _html_elements(response.text)
    quick = next(attrs for tag, attrs in elements if tag == 'button' and attrs.get('id') == 'quick-entry')
    assert 'research-open-sources' in quick['onclick']
    assert 'disabled' not in quick
    assert not any(attrs.get('id') == 'drawer-form' for tag, attrs in elements)
    entry = client.get(page('/sn/entry'))
    assert entry.status_code == 200 and '知识库数据维护' in entry.text
    assert 'name="value"' not in entry.text


@pytest.mark.parametrize('configured', [False, True])
@pytest.mark.parametrize('method', ['get','post'])
def test_empty_diagram_values_keep_actual_missing_states_and_nullable_cutoff(isolated, monkeypatch, configured, method):
    from tests.test_isolated_workbenches import confirm_kb_source
    client, headers, state, tmp = isolated
    login(client, headers)
    no_legacy(monkeypatch)
    nodes = []
    if configured:
        series = confirm_kb_source(state, [])
        nodes = [{'id':'unbound','label':'Awaiting selection','binding':{'kind':'none'}},
                 {'id':'registered','label':'Configured real field','binding':{'kind':'series','series_id':series}},
                 {'id':'missing','label':'Unknown registration','binding':{'kind':'series','series_id':'not-in-this-workbench'}},
                 {'id':'derived','label':'Not calculated','binding':{'kind':'derived','formula_id':'BASIS'}}]
    layout = {'nodes':nodes,'edges':[]}
    created = client.post(page('/api/sn/diagram/templates'), headers={'Origin':'http://testserver'},
                          json={'name':'Empty observation fixture','layout':layout})
    assert created.status_code == 200, created.text
    template = created.json()['saved']['id']
    response = (client.get(page(f'/api/sn/diagram/values?template_id={template}')) if method == 'get'
                else client.post(page('/api/sn/diagram/values'), headers={'Origin':'http://testserver'}, json={'layout':layout,'date':None}))
    assert response.status_code == 200, response.text
    body = response.json()
    assert body['as_of'] is None and body['judgment'] == {}
    assert len(body['nodes']) == len(nodes)
    if method == 'get':
        assert body['template_id'] == template
    assert all(node['value'] is None and node['as_of'] is None for node in body['nodes'])
    if configured:
        assert {node['node_id']:node['state'] for node in body['nodes']} == {
            'unbound':'unbound','registered':'no_data','missing':'retired','derived':'no_data'}
        assert body['nodes'][1]['unit'] == '吨'
    assert not (tmp / 'workspaces').exists()


@pytest.mark.parametrize('method', ['get','post'])
def test_diagram_values_after_confirmation_resolve_only_current_workbench(isolated, monkeypatch, method):
    from tests.test_isolated_workbenches import confirm_kb_source
    client, headers, state, tmp = isolated
    series = confirm_kb_source(state)
    login(client, headers)
    no_legacy(monkeypatch)
    layout={'nodes':[{'id':'measurement','label':'Actual field','binding':{'kind':'series','series_id':series}}],'edges':[]}
    for workbench in (WB, OTHER):
        created = client.post(page('/api/sn/diagram/templates', workbench), headers={'Origin':'http://testserver'},
                              json={'name':'Source verification fixture','layout':layout})
        assert created.status_code == 200
        template = created.json()['saved']['id']
        response = (client.get(page(f'/api/sn/diagram/values?template_id={template}', workbench)) if method == 'get'
                    else client.post(page('/api/sn/diagram/values', workbench), headers={'Origin':'http://testserver'}, json={'layout':layout}))
        assert response.status_code == 200, response.text
        assert response.json()['as_of'] == '2026-01-01'
        assert response.json()['nodes'][0]['state'] == 'ok'
        assert response.json()['nodes'][0]['value'] == 7.5


def test_empty_legacy_diagram_values_keep_existing_503_behavior(isolated):
    client, headers, _, tmp = isolated
    login(client, headers)
    response = client.get('/research/api/sn/diagram/values')
    assert response.status_code == 409 and '工作台' in response.text
    assert not (tmp / 'workspaces').exists()


def test_diagram_cutoff_before_real_observations_is_missing_not_a_source_date(isolated):
    from tests.test_isolated_workbenches import confirm_kb_source
    client, headers, state, tmp = isolated
    series = confirm_kb_source(state)
    login(client, headers)
    layout = {'nodes':[{'id':'measurement','label':'Quantity','binding':{'kind':'series','series_id':series}}],'edges':[]}
    response = client.post(page('/api/sn/diagram/values'), headers={'Origin':'http://testserver'},
                           json={'date':'2025-12-31','layout':layout})
    assert response.status_code == 200, response.text
    assert response.json()['as_of'] is None
    assert response.json()['nodes'][0]['state'] == 'no_data'
    assert response.json()['nodes'][0]['value'] is None
    assert client.post(page('/api/sn/diagram/values'), headers={'Origin':'http://testserver'},
                       json={'date':'not-a-date','layout':{'nodes':[]}}).status_code == 400

"""Real scoped report assembly and per-key chart reads share the saved chart pins."""
from copy import deepcopy
from datetime import date
import json
from urllib.parse import quote

from fastapi import Body
import httpx
import pytest

from tin.config import settings
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.schemas.judgment import JudgmentIn
from tin.web import app as web, chart_references, report_drafts, report_figures, research_strategy
from tin.web.report_view import readable_markdown
from tests.chart_fixtures import SID, OTHER
from tests.test_chart_api import chart_client  # noqa: F401
from tests.test_chart_references import reference_client, create, frozen  # noqa: F401
from tests.test_research_strategy_cards import entry
from tests.test_workbench_sources_v2 import source


@pytest.fixture
def report_flow(reference_client, monkeypatch):
    client, state = reference_client
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://fixture.invalid')
    original = __import__('tin.provenance.bridge', fromlist=['structured_request']).structured_request
    direct = source(1)

    def transport(method, endpoint, body, **kwargs):
        current_workbench.get().metadata.setdefault('name', '隔离报告研究台')
        if endpoint != '/api/research/facts' or body.get('catalog_only') or body.get('view_module') == 'indicators':
            return original(method, endpoint, body, **kwargs)
        from tin.integration.page_facts import _page
        request = _page.get()
        if request is not None:
            assert request['module'] == body['view_module']
        assert body['view_module'] in {'reports', 'judgments'}
        assert len(body['series_ids']) <= 256 and len(body['series_ids']) * body['points_per_series'] <= 20000
        state.requests.append((endpoint, deepcopy(body)))
        ids = body['series_ids']
        fields = [{**field, 'name': field['metric'], 'source_binding_id': direct['id'],
            'knowledge_base_id': direct['knowledge_base_id'], 'knowledge_base_tenant_id': 17,
            'source_type': direct['source_type'], 'source_id': direct['source_id'],
            'source_knowledge_id': 'fixture-document', 'dataset_id': 'fixture-dataset',
            'source_series_id': sid, 'source_file': 'fixture.xlsx', 'sheet': '测试表'}
            for sid, field in state.fields.items() if sid in ids]
        rows, totals = [], {}
        for field in fields:
            sid = field['series_id']
            points = [(stamp, value) for stamp, value in state.observations[sid] if stamp <= body['as_of']]
            totals[sid] = len(points)
            rows.extend({'series_id': sid, 'date': stamp, 'value': value, 'value_status': 'valid',
                'source_cell': 'M' + str(index), 'source_binding_id': direct['id']}
                for index, (stamp, value) in enumerate(points[-body['points_per_series']:]))
        return httpx.Response(200, json={'scope_version': 2, 'workbench_id': current_workbench.get().workbench_id,
            'sources_revision': 7, 'version': state.facts_version, 'bounded': True, 'indicators': fields,
            **({'include_invalid': True} if body.get('include_invalid') else {}),
            'rows': rows, 'datasets': [], 'source_versions': {direct['id']: 'e' * 64}, 'series_totals': totals,
            'unavailable_series_ids': list(set(ids) - set(state.fields)), 'offset': 0, 'total': len(rows),
            'next_offset': None, 'truncated': False}, request=httpx.Request(method, 'http://fixture.invalid' + endpoint))

    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    state.app.include_router(report_figures.router)
    state.app.include_router(report_drafts.router)
    state.app.include_router(research_strategy.router)
    state.app.add_api_route('/sn/reports', web.reports_page)
    state.app.add_api_route('/sn/reports/{day}/weekly.json', web.weekly_report_json)
    state.app.add_api_route('/sn/reports/{day}/weekly.md', web.weekly_report_markdown)

    @state.app.post('/fixture/judgment')
    def save_judgment(body: dict = Body()):
        from tin.judgments.service import save_version
        scope = current_workbench.get()
        state.scope = scope
        fields, _ = chart_references._metadata(scope, [SID, OTHER])
        with artifact_session(scope) as session:
            session.info.update(knowledge_facts=True, knowledge_facts_catalog=fields)
            row = save_version(session, JudgmentIn.model_validate(body), actor=scope.identity.user_id)
            session.commit()
            return {'id': row.id, 'version': row.version}

    return client, state


def test_saved_judgment_strategy_and_manual_selection_flow_through_report_and_real_manifest(report_flow):
    client, state = report_flow
    chart = create(client, title='保存的原修订图')
    first_pin = frozen(client, chart)
    judgment = {'variety': 'SN', 'author': 'first', 'written_at': '2023-01-02',
        'contradiction': {'bull': {'claim': '已保存支持观点', 'evidence': [
            {'desc': '已保存依据甲', 'series_id': SID}, {'desc': '已保存依据乙', 'series_id': OTHER}]},
            'bear': {'claim': '已保存反对观点', 'evidence': []}},
        'chart_references': [{'kind': 'chart', 'id': chart['id'], 'revision': 1,
            'target': {'kind': 'evidence', 'side': 'bull', 'index': 0}}]}
    saved = client.post('/fixture/judgment', json=judgment)
    assert saved.status_code == 200, saved.text
    changed = client.patch('/api/sn/charts/' + chart['id'], json={'revision': 1, 'title': '策略引用修订图'})
    assert changed.status_code == 200, changed.text
    card = entry(source_ids=[])
    card['card'].update(title='真实保存的策略方案', citations=[{'kind': 'chart', 'id': chart['id'], 'revision': 2, 'field': 'trigger'}])
    saved_card = client.post('/api/sn/research-strategies', json=card)
    assert saved_card.status_code == 201, saved_card.text
    assert client.patch('/api/sn/charts/' + chart['id'], json={'revision': 2, 'title': '不能偷偷使用的最新修订'}).status_code == 200
    draft = {'format': 'research-report-draft.v1', 'period': 'weekly', 'date': '2023-01-03', 'title': '真实报告草稿',
        'figures': [{'series_id': SID, 'caption': '保留但展示去重'}, {'series_id': OTHER, 'caption': '保留的人工图说'}],
        'sections': {key: {'visible': True, 'text': '保存的图表按语' if key == 'dashboard' else ''}
            for key in ('summary', 'research', 'dashboard', 'watch')}, 'references': []}
    created = client.post('/api/sn/report-drafts', json={'draft': draft})
    assert created.status_code == 201, created.text
    revision = created.json()['revision']
    response = client.get('/sn/reports/2023-01-03/weekly.json')
    assert response.status_code == 200, response.text
    payload = response.json()
    figures = payload['report_figures']
    assert [figure['key'] for figure in figures] == ['chart:' + chart['id'], 'indicator:' + OTHER]
    assert figures[0]['reference']['revision'] == 2 and figures[0]['title'] == '策略引用修订图'
    assert figures[0]['unit'] == '件' and figures[0]['as_of'] == '2023-01-03'
    assert figures[0]['caption'] == '引用于：研判（支持依据「已保存依据甲」）、策略「真实保存的策略方案」（适用 / 触发）'
    assert figures[1]['caption'] == '保留的人工图说'
    assert payload['observations'][0]['value'] == 12
    assert payload['judgment']['contradiction']['chart_references'][0]['revision'] == first_pin['revision']
    page = client.get('/sn/reports?date=2023-01-03&period=weekly')
    assert page.status_code == 200, page.text
    main = page.text
    order = ['id="report-overview"', 'id="report-logic-strategy"', 'id="report-dashboard"', 'id="report-full-record"', 'id="report-research"', 'id="report-strategy"']
    assert [main.index(marker) for marker in order] == sorted(main.index(marker) for marker in order)
    assert '策略引用修订图' in main and '引用于：研判' in main and '保存的图表按语' in main
    assert 'data-report-figure="indicator:' + SID not in main
    markdown = client.get('/sn/reports/2023-01-03/weekly.md')
    assert markdown.status_code == 200, markdown.text
    charts = markdown.text.split('\n## 图表\n', 1)[1].split('\n## 策略\n', 1)[0]
    assert '策略引用修订图' in charts and '保留的人工图说' in charts
    assert '引用于：研判' in charts and '单位：件' in charts and '数据截至日：2023-01-03' in charts
    assert '![' not in charts
    # The real route rebuilds report_manifest from saved, scoped research. No monkeypatch of it.
    key_url = '/api/sn/reports/figures/' + quote(figures[0]['key'], safe='') + '?date=2023-01-03&period=weekly&revision=' + str(revision)
    rendered = client.get(key_url)
    assert rendered.status_code == 200, rendered.text
    value = rendered.json()
    assert value['rendered']['definition']['title'] == '策略引用修订图'
    assert value['rendered']['payload']['revision'] == 2
    assert value['rendered']['as_of'] == '2023-01-03'
    assert [point['date'] for point in value['rendered']['payload']['curves'][0]['points']] == ['2023-01-01', '2023-01-02', '2023-01-03']
    assert client.get(key_url, headers={'x-restricted': 'true'}).status_code == 403
    assert client.get(key_url, headers={'x-workbench': '10000000-0000-4000-8000-000000000092'}).status_code == 409
    assert client.get(key_url.replace('revision=1', 'revision=99')).status_code == 409
    assert client.get(key_url.replace(quote(figures[0]['key'], safe=''), quote('indicator:' + SID, safe=''))).status_code == 404
    manual = client.get('/api/sn/reports/figures/' + quote(figures[1]['key'], safe='') + '?date=2023-01-03&period=weekly&revision=1')
    assert manual.status_code == 200 and manual.json()['series']['caption'] == '保留的人工图说'
    assert all(body.get('catalog_only') or len(body['series_ids']) <= 256 and len(body['series_ids']) * body['points_per_series'] <= 20000 for _, body in state.requests)


def test_draft_quotations_restore_chart_pins_from_exact_versions_without_storing_duplicate_definitions(report_flow):
    from tin.models.workbench import WorkbenchArtifact
    client, state = report_flow
    chart = create(client, title='引文中的固定图表')
    saved = client.post('/fixture/judgment', json={'variety': 'SN', 'author': 'first', 'written_at': '2023-01-02',
        'contradiction': {'bull': {'claim': '引用观点', 'evidence': [{'desc': '引用依据', 'series_id': SID}]}},
        'chart_references': [{'kind': 'chart', 'id': chart['id'], 'revision': 1}]})
    assert saved.status_code == 200, saved.text
    card = entry(source_ids=[])
    card['card'].update(title='引用策略版本', citations=[{'kind': 'chart', 'id': chart['id'], 'revision': 1, 'field': 'trigger'}])
    saved_card = client.post('/api/sn/research-strategies', json=card)
    assert saved_card.status_code == 201, saved_card.text
    draft = {'format': 'research-report-draft.v1', 'period': 'weekly', 'date': '2023-01-03', 'title': '有引文的报告',
        'figures': [{'series_id': SID, 'caption': '重复的原手选'}],
        'references': [{'kind': 'judgment', 'id': str(saved.json()['id']), 'revision': 1, 'section': 'research'},
            {'kind': 'strategy', 'id': 'research:' + saved_card.json()['id'], 'revision': 1, 'section': 'watch'}]}
    created = client.post('/api/sn/report-drafts', json={'draft': draft})
    assert created.status_code == 201, created.text
    assert all(row['chart_references'] for row in created.json()['draft']['reference_snapshots'])
    with artifact_session(state.scope) as session:
        stored = json.loads(session.get(WorkbenchArtifact, created.json()['id']).body)
        assert all('chart_references' not in row for row in stored['reference_snapshots'])
    assert client.patch('/api/sn/charts/' + chart['id'], json={'revision': 1, 'title': '后来的图表修订'}).status_code == 200
    response = client.get('/sn/reports/2023-01-03/weekly.json')
    assert response.status_code == 200, response.text
    figures = response.json()['report_figures']
    assert len(figures) == 1 and figures[0]['reference']['revision'] == 1
    quotations = [ref for ref in figures[0]['references'] if ref.get('report_reference')]
    assert {ref['report_reference']['kind'] for ref in quotations} == {'judgment', 'strategy'}
    assert figures[0]['title'] == '引文中的固定图表'
    result = client.get('/api/sn/reports/figures/' + quote(figures[0]['key'], safe='') + '?date=2023-01-03&period=weekly&revision=1')
    assert result.status_code == 200, result.text
    assert result.json()['rendered']['payload']['revision'] == 1
    assert result.json()['rendered']['definition']['title'] == '引文中的固定图表'


def test_frozen_html_and_markdown_list_titles_and_occurrences_without_chart_reads(loaded, tmp_path, monkeypatch):
    from fastapi.testclient import TestClient
    monkeypatch.setattr(settings, 'weknora_api_url', '')
    monkeypatch.setattr(settings, 'exports_dir', tmp_path)
    monkeypatch.setattr(web, 'SessionLocal', lambda: loaded)
    def forbidden(*args, **kwargs):
        pytest.fail('Frozen report must not re-open chart definitions or data')
    monkeypatch.setattr(chart_references, 'reference_definition', forbidden)
    monkeypatch.setattr(chart_references, 'render_chart_reference', forbidden)
    pin = {'kind': 'chart', 'id': 'frozen-only', 'revision': 7, 'as_of': '2023-01-02',
        'title': '冻结图表标题', 'definition': {'curves': [{'series_id': SID}]}}
    payload = {'variety': web.V, 'as_of': '2026-09-18', 'generated_at': '2026-09-18T16:00:00+08:00',
        'observations': [], 'judgment': {'version': 1, 'status': '草稿', 'contradiction': {'chart_references': [pin]},
            'thresholds': [], 'falsifiers': []}}
    folder = tmp_path / web.V
    folder.mkdir()
    (folder / '2026-09-18.json').write_text(json.dumps(payload, ensure_ascii=False), encoding='utf-8')
    response = TestClient(web.app).get('/sn/reports?date=2026-09-18')
    assert response.status_code == 200, response.text
    html = response.text
    assert '查看当时保存的报告' in html and '冻结图表标题' in html and '引用于：研判' in html
    assert 'data-live="false"' in html and 'data-figure-plot' not in html
    exported = TestClient(web.app).get('/sn/reports/2026-09-18/daily.md')
    assert exported.status_code == 200, exported.text
    assert '### 冻结图表标题' in exported.text and '引用于：研判' in exported.text
    markdown = readable_markdown(payload)
    assert '### 冻结图表标题' in markdown and '引用于：研判' in markdown and '数据截至日：2026-09-18' in markdown

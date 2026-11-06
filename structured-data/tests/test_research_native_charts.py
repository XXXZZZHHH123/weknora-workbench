"""Signed chart reads against small synthetic workbooks and mocked current grants."""
import base64
import hashlib
import hmac
import json
import time
from types import SimpleNamespace
from xml.sax.saxutils import escape
from zipfile import ZipFile

import httpx
import pytest
from fastapi.testclient import TestClient

from structured_data.native_charts import VERSION
from structured_data.server import create_app
from structured_data.store import DataStore

PATH = '/api/research/native-charts'
WB = '10000000-0000-4000-8000-000000000092'
KEY = b'synthetic-native-chart-service-key-123456789'
AUTHORIZATION = 'Bearer synthetic-chart-reader'
NS = 'http://schemas.openxmlformats.org/'


def signed(body, *, path=PATH, legacy=False, changes=None):
    identity = {'user_id': 'reader', 'email': 'reader@example.test', 'display_name': 'Reader',
                'tenant_id': 9, 'role': 'viewer', 'workbench_id': WB,
                'scope_version': 2, 'sources_revision': 7,
                'authorization_sha256': hashlib.sha256(AUTHORIZATION.encode()).hexdigest()}
    if legacy:
        identity.pop('scope_version')
        identity.pop('sources_revision')
        identity.update(knowledge_base_id='library-a', knowledge_base_tenant_id=9)
    identity.update(changes or {})
    raw = json.dumps(body, separators=(',', ':')).encode()
    encoded = base64.urlsafe_b64encode(json.dumps(identity).encode()).decode().rstrip('=')
    stamp = str(int(time.time()))
    proof = '\n'.join((stamp, 'POST', path, encoded, hashlib.sha256(raw).hexdigest())).encode()
    return {'content': raw, 'headers': {'Content-Type': 'application/json', 'Authorization': AUTHORIZATION,
            'X-Research-Identity': encoded, 'X-Research-Timestamp': stamp,
            'X-Research-Signature': hmac.new(KEY, proof, hashlib.sha256).hexdigest()}}


def _series(label, ref, style=''):
    return f'''<c:ser><c:tx><c:v>{escape(label)}</c:v></c:tx>{style}
      <c:cat><c:numRef><c:f>Data!A2:A3</c:f><c:numCache><c:pt idx="0"><c:v>1</c:v></c:pt></c:numCache></c:numRef></c:cat>
      <c:val><c:numRef><c:f>{escape(ref)}</c:f><c:numCache><c:pt idx="0"><c:v>999999</c:v></c:pt></c:numCache></c:numRef></c:val></c:ser>'''


def _chart_xml(title, primary, secondary=''):
    # Axis order deliberately differs from left/right position: the second
    # valAx, even on the left, is the secondary axis in the public protocol.
    return f'''<c:chartSpace xmlns:c="{NS}drawingml/2006/chart" xmlns:a="{NS}drawingml/2006/main">
      <c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>{escape(title)}</a:t></a:r></a:p></c:rich></c:tx></c:title>
      <c:plotArea><c:lineChart>{primary}<c:axId val="10"/><c:axId val="20"/></c:lineChart>
      <c:barChart>{secondary}<c:axId val="10"/><c:axId val="30"/></c:barChart>
      <c:dateAx><c:axId val="10"/><c:axPos val="b"/><c:crossAx val="20"/></c:dateAx>
      <c:valAx><c:axId val="20"/><c:axPos val="r"/><c:title><c:tx><c:rich><a:p><a:r><a:t>kg</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="10"/></c:valAx>
      <c:valAx><c:axId val="30"/><c:axPos val="l"/><c:crossAx val="10"/></c:valAx>
      </c:plotArea></c:chart></c:chartSpace>'''


def workbook(path, suffix):
    office = NS + 'officeDocument/2006/relationships'
    main = NS + 'spreadsheetml/2006/main'

    def relationships(items):
        return '<Relationships xmlns="' + NS + 'package/2006/relationships">' + ''.join(
            f'<Relationship Id="{rid}" Type="{office}/{kind}" Target="{target}"/>'
            for rid, kind, target in items) + '</Relationships>'

    explicit = '<c:spPr><a:ln w="28575"><a:solidFill><a:srgbClr val="c00000"/></a:solidFill><a:prstDash val="lgDash"/></a:ln></c:spPr>'
    themed = '<c:spPr><a:ln w="28808"><a:solidFill><a:schemeClr val="accent1"/></a:solidFill><a:prstDash val="sysDot"/></a:ln></c:spPr>'
    primary = ''.join([
        _series('Literal 2040', 'Data!B2:B3', explicit),
        _series('Pending', 'Pending!B2:B3'),
        _series('External', '[else.xlsx]Data!B2:B3'),
        _series('No values', 'Data!D2:D3'),
        _series('Ambiguous', 'Data!E2:E3'),
        _series('Missing mapping', 'Data!G2:G3'),
    ])
    charts = [
        _chart_xml('Figure ' + suffix, primary, _series('Second axis', 'Data!C2:C3', themed)),
        _chart_xml('Another ' + suffix, _series('Raw label', 'Data!B3:B3')),
        _chart_xml('Third ' + suffix, _series('Raw label', 'Data!B2:B2')),
    ]
    with ZipFile(path, 'w') as archive:
        archive.writestr('xl/workbook.xml', f'<workbook xmlns="{main}" xmlns:r="{office}"><sheets>' + ''.join(
            f'<sheet name="{name}" sheetId="{index}" r:id="s{index}"/>'
            for index, name in enumerate(['Figures', 'Data', 'Pending'], 1)) + '</sheets></workbook>')
        archive.writestr('xl/_rels/workbook.xml.rels', relationships([
            (f's{index}', 'worksheet', f'worksheets/sheet{index}.xml') for index in range(1, 4)]))
        archive.writestr('xl/worksheets/sheet1.xml', f'<worksheet xmlns="{main}" xmlns:r="{office}"><drawing r:id="d1"/></worksheet>')
        archive.writestr('xl/worksheets/_rels/sheet1.xml.rels', relationships([('d1', 'drawing', '../drawings/drawing1.xml')]))
        for index in (2, 3):
            archive.writestr(f'xl/worksheets/sheet{index}.xml', f'''<worksheet xmlns="{main}"><sheetData>
              <row r="2"><c r="A2" t="inlineStr"><is><t>2026-01-01</t></is></c><c r="B2"><v>20</v></c><c r="C2"><v>20</v></c><c r="E2"><v>20</v></c></row>
              <row r="3"><c r="A3" t="inlineStr"><is><t>2026-01-02</t></is></c><c r="B3"><v>30</v></c><c r="C3"><v>30</v></c><c r="E3"><v>30</v></c></row>
              </sheetData></worksheet>''')
        archive.writestr('xl/drawings/drawing1.xml', f'<xdr:wsDr xmlns:xdr="{NS}drawingml/2006/spreadsheetDrawing" xmlns:c="{NS}drawingml/2006/chart" xmlns:r="{office}">' + ''.join(
            f'<xdr:twoCellAnchor><xdr:from><xdr:row>{index}</xdr:row></xdr:from><xdr:graphicFrame><c:chart r:id="c{index}"/></xdr:graphicFrame></xdr:twoCellAnchor>'
            for index in range(1, 4)) + '</xdr:wsDr>')
        archive.writestr('xl/drawings/_rels/drawing1.xml.rels', relationships([
            (f'c{index}', 'chart', f'../charts/chart{index}.xml') for index in range(1, 4)]))
        for index, xml in enumerate(charts, 1):
            archive.writestr(f'xl/charts/chart{index}.xml', xml)


def publish_source(root, tmp_path, suffix, owner):
    original = tmp_path / f'fixture-{suffix}.xlsx'
    workbook(original, suffix)
    namespace = root if owner == 9 else root / 'workspaces' / str(owner)
    store = DataStore(namespace / 'knowledge-bases' / f'library-{suffix}')
    specs = [('quantity', 'Data', 'B', 'kg'), ('amount', 'Data', 'C', 'USD'),
             ('empty', 'Data', 'D', 'kg'), ('ambiguous-one', 'Data', 'E', 'kg'),
             ('ambiguous-two', 'Data', 'E', 'kg'), ('pending', 'Pending', 'B', 'kg')]
    series, observations = [], []
    for identifier, sheet, column, unit in specs:
        series.append({'series_id': identifier, 'sheet': sheet, 'metric': identifier,
                       'unit': unit, 'dimensions': {}, 'header_cells': [column + '1'], 'date_mode': 'date'})
        rows = [2] if identifier == 'ambiguous-one' else [3] if identifier == 'ambiguous-two' else [2, 3]
        for row in rows:
            value = None if identifier == 'empty' else row * 10
            observations.append({'series_id': identifier, 'date': f'2026-01-0{row-1}', 'value': value,
                                 'source_cell': f'{column}{row}', 'date_cell': f'A{row}',
                                 'raw_value': value, 'value_status': 'missing' if value is None else 'valid'})
    parsed = {'source_file': str(original), 'sha256': hashlib.sha256(original.read_bytes()).hexdigest(),
              'rule_version': 'synthetic-charts', 'dataset_kind': 'business',
              'series': series, 'observations': observations, 'sheets': [], 'excluded_regions': [], 'issues': []}
    dataset = store.publish(parsed)['dataset_id']
    digest = hashlib.md5(original.read_bytes()).hexdigest()
    review = {'import_id': ('1' if suffix == 'a' else '2') * 24, 'dataset_id': dataset,
              'dataset_kind': 'business', 'source_file': original.name, 'updated_at': '2026-01-03T00:00:00+00:00',
              'summary': {'confirmed': 1},
              'sheets': [{'name': 'Data', 'status': 'confirmed'}, {'name': 'Pending', 'status': 'needs_review'}],
              'native_source_bindings': [{'source_knowledge_id': f'original-{suffix}', 'file_hash': digest,
                  'knowledge_base_id': f'library-{suffix}', 'tenant_id': owner,
                  'file_name': original.name, 'file_size': original.stat().st_size}]}
    location = store.root / 'imports' / review['import_id'] / 'review.json'
    location.parent.mkdir(parents=True)
    location.write_text(json.dumps(review))
    grant = {'id': f'binding-{suffix}', 'source_type': 'knowledge_base' if suffix == 'a' else 'central_dataset',
             'source_id': f'library-{suffix}', 'knowledge_base_id': f'library-{suffix}', 'tenant_id': owner,
             'permission': 'viewer', 'delegated': False, 'view_modules': ['indicators'],
             'knowledge': [{'id': f'original-{suffix}', 'file_hash': digest, 'file_type': 'xlsx', 'parse_status': 'stored'}]}
    return SimpleNamespace(store=store, review=review, location=location, parsed=parsed, grant=grant)


@pytest.fixture
def research_charts(tmp_path, monkeypatch):
    monkeypatch.setenv('STRUCTURED_TENANT_ID', '9')
    secret = tmp_path / 'key'
    secret.write_bytes(KEY)
    monkeypatch.setenv('STRUCTURED_SERVICE_TOKEN_FILE', str(secret))
    root = tmp_path / 'data'
    first = publish_source(root, tmp_path, 'a', 9)
    second = publish_source(root, tmp_path, 'b', 10)
    state = {'checks': 0, 'revision': 7, 'inventory': True, 'status': 200, 'user_id': 'reader',
             'id': WB, 'tenant': 9, 'sources': [first.grant, second.grant], 'unavailable': [], 'can_read_source': True}

    def upstream(request):
        assert request.method == 'GET'
        assert request.url.path == f'/api/v1/workspaces/research/{WB}/source-scope'
        assert request.headers['Authorization'] == AUTHORIZATION
        proof = '\n'.join(('weknora.research-scope.v1', 'GET', request.url.path,
            request.headers['X-Research-Scope-Timestamp'], hashlib.sha256(AUTHORIZATION.encode()).hexdigest(), '9')).encode()
        assert request.headers['X-Research-Scope-Signature'] == hmac.new(KEY, proof, hashlib.sha256).hexdigest()
        state['checks'] += 1
        if state['checks'] == 2 and state.get('on_recheck'):
            state['on_recheck']()
        return httpx.Response(state['status'], json={'success': state['status'] == 200, 'data': {
            'scope_version': 2, 'sources_revision': state['revision'], 'inventory_included': state['inventory'],
            'id': state['id'], 'user_id': state['user_id'], 'scope_tenant_id': state['tenant'],
            'sources': state['sources'], 'unavailable_sources': state['unavailable'],
            'can_read_source': state['can_read_source']}})

    real_client = httpx.AsyncClient
    monkeypatch.setattr(httpx, 'AsyncClient', lambda **kwargs: real_client(transport=httpx.MockTransport(upstream), **kwargs))
    with TestClient(create_app(root)) as client:
        yield SimpleNamespace(client=client, state=state, first=first, second=second)


def read(fixture, body=None):
    response = fixture.client.post(PATH, **signed({} if body is None else body))
    assert response.status_code == 200, response.text
    return response.json()


def figure(document, knowledge_id='original-a'):
    return next(chart for chart in document['charts']
                if chart['knowledge_id'] == knowledge_id and chart['title'].startswith('Figure '))


@pytest.mark.parametrize('mode', ['unsigned', 'bad_signature', 'wrong_path', 'unsigned_authorization'])
def test_chart_endpoint_requires_path_and_authorization_bound_service_signature(research_charts, mode):
    client = research_charts.client
    request = signed({})
    if mode == 'unsigned':
        request = {'json': {}, 'headers': {'Authorization': AUTHORIZATION}}
    elif mode == 'bad_signature':
        request['headers']['X-Research-Signature'] = '0' * 64
    elif mode == 'wrong_path':
        request = signed({}, path='/api/research/facts')
    else:
        request['headers']['Authorization'] = 'Bearer somebody-else'
    assert client.post(PATH, **request).status_code == 401
    assert research_charts.state['checks'] == 0


@pytest.mark.parametrize('change,expected', [({'user_id': 'other'}, 403), ({'id': 'another-workbench'}, 403),
    ({'tenant': 10}, 403), ({'revision': 8}, 409), ({'inventory': False}, 503), ({'status': 403}, 403)])
def test_current_source_grant_must_match_the_signed_research_identity(research_charts, change, expected):
    research_charts.state.update(change)
    response = research_charts.client.post(PATH, **signed({}))
    assert response.status_code == expected
    assert 'charts' not in response.json()


def test_legacy_research_scope_requires_reopening_workbench(research_charts):
    response = research_charts.client.post(PATH, **signed({}, legacy=True))
    assert response.status_code == 409 and '重新打开研究台' in response.text
    assert research_charts.state['checks'] == 0


@pytest.mark.parametrize('mode', ['delegated', 'unavailable', 'metadata_restricted'])
def test_restricted_workbench_cannot_browse_any_native_charts(research_charts, mode):
    state = research_charts.state
    if mode == 'delegated':
        state['sources'][1]['delegated'] = True
    elif mode == 'unavailable':
        state['unavailable'] = [{'id': 'inaccessible', 'status': 'active'}]
    else:
        state['can_read_source'] = False
    response = research_charts.client.post(PATH, **signed({}))
    assert response.status_code == 403 and 'charts' not in response.json()


def test_response_is_exact_and_preserves_styles_axis_and_unconfirmed_curves(research_charts):
    document = read(research_charts)
    assert set(document) == {'charts', 'total', 'offset', 'next_offset'}
    assert document['total'] == 6 and document['offset'] == 0 and document['next_offset'] is None
    chart = figure(document)
    assert set(chart) == {'chart_key', 'knowledge_id', 'source_file', 'sheet', 'title', 'axes', 'curves'}
    assert chart['source_file'] == 'fixture-a.xlsx' and chart['sheet'] == 'Figures'
    assert [(axis['id'], axis['type'], axis['position']) for axis in chart['axes']] == [
        ('10', 'dateAx', 'b'), ('20', 'valAx', 'r'), ('30', 'valAx', 'l')]
    assert chart['axes'][1]['title'] == 'kg'
    assert all(set(axis) == {'id', 'type', 'title', 'position'} for axis in chart['axes'])
    curves = {curve['label']: curve for curve in chart['curves']}
    assert all(set(curve) == {'label', 'chart_type', 'axis', 'series_id', 'status', 'reason',
                            'first_date', 'last_date', 'unit', 'descriptor', 'style'} for curve in curves.values())
    primary = curves['Literal 2040']
    assert primary['axis'] == 'primary' and primary['chart_type'] == 'lineChart'
    assert primary['style'] == {'color': '#C00000', 'width_pt': 2.25, 'dash': 'dash'}
    assert primary['descriptor'] == {'sheet': 'Data', 'metric': 'quantity', 'column': 'B', 'start_row': 2, 'end_row': 3}
    assert primary['first_date'] == '2026-01-01' and primary['last_date'] == '2026-01-02'
    secondary = curves['Second axis']
    assert secondary['axis'] == 'secondary' and secondary['chart_type'] == 'barChart'
    assert secondary['style'] == {'color': None, 'width_pt': 2.27, 'dash': 'dot'}
    assert curves['Pending']['status'] == 'unconfirmed_sheet' and curves['Pending']['series_id'] is None
    assert curves['Pending']['first_date'] is None and curves['Pending']['unit'] is None
    assert curves['External']['status'] == 'unsupported' and curves['External']['series_id'] is None
    assert curves['No values']['status'] == 'no_data' and curves['No values']['series_id'].startswith('kb-')
    assert curves['Ambiguous']['status'] == 'pending_source' and curves['Ambiguous']['series_id'] is None
    assert '多个指标' in curves['Ambiguous']['reason']
    assert curves['Missing mapping']['status'] == 'pending_source' and curves['Missing mapping']['series_id'] is None
    assert curves['Pending']['style'] == {'color': None, 'width_pt': None, 'dash': None}
    assert '999999' not in json.dumps(document) and 'chart_' not in primary['series_id']
    assert research_charts.state['checks'] == 2


def test_curve_kb_identity_is_identical_to_research_facts_across_real_source_owners(research_charts):
    document = read(research_charts)
    path = '/api/research/facts'
    body = {'source_bindings': ['binding-a', 'binding-b'], 'catalog_only': True}
    response = research_charts.client.post(path, **signed(body, path=path))
    assert response.status_code == 200, response.text
    facts = response.json()['series']
    identifiers = []
    for suffix in ('a', 'b'):
        curve = figure(document, f'original-{suffix}')['curves'][0]
        field = next(field for field in facts if field['knowledge_base_id'] == f'library-{suffix}'
                     and field['source_series_id'] == 'quantity')
        assert curve['series_id'] == field['series_id']
        identity = json.dumps([f'library-{suffix}', f'original-{suffix}', 'quantity'],
                              sort_keys=True, separators=(',', ':'), ensure_ascii=False)
        assert curve['series_id'] == 'kb-' + hashlib.sha256(identity.encode()).hexdigest()[:48]
        identifiers.append(curve['series_id'])
    assert len(set(identifiers)) == 2


@pytest.mark.parametrize('status', ['auto_ready', 'template_ready', 'trusted_existing', 'needs_review'])
def test_scanned_or_withdrawn_source_sheet_never_gets_research_series_identity(research_charts, status):
    first = research_charts.first
    first.review['sheets'][0]['status'] = status
    first.location.write_text(json.dumps(first.review))
    curve = figure(read(research_charts))['curves'][0]
    assert curve['status'] == 'unconfirmed_sheet' and curve['series_id'] is None


def test_pagination_is_stable_across_sources_and_returns_exact_next_offset(research_charts):
    all_charts = read(research_charts)['charts']
    pages = [read(research_charts, {'offset': offset, 'limit': 2}) for offset in (0, 2, 4, 6, 100)]
    assert [page['total'] for page in pages] == [6] * 5
    assert [page['offset'] for page in pages] == [0, 2, 4, 6, 100]
    assert [page['next_offset'] for page in pages] == [2, 4, None, None, None]
    assert [chart for page in pages for chart in page['charts']] == all_charts
    assert len({chart['chart_key'] for chart in all_charts}) == 6
    assert all(len(chart['chart_key']) == 35 and chart['chart_key'].startswith('nc-') for chart in all_charts)
    research_charts.state['sources'].reverse()
    assert read(research_charts)['charts'] == all_charts
    assert len(read(research_charts, {'limit': 1})['charts']) == 1
    assert len(read(research_charts, {'limit': 200})['charts']) == 6


@pytest.mark.parametrize('body', [[], None, {'offset': -1}, {'offset': True}, {'offset': 0.5},
    {'limit': 0}, {'limit': 201}, {'limit': True}, {'limit': '1'}, {'source_bindings': ['binding-a']},
    {'sources': [{'knowledge_id': 'not-approved', 'file_hash': 'a' * 32}]}])
def test_pagination_and_caller_supplied_sources_are_rejected(research_charts, body):
    response = research_charts.client.post(PATH, **signed(body))
    assert response.status_code == 400 and 'charts' not in response.json()


def test_approved_original_hash_must_match_the_actual_snapshot_bytes(research_charts):
    first = research_charts.first
    first.grant['knowledge'][0]['file_hash'] = 'e' * 32
    first.review['native_source_bindings'][0]['file_hash'] = 'e' * 32
    first.location.write_text(json.dumps(first.review))
    response = research_charts.client.post(PATH, **signed({}))
    assert response.status_code == 409 and 'charts' not in response.json()


@pytest.mark.parametrize('field,value', [('permission', 'none'), ('knowledge_base_id', '../outside')])
def test_invalid_source_authorization_is_rejected(research_charts, field, value):
    research_charts.first.grant[field] = value
    response = research_charts.client.post(PATH, **signed({}))
    assert response.status_code == 403 and 'charts' not in response.json()


@pytest.mark.parametrize('change', ['hash', 'knowledge_id', 'deleted', 'removed_binding'])
def test_unapproved_originals_never_expose_old_or_unrelated_charts(research_charts, change):
    source = research_charts.first.grant
    if change == 'removed_binding':
        research_charts.state['sources'] = [research_charts.second.grant]
    elif change == 'hash':
        source['knowledge'][0]['file_hash'] = 'e' * 32
    elif change == 'knowledge_id':
        source['knowledge'][0]['id'] = 'different-original'
    else:
        source['knowledge'][0]['parse_status'] = 'deleting'
    document = read(research_charts)
    assert document['total'] == 3
    assert {chart['knowledge_id'] for chart in document['charts']} == {'original-b'}


@pytest.mark.parametrize('change', ['hash', 'removed_binding', 'permission', 'revision', 'delegated', 'revoked'])
def test_authorization_or_source_change_during_read_returns_409(research_charts, change):
    state = research_charts.state

    def change_grant():
        if change == 'hash':
            research_charts.first.grant['knowledge'][0]['file_hash'] = 'e' * 32
        elif change == 'removed_binding':
            state['sources'] = []
        elif change == 'permission':
            research_charts.first.grant['permission'] = 'editor'
        elif change == 'revision':
            state['revision'] = 8
        elif change == 'delegated':
            research_charts.first.grant['delegated'] = True
        else:
            state['status'] = 403

    state['on_recheck'] = change_grant
    response = research_charts.client.post(PATH, **signed({}))
    assert response.status_code == 409 and 'charts' not in response.json()
    assert state['checks'] == 2


@pytest.mark.parametrize('change', ['review', 'source', 'catalog'])
def test_local_snapshot_or_confirmation_change_during_chart_read_returns_409(research_charts, monkeypatch, change):
    first = research_charts.first
    original = DataStore.chart_index

    def changing_index(store, dataset):
        index = original(store, dataset)
        if store.root == first.store.root:
            if change == 'review':
                first.review['sheets'][0]['status'] = 'needs_review'
                first.location.write_text(json.dumps(first.review))
            elif change == 'source':
                with (store._path(dataset) / 'source.xlsx').open('ab') as stream:
                    stream.write(b'changed during chart read')
            else:
                location = store._path(dataset) / 'series.json'
                location.write_text(location.read_text() + '\n')
        return index

    monkeypatch.setattr(DataStore, 'chart_index', changing_index)
    response = research_charts.client.post(PATH, **signed({}))
    assert response.status_code == 409 and 'charts' not in response.json()


def test_old_chart_index_is_discardable_and_rebuilt_without_changing_published_data(research_charts):
    first = research_charts.first
    folder = first.store._path(first.review['dataset_id'])
    old = folder / 'native-charts-v6-verified-source-dates.json'
    old.write_text(json.dumps({'version': 'native-charts-v6-verified-source-dates', 'charts': [{'title': 'stale'}]}))
    protected = ['manifest.json', 'series.json', 'source.xlsx', 'data.duckdb']
    before = {name: hashlib.sha256((folder / name).read_bytes()).hexdigest() for name in protected}
    chart = figure(read(research_charts))
    assert chart['curves'][0]['style']['color'] == '#C00000'
    rebuilt = json.loads((folder / f'{VERSION}.json').read_text())
    assert rebuilt['version'] == VERSION and rebuilt['chart_count'] == 3
    assert old.is_file()
    assert before == {name: hashlib.sha256((folder / name).read_bytes()).hexdigest() for name in protected}


def test_empty_current_grant_has_no_fallback_to_tenant_or_previous_sources(research_charts, monkeypatch):
    research_charts.state['sources'] = []

    def forbidden(*args, **kwargs):
        raise AssertionError('empty scope opened a dataset')

    monkeypatch.setattr(DataStore, 'chart_index', forbidden)
    assert read(research_charts) == {'charts': [], 'total': 0, 'offset': 0, 'next_offset': None}

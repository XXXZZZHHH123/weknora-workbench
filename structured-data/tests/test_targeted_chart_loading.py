import hashlib
import json
from zipfile import ZipFile

import pytest

from structured_data import native_charts
from structured_data.chart_source_cache import (
    SourceEvidenceReadBudgetExceeded,
    _clear_native_chart_cache,
    _native_chart_cache_info,
    candidate_chart_parts,
    load_native_chart_snapshot,
)
from structured_data.source_inspection import _chart_evidence


MAIN = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
OFFICE = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
DRAWING = 'http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing'
CHART = 'http://schemas.openxmlformats.org/drawingml/2006/chart'
DRAWINGML = 'http://schemas.openxmlformats.org/drawingml/2006/main'
PKG = 'http://schemas.openxmlformats.org/package/2006/relationships'


def _relationships(items):
    def rel_type(target):
        if 'worksheets/' in target:
            return OFFICE + '/worksheet'
        if 'drawings/' in target:
            return OFFICE + '/drawing'
        if 'charts/' in target:
            return OFFICE + '/chart'
        return OFFICE + '/worksheet'

    children = ''.join(
        f'<Relationship Id="{rid}" Type="{types[0] if types else rel_type(target)}" Target="{target}"/>'
        for item in items
        for rid, target, *types in [item]
    )
    return f'<Relationships xmlns="{PKG}">{children}</Relationships>'


def _chart_xml(title, series):
    groups = []
    axes = [
        '<c:catAx><c:axId val="10"/><c:title><c:tx><c:rich><a:p><a:r><a:t>Period</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="20"/></c:catAx>',
        '<c:valAx><c:axId val="20"/><c:title><c:tx><c:rich><a:p><a:r><a:t>USD</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="10"/></c:valAx>',
        '<c:valAx><c:axId val="30"/><c:title><c:tx><c:rich><a:p><a:r><a:t>mm</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="10"/></c:valAx>',
    ]
    for label, value_ref, category_ref, axis_id in series:
        groups.append(
            f'<c:lineChart><c:axId val="10"/><c:axId val="{axis_id}"/>'
            f'<c:ser><c:tx><c:v>{label}</c:v></c:tx>'
            f'<c:cat><c:strRef><c:f>{category_ref}</c:f></c:strRef></c:cat>'
            f'<c:val><c:numRef><c:f>{value_ref}</c:f></c:numRef></c:val></c:ser>'
            '</c:lineChart>'
        )
    return (f'<c:chartSpace xmlns:c="{CHART}" xmlns:a="{DRAWINGML}"><c:chart>'
            f'<c:title><c:tx><c:rich><a:p><a:r><a:t>{title}</a:t></a:r></a:p></c:rich></c:tx></c:title>'
            '<c:plotArea>' + ''.join(groups) + ''.join(axes) +
            '</c:plotArea></c:chart></c:chartSpace>')


def _source(path):
    sheets = (
        '<sheets><sheet name="Dashboard" sheetId="1" r:id="r1"/>'
        '<sheet name="Finance" sheetId="2" r:id="r2"/>'
        '<sheet name="Climate" sheetId="3" r:id="r3"/></sheets>'
    )
    workbook = (f'<workbook xmlns="{MAIN}" xmlns:r="{OFFICE}">'
                f'{sheets}</workbook>')
    worksheet = (f'<worksheet xmlns="{MAIN}" xmlns:r="{OFFICE}">'
                 '<drawing r:id="d1"/></worksheet>')
    drawing = (f'<xdr:wsDr xmlns:xdr="{DRAWING}" xmlns:c="{CHART}" xmlns:r="{OFFICE}">'
               '<xdr:twoCellAnchor><xdr:from><xdr:row>0</xdr:row></xdr:from>'
               '<xdr:graphicFrame><c:chart r:id="c1"/></xdr:graphicFrame></xdr:twoCellAnchor>'
               '<xdr:twoCellAnchor><xdr:from><xdr:row>4</xdr:row></xdr:from>'
               '<xdr:graphicFrame><c:chart r:id="c2"/></xdr:graphicFrame></xdr:twoCellAnchor>'
               '</xdr:wsDr>')
    with ZipFile(path, 'w') as archive:
        archive.writestr('xl/workbook.xml', workbook)
        archive.writestr('xl/_rels/workbook.xml.rels', _relationships([
            ('r1', 'worksheets/sheet1.xml'), ('r2', 'worksheets/sheet2.xml'),
            ('r3', 'worksheets/sheet3.xml'),
        ]))
        archive.writestr('xl/worksheets/sheet1.xml', worksheet)
        archive.writestr('xl/worksheets/sheet2.xml', f'<worksheet xmlns="{MAIN}"/>')
        archive.writestr('xl/worksheets/sheet3.xml', f'<worksheet xmlns="{MAIN}"/>')
        archive.writestr('xl/worksheets/_rels/sheet1.xml.rels', _relationships([
            ('d1', '../drawings/drawing1.xml'),
        ]))
        archive.writestr('xl/drawings/drawing1.xml', drawing)
        archive.writestr('xl/drawings/_rels/drawing1.xml.rels', _relationships([
            ('c1', '../charts/chart1.xml'), ('c2', '../charts/chart2.xml'),
        ]))
        archive.writestr('xl/charts/chart1.xml', _chart_xml('Fresh mixed metrics', [
            ('Net cash', "'Finance'!$B$2:$B$3", "'Finance'!$A$2:$A$3", '20'),
            ('Rainfall', "'Climate'!$C$4:$C$5", "'Climate'!$A$4:$A$5", '30'),
        ]))
        archive.writestr('xl/charts/chart2.xml', _chart_xml('Fresh climate detail', [
            ('Humidity', "'Climate'!$D$4:$D$5", "'Climate'!$A$4:$A$5", '30'),
        ]))
    return path


def _add_unattached_candidate_chart(source, destination):
    with ZipFile(source) as original, ZipFile(destination, 'w') as updated:
        for name in original.namelist():
            data = original.read(name)
            if name == 'xl/worksheets/_rels/sheet1.xml.rels':
                data = data.replace(
                    b'</Relationships>',
                    f'<Relationship Id="d2" Type="{OFFICE}/drawing" Target="../drawings/drawing2.xml"/></Relationships>'.encode())
            updated.writestr(name, data)
        updated.writestr('xl/drawings/drawing2.xml',
            f'<xdr:wsDr xmlns:xdr="{DRAWING}" xmlns:c="{CHART}" xmlns:r="{OFFICE}">'
            '<xdr:twoCellAnchor><xdr:from><xdr:row>0</xdr:row></xdr:from>'
            '<xdr:graphicFrame><c:chart r:id="c3"/></xdr:graphicFrame></xdr:twoCellAnchor>'
            '</xdr:wsDr>')
        updated.writestr('xl/drawings/_rels/drawing2.xml.rels', _relationships([
            ('c3', '../charts/chart3.xml'),
        ]))
        updated.writestr('xl/charts/chart3.xml', _chart_xml('Unattached', [
            ('Net cash', "'Finance'!$B$2:$B$3", "'Finance'!$A$2:$A$3", '20'),
        ]))
    return destination


def _index(path, entries):
    (path.parent / f'{native_charts.VERSION}.json').write_text(json.dumps({
        'version': native_charts.VERSION,
        'dataset_id': path.parent.name,
        'chart_count': len(entries),
        'charts': entries,
    }), encoding='utf-8')


def _candidate(part, value_ref, category_ref, *, label='Forged', host='Unverified host'):
    return {
        'part': part, 'sheet': host, 'title': 'Forged title', 'anchor_row': 999,
        'axis_titles': ['Forged axis'],
        'curves': [{'label': label, 'value_ref': value_ref,
                    'category_ref': category_ref}],
    }


@pytest.fixture(autouse=True)
def reset_cache():
    _clear_native_chart_cache()
    yield
    _clear_native_chart_cache()


def test_candidate_index_facts_are_never_returned_and_live_part_is_reparsed(tmp_path):
    path = _source(tmp_path / 'source.xlsx')
    _index(path, [
        _candidate('xl/charts/chart1.xml', "'Finance'!$B$2:$B$3", "'Finance'!$A$2:$A$3"),
        _candidate('xl/charts/chart404.xml', "'Finance'!$B$2:$B$3", "'Finance'!$A$2:$A$3"),
        _candidate('xl/charts/chart1.xml', 'NamedMeasure', None),
    ])
    source_sha = hashlib.sha256(path.read_bytes()).hexdigest()

    evidence, omitted, scope = _chart_evidence(
        path, 'Finance', [('B2:B3', (2, 2, 2, 3))], 'source.xlsx', source_sha)

    assert not omitted
    assert len(evidence) == 1
    chart = evidence[0]
    assert chart['title'] == 'Fresh mixed metrics'
    assert chart['curve_label'] == 'Net cash'
    assert chart['chart_sheet'] == 'Dashboard'
    assert chart['axis_ids'] == ['10', '20']
    assert [(axis['id'], axis['title']) for axis in chart['axes']] == [
        ('10', 'Period'), ('20', 'USD'), ('30', 'mm')]
    assert scope['complete'] is False
    assert scope['candidate_parts'] == 2
    assert scope['parsed_parts'] == 1
    assert 'candidate_index_source_sha_unbound' in scope['reasons']
    assert 'candidate_index_has_unresolved_references' in scope['reasons']
    assert 'candidate_part_not_in_source' in scope['reasons'] or \
           'candidate_part_not_in_live_relationships' in scope['reasons']


def test_missing_index_returns_explicit_incomplete_scope_not_no_evidence(tmp_path):
    path = _source(tmp_path / 'source.xlsx')
    source_sha = hashlib.sha256(path.read_bytes()).hexdigest()

    evidence, _, scope = _chart_evidence(
        path, 'Finance', [('B2:B3', (2, 2, 2, 3))], 'source.xlsx', source_sha)

    assert evidence == []
    assert scope['complete'] is False
    assert scope['reasons'] == ['candidate_index_missing']


def test_candidate_part_budget_is_a_typed_failure(tmp_path, monkeypatch):
    path = _source(tmp_path / 'source.xlsx')
    _index(path, [
        _candidate('xl/charts/chart1.xml', "'Finance'!B2:B3", "'Finance'!A2:A3"),
        _candidate('xl/charts/chart2.xml', "'Finance'!B2:B3", "'Finance'!A2:A3"),
    ])
    source_sha = hashlib.sha256(path.read_bytes()).hexdigest()
    monkeypatch.setattr('structured_data.chart_source_cache.MAX_CANDIDATE_PARTS', 1)

    with pytest.raises(SourceEvidenceReadBudgetExceeded):
        _chart_evidence(path, 'Finance', [('B2:B3', (2, 2, 2, 3))],
                        'source.xlsx', source_sha)


def test_part_selection_isolated_in_cache_for_same_source_sha(tmp_path):
    path = _source(tmp_path / 'source.xlsx')
    source_sha = hashlib.sha256(path.read_bytes()).hexdigest()

    first = load_native_chart_snapshot(path, source_sha, {'xl/charts/chart1.xml'})
    second = load_native_chart_snapshot(path, source_sha, {'xl/charts/chart2.xml'})

    assert [chart['title'] for chart in first['charts']] == ['Fresh mixed metrics']
    assert [chart['title'] for chart in second['charts']] == ['Fresh climate detail']
    assert _native_chart_cache_info()['entries'] == 2


def test_targeted_resolution_does_not_read_unrelated_worksheet_payloads(tmp_path, monkeypatch):
    path = _source(tmp_path / 'source.xlsx')
    original_read = ZipFile.read
    reads = []

    def guarded_read(archive, name, *args, **kwargs):
        reads.append(name)
        if name in {'xl/worksheets/sheet2.xml', 'xl/worksheets/sheet3.xml'}:
            raise AssertionError(f'unrelated worksheet payload was read: {name}')
        return original_read(archive, name, *args, **kwargs)

    monkeypatch.setattr(ZipFile, 'read', guarded_read)
    charts, reasons = native_charts.extract_parts(path, {'xl/charts/chart1.xml'})

    assert [chart['title'] for chart in charts] == ['Fresh mixed metrics']
    assert reasons == []
    assert 'xl/worksheets/sheet1.xml' in reads
    assert 'xl/worksheets/sheet2.xml' not in reads
    assert 'xl/worksheets/sheet3.xml' not in reads


def test_unattached_drawing_relationship_does_not_bind_candidate_part(tmp_path):
    original = _source(tmp_path / 'source.xlsx')
    path = _add_unattached_candidate_chart(original, tmp_path / 'unattached.xlsx')

    charts, reasons = native_charts.extract_parts(path, {'xl/charts/chart3.xml'})

    assert charts == []
    assert 'candidate_part_not_in_live_relationships' in reasons


@pytest.mark.parametrize(('rel_part', 'rel_id', 'wrong_type'), [
    ('xl/_rels/workbook.xml.rels', 'r1', OFFICE + '/drawing'),
    ('xl/worksheets/_rels/sheet1.xml.rels', 'd1', OFFICE + '/chart'),
    ('xl/drawings/_rels/drawing1.xml.rels', 'c1', OFFICE + '/worksheet'),
])
def test_targeted_relationship_walk_rejects_wrong_relationship_types(
        tmp_path, rel_part, rel_id, wrong_type):
    source = _source(tmp_path / 'valid.xlsx')
    path = tmp_path / 'wrong-type.xlsx'
    with ZipFile(source) as original, ZipFile(path, 'w') as updated:
        for name in original.namelist():
            data = original.read(name)
            if name == rel_part:
                marker = f'Id="{rel_id}" Type="'.encode()
                start = data.index(marker) + len(marker)
                end = data.index(b'"', start)
                data = data[:start] + wrong_type.encode() + data[end:]
            updated.writestr(name, data)

    charts, reasons = native_charts.extract_parts(path, {'xl/charts/chart1.xml'})
    assert charts == []
    assert 'candidate_part_not_in_live_relationships' in reasons


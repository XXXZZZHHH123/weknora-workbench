"""Generic PDF tables: immutable evidence, explicit cross-page plans, scoped SQL."""
from copy import deepcopy
import hashlib
import json
from pathlib import Path

import pytest

from structured_data.pdf_tables import PDFTableStore


def bundle(*tables):
    if not tables:
        tables = ([['Item', 'Value'], ['A', '-1.20'], ['B', '']],)
    return dict(schema_version='pdf_tables_v1', source=dict(sha256='a' * 64, file_name='example.pdf'),
                parser=dict(engine='fixture', version='1'), raw={'unaltered': '<table>raw</table>'},
                formulas=[{'latex': r'\\frac{a}{b}', 'page': 1}], fragments=[
                    dict(id=f'table-{i+1}', page=i+1, bbox=[0.1, 0.1, 0.9, 0.9],
                         caption='Table', footnotes=['Source note'], rows=[
                             [dict(text=text, bbox=[0.1, 0.1, 0.4, 0.2]) for text in row] for row in table])
                    for i, table in enumerate(tables)])


def setup(tmp_path, raw=None, header_rows=None, types=('text', 'decimal')):
    store = PDFTableStore(tmp_path / 'scope')
    draft = store.create_draft(raw or bundle())
    fragments = draft['bundle']['fragments']
    ids = [f['id'] for f in fragments]
    if header_rows is None:
        header_rows = {i: [dict(row=0, reason='Explicit reviewed header')] for i in ids}
    plan = dict(bundle_sha256=draft['bundle_sha256'], fragment_ids=ids, header_rows=header_rows,
                columns=[dict(name=f'col{c}', type=typ, source_columns={i: c for i in ids}) for c, typ in enumerate(types)])
    return store, draft, plan


def publish(store, draft, plan):
    return store.publish(draft['draft_id'], dict(plan, reviewed=True))


def test_raw_round_trip_and_publication_requires_explicit_review(tmp_path):
    raw = bundle()
    store, draft, plan = setup(tmp_path, raw)
    assert draft['bundle'] == raw
    assert draft['status'] == 'draft' and not draft['extraction_verified']
    assert store.create_draft(raw)['draft_id'] == draft['draft_id']
    with pytest.raises(ValueError, match='reviewed:true'):
        store.publish(draft['draft_id'], plan)
    with pytest.raises(ValueError, match='reviewed:true'):
        store.publish(draft['draft_id'], dict(plan, reviewed=1))
    table = publish(store, draft, plan)
    assert table['status'] == 'reviewed' and not table['extraction_verified']
    assert not table['coverage']['source_document_complete']
    assert table['coverage']['source_document_completeness'] == 'unknown'
    assert publish(store, draft, plan)['table_id'] == table['table_id']
    assert store.read_draft(draft['draft_id'])['bundle'] == raw
    assert len(store.list_tables()['tables']) == 1
    assert not store.list_tables('b' * 64)['tables']
    rows = table['rows']
    assert rows[0]['raw_values'] == {'col0': 'A', 'col1': '-1.20'}
    assert rows[1]['values']['col1'] is None
    assert rows[1]['raw_values']['col1'] == ''
    cell = rows[0]['source_cells']['col1'][0]
    assert cell['page'] == 1 and cell['row'] == 1 and cell['column'] == 1
    assert cell['text'] == '-1.20' and cell['source_sha256'] == 'a' * 64


def test_repeat_header_and_headerless_continuation_are_explicit(tmp_path):
    raw = bundle([['Item', 'Value'], ['A', '1']], [['Item', 'Value'], ['B', '2']], [['C', '3']])
    headers = {'table-1': [{'row': 0, 'reason': 'header'}], 'table-2': [{'row': 0, 'reason': 'repeated header reviewed'}], 'table-3': []}
    store, draft, plan = setup(tmp_path, raw, headers)
    table = publish(store, draft, plan)
    assert [r['values']['col0'] for r in table['rows']] == ['A', 'B', 'C']
    assert [r['source_cells']['col0'][0]['page'] for r in table['rows']] == [1, 2, 3]
    assert table['coverage']['covered_data_source_row_count'] == 3
    assert table['coverage']['header_row_count'] == 2
    assert table['coverage']['selected_raw_row_count'] == 5
    bad = deepcopy(plan)
    del bad['header_rows']['table-3']
    with pytest.raises(ValueError, match='header_rows'):
        store.validate_plan(draft['draft_id'], bad)
    bad = deepcopy(plan)
    bad['header_rows']['table-2'] = [0]
    with pytest.raises(ValueError, match='explicit row and reason'):
        store.validate_plan(draft['draft_id'], bad)


def test_identical_layouts_do_not_auto_merge_and_plans_are_versioned(tmp_path):
    raw = bundle([['Item', 'Qty (kg)'], ['A', '3']], [['Item', 'Qty (lb)'], ['B', '7']])
    store, draft, plan = setup(tmp_path, raw)
    first = deepcopy(plan)
    first['fragment_ids'] = ['table-1']
    first['header_rows'] = {'table-1': first['header_rows']['table-1']}
    for col in first['columns']:
        col['source_columns'] = {'table-1': col['source_columns']['table-1']}
    one = publish(store, draft, first)
    assert len(one['rows']) == 1
    assert one['coverage']['selected_fragment_count'] == 1
    two = publish(store, draft, plan)
    assert one['table_id'] != two['table_id']
    assert len(two['rows']) == 2  # Only the explicit plan joins them.
    assert len(store.list_tables()['tables']) == 2


@pytest.mark.parametrize('change', ['unknown_fragment', 'duplicate_fragment', 'wrong_hash', 'drop_column', 'duplicate_column', 'row_bounds', 'column_bounds', 'duplicate_header', 'reserved_column', 'unknown_plan_field'])
def test_plan_identity_coverage_and_bounds(tmp_path, change):
    store, draft, plan = setup(tmp_path)
    if change == 'unknown_fragment':
        plan['fragment_ids'] = ['invented']
    elif change == 'duplicate_fragment':
        plan['fragment_ids'] *= 2
    elif change == 'wrong_hash':
        plan['bundle_sha256'] = 'b' * 64
    elif change == 'drop_column':
        plan['columns'].pop()
    elif change == 'duplicate_column':
        plan['columns'][1]['source_columns']['table-1'] = 0
    elif change == 'row_bounds':
        plan['header_rows']['table-1'][0]['row'] = 999
    elif change == 'column_bounds':
        plan['columns'][0]['source_columns']['table-1'] = -1
    elif change == 'duplicate_header':
        plan['header_rows']['table-1'] *= 2
    elif change == 'reserved_column':
        plan['columns'][0]['name'] = '_row_id'
    else:
        plan['guess_headers'] = True
    with pytest.raises(ValueError):
        store.validate_plan(draft['draft_id'], plan)


def test_exclusion_is_audited_and_never_complete(tmp_path):
    store, draft, plan = setup(tmp_path)
    plan['excluded_rows'] = [dict(fragment_id='table-1', row=2, reason='Reviewed unrelated subtotal')]
    table = publish(store, draft, plan)
    assert not table['coverage']['table_input_complete']
    assert table['coverage']['excluded_row_count'] == 1
    assert table['coverage']['selected_rows_accounted']
    result = store.query(table['table_id'])
    assert not result['input_complete']
    bad = deepcopy(plan)
    bad['excluded_rows'][0]['row'] = 0
    with pytest.raises(ValueError, match='Duplicate source row'):
        store.validate_plan(draft['draft_id'], bad)


def merge_plan(plan, mode='take_upper'):
    plan['boundary_merges'] = [dict(upper=dict(fragment_id='table-1', row=1),
        lower=dict(fragment_id='table-2', row=0), reason='Reviewed split row across page break',
        cells=[dict(column='col0', operation='concat', separator=' '), dict(column='col1', operation=mode)])]
    return plan


def test_reviewed_boundary_merge_retains_both_cell_spans(tmp_path):
    raw = bundle([['Item', 'Value'], ['Long', '-12']], [['label', ''], ['B', '3']])
    store, draft, plan = setup(tmp_path, raw, {'table-1': [dict(row=0, reason='header')], 'table-2': []})
    merge_plan(plan)
    table = publish(store, draft, plan)
    row = table['rows'][0]
    assert row['values'] == dict(col0='Long label', col1='-12')
    assert len(row['source_cells']['col0']) == 2 and len(row['source_cells']['col1']) == 2
    assert [x['page'] for x in row['source_cells']['col1']] == [1, 2]
    assert table['coverage']['covered_data_source_row_count'] == 3
    assert table['coverage']['logical_row_count'] == 2


@pytest.mark.parametrize('lower,types,operation', [
    ('9', ('text', 'decimal'), 'take_upper'),
    ('2', ('text', 'decimal'), 'concat'),
    ('2', ('text', 'text'), 'concat'),
])
def test_boundary_numeric_or_differing_cells_never_guessed(tmp_path, lower, types, operation):
    raw = bundle([['Item', 'Value'], ['Long', '1']], [['label', lower]])
    store, draft, plan = setup(tmp_path, raw, {'table-1': [dict(row=0, reason='header')], 'table-2': []}, types)
    merge_plan(plan, operation)
    plan['boundary_merges'][0]['cells'][1]['separator'] = ''
    with pytest.raises(ValueError, match='numeric|differing'):
        publish(store, draft, plan)


def test_duplicate_boundary_row_and_nonboundary_merge_rejected(tmp_path):
    raw = bundle([['Item', 'Value'], ['Long', '1'], ['Next', '2']], [['label', '']])
    store, draft, plan = setup(tmp_path, raw, {'table-1': [dict(row=0, reason='header')], 'table-2': []})
    merge_plan(plan)
    with pytest.raises(ValueError, match='last/first'):
        publish(store, draft, plan)
    plan['boundary_merges'][0]['upper']['row'] = 2
    plan['boundary_merges'] *= 2
    with pytest.raises(ValueError, match='reused'):
        publish(store, draft, plan)


@pytest.mark.parametrize('text', ['--', 'N/A', '1,000', '3%', '1e30', '0.1234567890123456789'])
def test_numeric_conversion_is_exact_or_rejected_not_missing_zero(tmp_path, text):
    store, draft, plan = setup(tmp_path, bundle([['Item', 'Value'], ['A', text]]))
    with pytest.raises(ValueError, match='Decimal'):
        publish(store, draft, plan)
    plan['columns'][1]['type'] = 'text'
    assert publish(store, draft, plan)['rows'][0]['values']['col1'] == text


def test_scoped_sql_uses_complete_inputs_and_auditable_output(tmp_path):
    store, draft, plan = setup(tmp_path, bundle([['Item', 'Value'], ['A', '-1.25'], ['B', '2.50'], ['C', '']]))
    table = publish(store, draft, plan)
    tid = table['table_id']
    result = store.query(tid, 'SELECT _row_id,col0,col1 FROM data ORDER BY col0', limit=1)
    assert result['input_count'] == 3 and result['input_complete']
    assert result['output_truncated'] and not result['output_complete']
    assert result['rows'][0][2] == '-1.250000000000000000'
    refs = result['row_provenance'][0]
    assert refs['scope'] == 'returned_row_id_references'
    assert refs['source_rows'][0]['source_cells']['col1'][0]['text'] == '-1.25'
    agg = store.query(tid, 'WITH x AS (SELECT * FROM data) SELECT sum(col1) total,count(col1) valid_count,count(*) used_count FROM x', limit=1)
    assert agg['rows'] == [['1.250000000000000000', 2, 3]]
    assert agg['row_provenance'][0]['scope'] == 'all_loaded_inputs_not_sql_lineage'
    assert not agg['provenance']['sql_result_lineage_verified']
    assert agg['source']['sha256'] == 'a' * 64
    assert not agg['extraction_verified']


@pytest.mark.parametrize('sql', [
    'DROP TABLE data', 'UPDATE data SET col1=0', 'SELECT * FROM data; SELECT 2',
    "COPY data TO '/tmp/pdf-test.csv'", "ATTACH '/tmp/other.duckdb' AS other", "SET threads=8",
    'SELECT * FROM other', "SELECT * FROM read_csv('/etc/passwd')",
    'SELECT * FROM duckdb_tables()',
])
def test_sql_is_scoped_read_only(tmp_path, sql):
    store, draft, plan = setup(tmp_path)
    tid = publish(store, draft, plan)['table_id']
    # Catalog functions see only an isolated in-memory database, but direct
    # catalog table access and external relations are rejected as table names.
    if sql == 'SELECT * FROM duckdb_tables()':
        result = store.query(tid, sql)
        assert all('/' not in str(v) for row in result['rows'] for v in row if isinstance(v, str))
    else:
        with pytest.raises(ValueError):
            store.query(tid, sql)
    assert store.query(tid, 'SELECT count(*) FROM data')['rows'] == [[2]]


def test_query_budgets_and_wide_nontimeseries_tables(tmp_path):
    width = 42
    raw = bundle([[f'H{i}' for i in range(width)], [str(i) for i in range(width)]])
    store, draft, plan = setup(tmp_path, raw, types=('text',) * width)
    tid = publish(store, draft, plan)['table_id']
    result = store.query(tid)
    assert len(result['columns']) == width + 1
    for kwargs in ({'limit': 0}, {'limit': True}, {'limit': 501}, {'budget_seconds': 0}, {'budget_seconds': float('nan')}, {'budget_seconds': 9}):
        with pytest.raises(ValueError):
            store.query(tid, **kwargs)
    with pytest.raises(ValueError, match='byte budget'):
        store.query(tid, "SELECT repeat('x',200001) FROM data")
    with pytest.raises(ValueError, match='Scoped PDF SQL failed'):
        store.query(tid, 'SELECT sum(a.i*b.i) FROM range(1000000) a(i), range(1000000) b(i)', budget_seconds=.05)


def test_tampered_draft_and_table_fail_closed(tmp_path):
    store, draft, plan = setup(tmp_path)
    tid = publish(store, draft, plan)['table_id']
    tpath = store.root / 'tables' / (tid + '.json')
    original = tpath.read_text()
    damaged = json.loads(original)
    damaged['rows'][0]['values']['col1'] = '999'
    tpath.write_text(json.dumps(damaged))
    with pytest.raises(ValueError, match='hash mismatch'):
        store.read_table(tid)
    # Even an attacker recomputing the record checksum cannot make it disagree
    # with its bound, immutable raw draft and explicit plan.
    from structured_data.pdf_tables import _hash
    damaged.pop('record_sha256')
    damaged['record_sha256'] = _hash(damaged)
    tpath.write_text(json.dumps(damaged))
    with pytest.raises(ValueError, match='raw source and plan'):
        store.read_table(tid)
    tpath.write_text(original)
    dpath = store.root / 'drafts' / (draft['draft_id'] + '.json')
    damaged = json.loads(dpath.read_text())
    damaged['bundle']['fragments'][0]['rows'][1][1]['text'] = '999'
    dpath.write_text(json.dumps(damaged))
    with pytest.raises(ValueError, match='identity/hash mismatch'):
        store.read_draft(draft['draft_id'])
    with pytest.raises(ValueError, match='identity/hash mismatch'):
        store.query(tid)


@pytest.mark.parametrize('ident', ['../outside', '..', '/tmp/table', 'a/b', 'a\\b', 'x\x00x'])
def test_ids_cannot_escape_caller_scope(tmp_path, ident):
    store, draft, plan = setup(tmp_path)
    with pytest.raises(ValueError):
        store.read_draft(ident)
    with pytest.raises(ValueError):
        store.read_table(ident)


def test_symlinks_and_other_scope_access_are_rejected(tmp_path):
    store, draft, plan = setup(tmp_path)
    tid = publish(store, draft, plan)['table_id']
    other = PDFTableStore(tmp_path / 'other')
    with pytest.raises(ValueError, match='does not exist in this scope'):
        other.read_table(tid)
    target = tmp_path / 'outside.json'
    target.write_text('{}')
    link = store.root / 'tables' / 'pdft_link.json'
    link.symlink_to(target)
    with pytest.raises(ValueError, match='escapes scoped root'):
        store.read_table('pdft_link')


@pytest.mark.parametrize('change', ['coordinate', 'nan', 'duplicate_id', 'cell_type', 'span', 'hash', 'ragged'])
def test_bundle_validation_and_ambiguous_grid_review(tmp_path, change):
    raw = bundle()
    cell = raw['fragments'][0]['rows'][1][0]
    if change == 'coordinate':
        cell['bbox'] = [0, 0, 2, 1]
    elif change == 'nan':
        cell['bbox'] = [0, 0, float('nan'), 1]
    elif change == 'duplicate_id':
        raw['fragments'] *= 2
    elif change == 'cell_type':
        cell['text'] = 123
    elif change == 'span':
        cell['rowspan'] = True
    elif change == 'hash':
        raw['source']['sha256'] = '../not-a-hash'
    else:
        raw['fragments'][0]['rows'][1].pop()
    if change == 'ragged':
        store, draft, plan = setup(tmp_path, raw)
        with pytest.raises(ValueError, match='Ragged'):
            publish(store, draft, plan)
    else:
        with pytest.raises(ValueError):
            setup(tmp_path, raw)


def test_bridge_real_shaped_paddle_html_spans_round_trip_to_scoped_query(tmp_path):
    from structured_data.pdf_parser_bridge import normalize_parser_response
    first_html = ('<table><caption>Measured values</caption>'
                  '<tr><th rowspan="2">Item</th><th colspan="2">Measures</th></tr>'
                  '<tr><th>First</th><th>Second</th></tr>'
                  '<tr><td>A</td><td></td><td>-1.20</td></tr></table>')
    continuation_html = '<table><tr><td>B</td><td>2.50</td><td>-3</td></tr></table>'
    response = {'result': {'layoutParsingResults': [
        {'prunedResult': {'width': 1000, 'height': 2000, 'parsing_res_list': [
            {'block_label': 'table', 'id': 'backend:table-1', 'block_bbox': [100, 200, 900, 800], 'block_content': first_html},
            {'block_label': 'formula', 'block_bbox': [100, 900, 500, 1000], 'block_content': r'\frac{a}{b}'},
        ]}},
        {'prunedResult': {'width': 1000, 'height': 2000, 'parsing_res_list': [
            {'block_label': 'table', 'block_bbox': [100, 100, 900, 400], 'block_content': continuation_html},
        ]}},
    ]}}
    normalized = normalize_parser_response('paddleocr_vl', response,
        {'sha256': 'a' * 64, 'file_name': 'sample.pdf', 'knowledge_id': 'actual-source'})
    assert normalized['parser']['version'] is None
    assert normalized['fragments'][0]['rows'][0][0]['rowspan'] == 2
    assert normalized['fragments'][0]['rows'][0][1]['colspan'] == 2
    assert normalized['fragments'][0]['rows'][0][2] == {'text': ''}
    assert normalized['fragments'][0]['bbox'] == [.1, .1, .9, .4]
    assert normalized['raw']['response_json'] == response
    assert normalized['formulas'][0]['latex'] == r'\frac{a}{b}'
    ids = [f['id'] for f in normalized['fragments']]
    headers = {ids[0]: [dict(row=0, reason='First merged header'), dict(row=1, reason='Second merged header')], ids[1]: []}
    store, draft, plan = setup(tmp_path, normalized, headers, ('text', 'decimal', 'decimal'))
    assert store.read_draft(draft['draft_id'])['bundle'] == normalized
    table = publish(store, draft, plan)
    assert table['coverage']['covered_data_source_row_count'] == 2
    assert table['coverage']['header_row_count'] == 2
    result = store.query(table['table_id'], 'SELECT _row_id,col0,col1,col2 FROM data ORDER BY col0')
    assert result['rows'][0][1:] == ['A', None, '-1.200000000000000000']
    assert result['rows'][1][1:] == ['B', '2.500000000000000000', '-3.000000000000000000']
    assert [r['source_rows'][0]['source_cells']['col0'][0]['page'] for r in result['row_provenance']] == [1, 2]
    assert store.list_drafts()['drafts'][0]['fragment_count'] == 2
    assert not store.list_drafts('b' * 64)['drafts']


def test_bridge_mineru_unknown_page_never_becomes_complete_source(tmp_path):
    from structured_data.pdf_parser_bridge import normalize_parser_response, PDFParserError
    source = {'sha256': 'a' * 64, 'file_name': 'sample.pdf'}
    layout = [
        {'type': 'table', 'page_idx': 0, 'bbox': [100, 100, 900, 500],
         'table_body': '<table><tr><td>Item</td><td>Value</td></tr><tr><td>A</td><td>3</td></tr></table>'},
        {'type': 'table', 'table_body': '<table><tr><td>Unknown merged page</td><td>8</td></tr></table>'},
    ]
    normalized = normalize_parser_response('mineru_v1', {'structured': layout, 'markdown': 'raw merged rendering'}, source)
    assert normalized['requires_review'] and normalized['ambiguities']
    assert len(normalized['fragments']) == 1
    assert normalized['raw']['structured'] == layout
    store, draft, plan = setup(tmp_path, normalized)
    table = publish(store, draft, plan)
    assert table['coverage']['source_document_completeness'] == 'unknown'
    assert not table['coverage']['source_document_complete']
    assert store.read_draft(draft['draft_id'])['bundle']['ambiguities'] == normalized['ambiguities']
    assert not table['coverage']['table_input_complete']
    assert table['coverage']['extraction_ambiguity_count'] == 1
    with pytest.raises(PDFParserError, match='page provenance'):
        normalize_parser_response('mineru_v1', {'structured': layout[1:]}, source)


def test_sql_forged_row_id_is_only_a_reference_not_verified_lineage(tmp_path):
    store, draft, plan = setup(tmp_path)
    table = publish(store, draft, plan)
    real_id = table['rows'][0]['_row_id']
    result = store.query(table['table_id'], f"SELECT '{real_id}' AS _row_id, 999999 AS invented")
    ref = result['row_provenance'][0]
    assert ref['scope'] == 'returned_row_id_references'
    assert not ref['sql_result_lineage_verified']
    assert not result['provenance']['sql_result_lineage_verified']
    assert ref['source_rows'][0]['raw_values']['col1'] == '-1.20'
    result = store.query(table['table_id'], "SELECT 'pdfr_invented' AS _row_id")
    assert not result['row_provenance'][0]['source_rows']
    assert result['row_provenance'][0]['scope'] == 'all_loaded_inputs_not_sql_lineage'


def test_sql_nested_external_and_cross_scope_queries_fail(tmp_path):
    store, draft, plan = setup(tmp_path)
    tid = publish(store, draft, plan)['table_id']
    for sql in (
        "SELECT * FROM query('SELECT * FROM read_csv(''/etc/passwd'')')",
        "WITH stolen AS (SELECT * FROM read_json('/etc/passwd')) SELECT * FROM stolen",
        "SELECT * FROM data JOIN other_scope.data USING (_row_id)",
        "SELECT * FROM query('DELETE FROM data')",
        "SELECT * FROM read_text('/proc/self/environ')",
    ):
        with pytest.raises(ValueError):
            store.query(tid, sql)
    assert store.query(tid, 'SELECT count(*) FROM data')['rows'] == [[2]]


def test_bridge_math_and_multiline_numeric_cells_cannot_silently_become_plain_numbers(tmp_path):
    from structured_data.pdf_parser_bridge import normalize_parser_response
    for index, html_cell in enumerate(('10<sup>2</sup>', '10<sub>2</sub>', '1<br>2')):
        html = '<table><tr><td>Item</td><td>Value</td></tr><tr><td>A</td><td>' + html_cell + '</td></tr></table>'
        response = {'structured': [{'type': 'table', 'page_idx': 0, 'table_body': html}]}
        raw = normalize_parser_response('mineru_v1', response, {'sha256': 'a' * 64, 'file_name': 'formula.pdf'})
        cell = raw['fragments'][0]['rows'][1][1]
        assert cell['text'] not in {'102', '12'}
        store, draft, plan = setup(tmp_path / str(index), raw)
        with pytest.raises(ValueError, match='Decimal conversion'):
            publish(store, draft, plan)
        plan['columns'][1]['type'] = 'text'
        table = publish(store, draft, plan)
        assert table['rows'][0]['values']['col1'] == cell['text']
        assert html in store.read_draft(draft['draft_id'])['bundle']['fragments'][0]['raw_html']


@pytest.mark.parametrize('mode', ['colspan', 'rowspan', 'header_crosses_data'])
def test_data_merged_cells_never_imply_ordinary_numeric_cells(tmp_path, mode):
    raw = bundle([['Label', 'Value'], ['A', '12'], ['B', '3']])
    rows = raw['fragments'][0]['rows']
    if mode == 'colspan':
        rows[1][0] = {'text': '12', 'colspan': 2}
        rows[1][1] = {'text': ''}
    elif mode == 'rowspan':
        rows[1][1] = {'text': '12', 'rowspan': 2}
        rows[2][1] = {'text': ''}
    else:
        rows[0][1] = {'text': 'Value', 'rowspan': 2}
        rows[1][1] = {'text': ''}
    store, draft, plan = setup(tmp_path, raw)
    with pytest.raises(ValueError, match='Merged data cell'):
        publish(store, draft, plan)


@pytest.mark.parametrize('mode', ['out_of_rows', 'out_of_columns', 'overlap', 'covered_value'])
def test_invalid_span_geometry_is_rejected_in_raw_bundle(tmp_path, mode):
    raw = bundle([['Label', 'Value'], ['A', '12'], ['B', '3']])
    rows = raw['fragments'][0]['rows']
    if mode == 'out_of_rows':
        rows[2][0] = {'text': 'x', 'rowspan': 2}
    elif mode == 'out_of_columns':
        rows[0][1] = {'text': 'x', 'colspan': 2}
    elif mode == 'overlap':
        rows[0][0] = {'text': 'x', 'rowspan': 2}
        rows[1][0] = {'text': '', 'colspan': 2}
        rows[1][1] = {'text': ''}
    else:
        rows[0][0] = {'text': 'x', 'colspan': 2}
    with pytest.raises(ValueError, match='span|Covered'):
        setup(tmp_path, raw)


def test_selected_caption_notes_and_explicit_title_survive_list_and_read(tmp_path):
    raw = bundle([['Item', 'Value'], ['A', '3']], [['Item', 'Value'], ['B', '9']])
    raw['fragments'][0].update(caption='Actual table caption', footnotes=['Unit kg; exact note'])
    raw['fragments'][1].update(caption='Different actual caption', footnotes=['Unit lb; exact note'])
    store, draft, plan = setup(tmp_path, raw)
    plan['title'] = 'Explicit reviewed title'
    table = publish(store, draft, plan)
    assert table['title'] == plan['title']
    listed = store.list_tables()['tables'][0]
    assert listed['title'] == plan['title']
    assert [f['page'] for f in listed['fragments']] == [1, 2]
    assert listed['fragments'][0]['caption'] == 'Actual table caption'
    assert listed['fragments'][1]['footnotes'] == ['Unit lb; exact note']
    assert 'rows' not in listed and 'values' not in listed['fragments'][0]

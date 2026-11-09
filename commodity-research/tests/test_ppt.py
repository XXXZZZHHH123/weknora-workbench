"""Structural parsing, bounded reads and byte-preserving OPC assembly."""
from copy import deepcopy
from io import BytesIO
import struct
from zipfile import ZipFile, ZipInfo

from lxml import etree as ET
import pytest

from tin.ppt import emf as emf_module
from tin.ppt.candidates import chart_candidates, template_candidates, verify_pairs
from tin.ppt.emf import extract_text
from tin.ppt.exporter import AFTER_HIGHLIGHT, assemble, highlight_paragraph
from tin.ppt.package import MAX_ENTRIES, MAX_EXPANDED, MAX_UPLOAD, NS, PptError, package, qn, relationships, xml
from tin.ppt.parser import classify, parse_pptx
from tests.ppt_fixtures import GRAPH_TEXTS, PNG, TABLE_TEXTS, archive_bytes, emf, ext_text, make_pptx, plus_text, ppt_entries


def test_emf_both_text_formats_unicode_lengths_and_cap():
    texts = ['文字', '2030-02-01', '-1.25', 'astral 🧪']
    assert extract_text(emf(texts)) == texts
    assert extract_text(emf(texts, plus=True)) == texts
    assert extract_text(ext_text('甲') + plus_text('乙')) == ['甲', '乙']
    assert len(extract_text(emf([str(index) for index in range(240)]))) == 200
    assert extract_text(emf(['ok']), limit=1000) == ['ok']
    assert emf_module.MAX_TEXTS == 200


@pytest.mark.parametrize('data', [
    b'\0' * 7, struct.pack('<II', 84, 0), struct.pack('<II', 84, 1000),
    struct.pack('<II', 70, 12) + b'bad!',
    struct.pack('<III', 70, 28, 10000) + b'EMF+' + b'\0' * 12,
])
def test_truncated_or_invalid_emf_records_do_not_read_out_of_bounds(data):
    assert extract_text(data) == []


def test_text_record_offsets_and_plus_payload_bounds_are_checked():
    data = bytearray(ext_text('do not read header'))
    struct.pack_into('<I', data, 48, 0)
    assert extract_text(bytes(data)) == []
    plus = bytearray(plus_text('valid'))
    struct.pack_into('<I', plus, 24, 28)  # Declared payload excludes the string.
    assert extract_text(bytes(plus)) == []


def test_parse_presentation_order_groups_relations_geometry_and_preserved_types():
    result = parse_pptx(make_pptx())
    assert (result['width_emu'], result['height_emu']) == (12000000, 9000000)
    first, second = result['slides']
    assert [page['part'] for page in result['slides']] == ['ppt/slides/slide7.xml', 'ppt/slides/slide2.xml']
    assert first['title'] == '合成周报 2030 后半段 页码'
    assert first['title_paragraph'] == 1
    assert second['title'] == '另一页 后半段 页码'
    assert len(first['slots']) == 7 and len(second['slots']) == 2
    graph, table, raster, svg, fallback, native, unbound = first['slots']
    assert graph['kind'] == 'chart' and graph['texts'] == GRAPH_TEXTS
    assert graph['shape_id'] == '3' and graph['relationship_id'] == 'rGraph'
    assert graph['media_part'] == 'ppt/media/shared.emf' and graph['group_ids'] == ['2']
    assert graph['position'] == {'x': 100, 'y': 200, 'cx': 300, 'cy': 400}
    assert graph['page_position'] == {'x': 280, 'y': 560, 'cx': 600, 'cy': 800}
    assert table['kind'] == 'table' and table['texts'] == TABLE_TEXTS
    assert table['pairs'][0] == {'date': '2023-01-01', 'value': '10.00', 'text': '10.00', 'decimals': 2}
    assert len(table['pairs']) == 5
    assert raster['kind'] == unbound['kind'] == 'image' and raster['replaceable']
    assert svg['kind'] == fallback['kind'] == native['kind'] == 'image'
    assert [slot['preserve_reason'] for slot in (svg, fallback, native)] == ['svg', 'svg', 'native_chart']
    assert all(not slot['replaceable'] for slot in (svg, fallback, native))
    assert second['slots'][0]['media_part'] == graph['media_part']


def test_title_joins_actual_text_without_xml_pretty_print_tails():
    entries = ppt_entries()
    root = xml(entries['ppt/slides/slide7.xml'])
    entries['ppt/slides/slide7.xml'] = ET.tostring(root, encoding='UTF-8', pretty_print=True)
    result = parse_pptx(archive_bytes(entries))
    assert result['slides'][0]['title'] == '合成周报 2030 后半段 页码'


@pytest.mark.parametrize('texts,kind', [
    ([], 'image'), (['a', 'b', 'c'], 'image'), (['0', '1', '2'], 'chart'),
    (['-2.0%', '0.0%', '+2.0%'], 'chart'), (TABLE_TEXTS, 'table'),
    (['2023-01-01', '2023-01-02', '2023-01-03', '0', '1', '2'], 'chart'),
    (['2023-02-30', '1', '2023-02-31', '2', '2023-1-1', '3'], 'chart'),
    (TABLE_TEXTS + ['unpaired'] * 15, 'chart'),
])
def test_classification_uses_only_structural_date_number_evidence(texts, kind):
    assert classify(texts)[0] == kind


@pytest.mark.parametrize('name', ['/absolute', '../escape', 'ppt/../escape', 'ppt\\evil', 'C:/evil',
                                  'ppt//evil', 'ppt/folder//', 'ppt/./evil', 'ppt/%2e%2e/evil', 'ppt/line\nbreak'])
def test_zip_abnormal_entry_names_rejected(name):
    entries = ppt_entries()
    entries[name] = b'bad'
    with pytest.raises(PptError, match='异常'):
        parse_pptx(archive_bytes(entries))


def test_zip_requires_presentation_and_rejects_nonzip_duplicate_symlink_and_encryption():
    with pytest.raises(PptError):
        parse_pptx(b'not a zip')
    entries = ppt_entries()
    del entries['ppt/presentation.xml']
    with pytest.raises(PptError):
        parse_pptx(archive_bytes(entries))
    buffer = BytesIO(make_pptx())
    with pytest.warns(UserWarning, match='Duplicate'):
        with ZipFile(buffer, 'a') as archive:
            archive.writestr('ppt/presentation.xml', b'<bad/>')
    with pytest.raises(PptError, match='重复'):
        parse_pptx(buffer.getvalue())
    for encrypted in (False, True):
        buffer = BytesIO(make_pptx())
        if not encrypted:
            with ZipFile(buffer, 'a') as archive:
                link = ZipInfo('ppt/media/link')
                link.create_system = 3
                link.external_attr = 0o120777 << 16
                archive.writestr(link, b'/tmp/target')
            data = buffer.getvalue()
        else:
            data = bytearray(buffer.getvalue())
            offset = data.index(b'PK\x01\x02')
            struct.pack_into('<H', data, offset + 8, 1)
            data = bytes(data)
        with pytest.raises(PptError, match='异常'):
            parse_pptx(data)


def test_null_byte_in_raw_zip_name_is_rejected_instead_of_truncated():
    entries = ppt_entries()
    entries['ppt/nullXevil'] = b'bad'
    data = archive_bytes(entries).replace(b'ppt/nullXevil', b'ppt/null\x00evil')
    with pytest.raises(PptError, match='异常'):
        parse_pptx(data)


def test_declared_expansion_and_entry_limits_rejected_before_reading():
    assert (MAX_UPLOAD, MAX_EXPANDED, MAX_ENTRIES) == (60 * 1024**2, 500 * 1024**2, 5000)
    data = bytearray(make_pptx())
    offset = data.index(b'PK\x01\x02')
    struct.pack_into('<I', data, offset + 24, MAX_EXPANDED + 1)
    with pytest.raises(PptError) as error:
        parse_pptx(bytes(data))
    assert error.value.status == 413
    entries = ppt_entries()
    entries.update({f'empty/{index}': b'' for index in range(5001)})
    with pytest.raises(PptError) as error:
        parse_pptx(archive_bytes(entries))
    assert error.value.status == 413


@pytest.mark.parametrize('encoding', ['utf-8', 'utf-16'])
@pytest.mark.parametrize('declaration', [
    '<!DOCTYPE root [<!ENTITY xxe SYSTEM "file:///not-accessed-by-ppt-tests">]>',
    '<!DOCTYPE root SYSTEM "https://not-accessed.invalid/entity">',
    '<!DOCTYPE root [<!ENTITY a "expanded"><!ENTITY b "&a;&a;">]>',
])
def test_xml_dtd_and_entities_rejected_even_in_unreferenced_parts(encoding, declaration):
    entries = ppt_entries()
    entries['custom/unused.xml'] = (f'<?xml version="1.0" encoding="{encoding}"?>' + declaration + '<root/>').encode(encoding)
    with pytest.raises(PptError, match='DTD|XML'):
        parse_pptx(archive_bytes(entries))


def test_bad_internal_relationship_cannot_escape_zip_and_external_target_is_not_read():
    entries = ppt_entries()
    entries['ppt/slides/_rels/slide7.xml.rels'] = entries['ppt/slides/_rels/slide7.xml.rels'].replace(
        b'../media/shared.emf', b'../../../outside.emf')
    with pytest.raises(PptError, match='范围'):
        parse_pptx(archive_bytes(entries))
    entries = ppt_entries()
    entries['ppt/slides/_rels/slide7.xml.rels'] = entries['ppt/slides/_rels/slide7.xml.rels'].replace(
        b'Id="rGraph"', b'TargetMode="External" Id="rGraph"')
    graph = parse_pptx(archive_bytes(entries))['slides'][0]['slots'][0]
    assert graph['media_part'] is None and graph['preserve_reason'] == 'missing_media'


@pytest.mark.parametrize('matched,verified', [(5, True), (4, True), (3, False), (0, False)])
def test_numerical_verification_rounding_and_eighty_percent_threshold(matched, verified):
    pairs = [{'date': f'2023-01-0{index + 1}', 'value': value, 'decimals': places}
             for index, (value, places) in enumerate([('1.24', 2), ('-1.3', 1), ('3', 0), ('0.00', 2), ('1000.0', 1)])]
    actual = [1.235, -1.25, 2.5, 0.004, 1000.04]
    points = [{'date': pair['date'], 'value': actual[index] if index < matched else 9876} for index, pair in enumerate(pairs)]
    result = verify_pairs(pairs, points)
    assert result == {'matched': matched, 'total': 5, 'score': matched / 5, 'numerically_verified': verified}
    assert verify_pairs(pairs, points[:3])['numerically_verified'] is False
    assert verify_pairs([], [])['numerically_verified'] is False
    assert verify_pairs(pairs, [{'date': pair['date'], 'value': None} for pair in pairs])['matched'] == 0


@pytest.mark.parametrize('field', ['title', 'labels', 'segment_labels', 'units'])
def test_chart_recall_uses_all_requested_fields_and_returns_scored_top_three(field):
    charts = []
    for index in range(5):
        chart = {'id': str(index), 'title': '', 'labels': [], 'segment_labels': [], 'units': []}
        chart[field] = '唯一召回证据' if field == 'title' else ['唯一召回证据']
        charts.append(chart)
    result = chart_candidates(['唯一召回证据'], charts)
    assert len(result) == 3 and all(0 < value['score'] <= 1 for value in result)
    assert chart_candidates([], charts) == []


def test_table_candidates_are_only_from_charts_on_the_same_page_and_never_bind():
    presentation = parse_pptx(make_pptx())
    first, second = presentation['slides']
    # Move the table to the second page, which has no recalled text evidence.
    table = deepcopy(first['slots'][1])
    table['slot_id'] = 'moved-table'
    second['slots'] = [table]
    charts = [{'id': 'eligible', 'title': '隔离图表', 'labels': [], 'segment_labels': [], 'units': [], 'series_ids': ['s1']},
              {'id': 'elsewhere', 'title': 'different', 'labels': [], 'segment_labels': [], 'units': [], 'series_ids': ['s2']}]
    fields = {'s1': {'metric': 'one'}, 's2': {'metric': 'two'}}
    bindings, requests = {}, []

    def reader(identifiers, day, *, limit):
        requests.append((identifiers, day, limit))
        return {sid: {'points': [{'date': pair['date'], 'value': float(pair['value'])}
                                for pair in table['pairs']], 'limited': False} for sid in identifiers}

    result = template_candidates(presentation, bindings, charts, fields, reader)
    assert result['moved-table'] == []
    assert all(ids == ['s1'] for ids, _, _ in requests)
    assert all(limit == 2000 for _, _, limit in requests)
    assert bindings == {}
    # An explicit chart binding on this page supplies its own series evidence.
    bindings['moved-table'] = {'kind': 'chart', 'chart_id': 'elsewhere'}
    result = template_candidates(presentation, bindings, charts, fields, reader)
    assert [candidate['series_id'] for candidate in result['moved-table']] == ['s2']
    assert result['moved-table'][0]['label'] == '数值已核实'
    assert bindings['moved-table']['kind'] == 'chart'


def test_export_only_changes_selected_parts_relations_types_and_title_styles(tmp_path):
    original = make_pptx()
    parsed = parse_pptx(original)
    png = tmp_path / 'render.png'
    png.write_bytes(PNG)
    output = tmp_path / 'out.pptx'
    titles = assemble(original, output, parsed, {'slide-1-pic-1': png, 'slide-1-pic-3': png}, mark_headlines=True)
    assert [value['slide'] for value in titles] == [1]
    changed = {'ppt/slides/slide7.xml', 'ppt/slides/_rels/slide7.xml.rels', '[Content_Types].xml'}
    with ZipFile(BytesIO(original)) as before, ZipFile(output) as after:
        assert after.testzip() is None and before.comment == after.comment
        for name in before.namelist():
            if name not in changed:
                assert before.read(name) == after.read(name), name
        assert all(before.read(name) != after.read(name) for name in changed)
        added = set(after.namelist()) - set(before.namelist())
        assert len(added) == 2 and all(part.startswith('ppt/media/') and part.endswith('.png') for part in added)
        assert all(after.read(name) == PNG for name in added)
        old_rel = relationships(before, 'ppt/slides/slide7.xml')
        new_rel = relationships(after, 'ppt/slides/slide7.xml')
        assert len(new_rel) == len(old_rel) + 2
        assert all(new_rel[key] == value for key, value in old_rel.items())
        old = xml(before.read('ppt/slides/slide7.xml'))
        new = xml(after.read('ppt/slides/slide7.xml'))
        assert [node.text for node in old.iter(qn('a', 't'))] == [node.text for node in new.iter(qn('a', 't'))]
        old_pics, new_pics = list(old.iter(qn('p', 'pic'))), list(new.iter(qn('p', 'pic')))
        for index, (previous, current) in enumerate(zip(old_pics, new_pics)):
            assert ET.tostring(previous.find('./p:spPr', NS)) == ET.tostring(current.find('./p:spPr', NS))
            assert ET.tostring(previous.find('./p:blipFill/a:srcRect', NS)) == ET.tostring(current.find('./p:blipFill/a:srcRect', NS))
            rid = current.find('./p:blipFill/a:blip', NS).get(qn('r', 'embed'))
            if index in {0, 2}:
                assert rid not in old_rel and new_rel[rid]['target'] in added
            else:
                assert rid == previous.find('./p:blipFill/a:blip', NS).get(qn('r', 'embed'))
        # Shared EMF and PNG, the entire other page, c:chart and both SVG forms survive.
        for part in ('ppt/media/shared.emf', 'ppt/media/shared.png', 'ppt/media/vector.svg',
                     'ppt/charts/chart1.xml', 'ppt/slides/slide2.xml'):
            assert before.read(part) == after.read(part)
        types = xml(after.read('[Content_Types].xml'))
        png_type = types.find("ct:Default[@Extension='png']", NS)
        assert png_type is not None and png_type.get('ContentType') == 'image/png'
        assert list(types).index(png_type) < next(index for index, child in enumerate(types) if child.tag == qn('ct', 'Override'))
        paras = list(new.iter(qn('a', 'p')))
        for run in paras[1]:
            assert run.find('./a:rPr/a:highlight/a:srgbClr', NS).get('val') == 'FFFF00'
        tags = [ET.QName(child).localname for child in paras[1][0].find('a:rPr', NS)]
        assert tags[:4] == ['ln', 'solidFill', 'effectLst', 'highlight']
        assert not paras[2].findall('.//a:highlight', NS)


def test_export_with_no_changes_copies_all_bytes_and_existing_png_type_is_untouched(tmp_path):
    original = make_pptx(png_default=True)
    parsed = parse_pptx(original)
    output = tmp_path / 'copy.pptx'
    assemble(original, output, parsed, {})
    with ZipFile(BytesIO(original)) as before, ZipFile(output) as after:
        assert before.namelist() == after.namelist()
        assert all(before.read(part) == after.read(part) for part in before.namelist())
    png = tmp_path / 'input.png'
    png.write_bytes(PNG)
    assemble(original, output, parsed, {'slide-1-pic-1': png})
    with ZipFile(BytesIO(original)) as before, ZipFile(output) as after:
        assert before.read('[Content_Types].xml') == after.read('[Content_Types].xml')
    assert parse_pptx(output)['slides'][0]['slots'][0]['kind'] == 'image'


@pytest.mark.parametrize('successor', sorted(AFTER_HIGHLIGHT))
def test_highlight_is_before_every_schema_successor_and_after_fill_effect(successor):
    para = ET.Element(qn('a', 'p'))
    run = ET.SubElement(para, qn('a', 'r'))
    props = ET.SubElement(run, qn('a', 'rPr'))
    for name in ('ln', 'solidFill', 'effectDag'):
        ET.SubElement(props, qn('a', name))
    ET.SubElement(props, successor)
    # A previous highlight is normalized, rather than duplicated.
    ET.SubElement(props, qn('a', 'highlight'))
    ET.SubElement(run, qn('a', 't')).text = '2030'
    highlight_paragraph(para)
    assert len(props.findall(qn('a', 'highlight'))) == 1
    assert props[3].tag == qn('a', 'highlight') and props[4].tag == successor


@pytest.mark.parametrize('slot', ['absent', 'slide-1-pic-4', 'slide-1-chart-1'])
def test_assembler_rejects_unknown_or_unsupported_replacements(tmp_path, slot):
    original = make_pptx()
    png = tmp_path / 'input.png'
    png.write_bytes(PNG)
    with pytest.raises(PptError):
        assemble(original, tmp_path / 'out.pptx', parse_pptx(original), {slot: png})

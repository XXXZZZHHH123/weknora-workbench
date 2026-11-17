"""Optional dimension labels preserve the saved research layout and explicit IDs."""
from copy import deepcopy
from datetime import date, datetime, timezone

from lxml.html import fromstring

from tests.test_judgment_dimension_schema import legacy_fields
from tests.test_judgment_research_view import facts, saved, rule, A, DAY
from tests.test_judgment_research_template import render
from tin.export.board import judgment_record_block, threshold_radar_values
from tin.judgments.markdown import dump_markdown
from tin.judgments.markdown_protocol import exported_rules
from tin.models import SourceMapping
from tin.schemas.judgment import JudgmentIn
from tin.web.judgment_research import judgment_research_view
from tin.web.report_view import present_snapshot, readable_markdown
from tin.web.research_dimensions import DIMENSIONS_KEY
from tin.web.restricted_views import mapping_key


CUSTOM = [{'id': 'desk_cost', 'label': '成本与利润', 'category': '供给', 'description': 'Synthetic desk definition'}]


def configure(session, dimensions):
    session.info['workbench_id'] = '10000000-0000-0000-0000-000000000001'
    session.add(SourceMapping(id=mapping_key(session, DIMENSIONS_KEY),
        config={'dimensions': dimensions}, confirmed_by='synthetic', confirmed_at=datetime.now(timezone.utc)))
    session.commit()


def test_optional_dimension_tags_keep_evidence_in_its_original_side_and_order(facts):
    row = saved(bull=[{'desc': 'No tag'}, {'desc': 'Higher explicit price', 'dimension': 'price'}],
                bear=[{'desc': 'Lower explicit price', 'dimension': 'price'},
                      {'desc': 'Deleted assignment', 'dimension': 'obsolete'}])
    view = judgment_research_view(facts, row, DAY)
    bull, bear = view['sides']['bull']['evidence'], view['sides']['bear']['evidence']
    assert [item['desc'] for item in bull] == ['No tag', 'Higher explicit price']
    assert not bull[0].get('dimension_label')
    assert bull[1]['dimension_label'] == bear[0]['dimension_label'] == '价格与价差'
    assert bear[1]['dimension_label'] == '已删除维度'
    assert 'dimension_groups' not in view
    for restricted in (False, True):
        document = fromstring(render(restricted_view=restricted, **view))
        assert not document.xpath('//*[@data-dimension-group]')
        assert not document.xpath('//th[text()="维度"]')
        assert '未标注' not in document.text_content()
        assert len(document.xpath('//*[@id="judgment-hypotheses"]/*[contains(@class,"judgment-hypotheses")]/article')) == 2
        for side, descriptions, labels in (
                ('bull', ['No tag', 'Higher explicit price'], [[], ['价格与价差']]),
                ('bear', ['Lower explicit price', 'Deleted assignment'], [['价格与价差'], ['已删除维度']])):
            section = document.xpath(f'//*[@id="judgment-hypotheses"]//article[@data-hypothesis="{side}"]')[0]
            cards = section.xpath('.//li[@data-research-item="evidence"]')
            assert len(cards) == len(descriptions)
            for index, (card, description, tag_labels) in enumerate(zip(cards, descriptions, labels)):
                desc = card.xpath('./p[contains(@class,"judgment-evidence-desc")]')[0]
                assert desc.text.strip() == description
                tags = desc.xpath('./span[@data-dimension-label]')
                assert [tag.text_content() for tag in tags] == tag_labels
                assert all('judgment-dimension-tag' in tag.get('class', '').split() for tag in tags)
                if not restricted:
                    assert card.xpath(f'.//*[@data-judgment-library-target="evidence:{side}:{index}"]')
            # Preserve the explanatory titles restored by the CI fix.
            label = '多方' if side == 'bull' else '空方'
            assert section.xpath('./h4[contains(@class,"judgment-falsifier-section")]')[0].text_content() == f'什么变化会证伪{label}判断'
            assert '尚无已核验的证伪条件' in section.text_content()
        if restricted:
            assert not document.xpath('//*[@data-judgment-library-target]')


def test_untagged_judgment_adds_no_labels_or_dimension_layout(facts):
    row = saved(bull=[{'desc': 'No tag'}], bear=[{'desc': 'Still no tag'}],
                falsifiers=[rule('support', A, 'bull')])
    view = judgment_research_view(facts, row, DAY)
    for restricted in (False, True):
        document = fromstring(render(restricted_view=restricted, **view))
        assert not document.xpath('//*[@data-dimension-label or @data-dimension-group]')
        assert not document.xpath('//th[text()="维度"]')
        assert '未标注' not in document.text_content()


def test_custom_dimensions_and_renames_use_current_labels_without_reassigning_ids(facts):
    configure(facts, CUSTOM)
    row = saved(bull=[{'desc': 'Cost change', 'dimension': 'desk_cost'},
                      {'desc': 'No silent remap', 'dimension': 'supply'}])
    first = judgment_research_view(facts, row, DAY)
    assert [item['dimension_label'] for item in first['sides']['bull']['evidence']] == ['成本与利润', '已删除维度']
    config = facts.get(SourceMapping, mapping_key(facts, DIMENSIONS_KEY))
    config.config = {'dimensions': [{**CUSTOM[0], 'label': '成本变化'}]}
    facts.commit()
    assert judgment_research_view(facts, row, DAY)['sides']['bull']['evidence'][0]['dimension_label'] == '成本变化'
    assert row.contradiction['bull']['evidence'][0]['dimension'] == 'desk_cost'


def test_falsifier_tags_stay_beside_condition_names_under_original_hypotheses(facts):
    row = saved(falsifiers=[{**rule('support', A, 'bull'), 'dimension': 'price'},
                            rule('opposition', A, 'bear')])
    view = judgment_research_view(facts, row, DAY)
    for restricted in (False, True):
        document = fromstring(render(restricted_view=restricted, **view))
        for side, own, other, labels in (('bull', 'support', 'opposition', ['价格与价差']),
                                          ('bear', 'opposition', 'support', [])):
            section = document.xpath(f'//*[@id="judgment-hypotheses"]//article[@data-hypothesis="{side}"]')[0]
            cards = section.xpath('./div[@data-research-item="falsifier"]')
            assert len(cards) == 1
            heading = cards[0].xpath('./div[contains(@class,"judgment-falsifier-heading")]')[0]
            assert 'Original falsification condition ' + own in heading.text_content()
            assert 'Original falsification condition ' + other not in heading.text_content()
            assert [tag.text_content() for tag in heading.xpath('.//*[@data-dimension-label]')] == labels
        assert not document.xpath('//*[@id="judgment-falsifiers"]')
        assert '未标注' not in document.text_content()


def test_shared_comparison_keeps_duplicate_text_labels_with_their_own_evidence(facts):
    for tagged in (True, False):
        bull = [{'desc': 'Same interpretation', 'series_id': A, 'dimension': 'price'},
                {'desc': 'Same interpretation', 'series_id': A, 'dimension': 'inventory'},
                {'desc': 'Same interpretation', 'series_id': A}]
        bear = [{'desc': 'Other interpretation', 'series_id': A, 'dimension': 'obsolete'},
                {'desc': 'Other interpretation', 'series_id': A}]
        falsifiers = [{**rule('support', A, 'bull'), 'dimension': 'price'}, rule('opposition', A, 'bear')]
        if not tagged:
            for item in bull + bear + falsifiers:
                item.pop('dimension', None)
        view = judgment_research_view(facts, saved(bull=bull, bear=bear, falsifiers=falsifiers), DAY)
        assert len(view['shared']) == 1
        for restricted in (False, True):
            document = fromstring(render(restricted_view=restricted, **view))
            shared = document.xpath('//*[@data-shared-indicator]')[0]
            for side, description, expected in (
                    ('bull', 'Same interpretation', [['价格与价差'], ['库存'], []]),
                    ('bear', 'Other interpretation', [['已删除维度'], []])):
                section = shared.xpath(f'.//*[@data-hypothesis="{side}"]')[0]
                paragraphs = section.xpath('./p')
                evidence = paragraphs[:-1]
                assert [item.text.strip() for item in evidence] == [description] * len(expected)
                assert [[tag.text_content() for tag in item.xpath('./span[@data-dimension-label]')]
                        for item in evidence] == (expected if tagged else [[] for _ in expected])
                falsifier = paragraphs[-1]
                assert falsifier.text.startswith('证伪：Original falsification condition ')
                assert [tag.text_content() for tag in falsifier.xpath('./span[@data-dimension-label]')] == (
                    ['价格与价差'] if tagged and side == 'bull' else [])
            if not tagged:
                assert not shared.xpath('.//*[@data-dimension-label]')
            assert '未标注' not in shared.text_content()


def test_threshold_tags_stay_in_name_cells_without_adding_a_column(facts):
    row = saved(bull=[{'desc': 'Threshold observation', 'series_id': A}])
    row.thresholds = [
        {'id': 'tagged', 'name': 'Tagged threshold', 'series_id': A, 'operator': '>', 'value': 10,
         'type': '观察位', 'horizon': '短期', 'dimension': 'price'},
        {'id': 'plain', 'name': 'Plain threshold', 'series_id': A, 'operator': '<', 'value': 2,
         'type': '风控线', 'horizon': '中长期'}]
    view = judgment_research_view(facts, row, DAY)
    assert view['threshold_dimensions'] == {'tagged': '价格与价差'}
    radar = threshold_radar_values(row, {})
    for restricted in (False, True):
        document = fromstring(render(restricted_view=restricted, radar=radar, **view))
        table = document.xpath('//*[@id="judgment-thresholds"]//table')[0]
        assert len(table.xpath('./thead/tr/th')) == 7
        assert not table.xpath('.//th[text()="维度"]')
        rows = table.xpath('./tbody/tr[not(@data-rule-chart-row)]')
        for cells, name, labels in ((rows[0].xpath('./td'), 'Tagged threshold', ['价格与价差']),
                                    (rows[1].xpath('./td'), 'Plain threshold', [])):
            assert len(cells) == 7
            assert cells[0].text.strip() == name
            assert [tag.text_content() for tag in cells[0].xpath('.//*[@data-dimension-label]')] == labels
        assert all(cell.get('colspan') == '7' for cell in table.xpath('./tbody/tr[@data-rule-chart-row]/td'))
        evidence = document.xpath('//*[@id="judgment-hypotheses"]//li[@data-research-item="evidence"]')[0]
        conditions = evidence.xpath('.//*[@data-item-threshold]/span[1]')
        assert len(conditions) == 2
        for condition, name, labels in ((conditions[0], 'Tagged threshold', ['价格与价差']),
                                        (conditions[1], 'Plain threshold', [])):
            assert condition.xpath('./strong')[0].text_content() == name
            assert [tag.text_content() for tag in condition.xpath('./span[@data-dimension-label]')] == labels
        assert '未标注' not in document.text_content()


def test_report_html_markdown_and_snapshot_keep_tag_ids_and_names_without_mutating():
    raw = legacy_fields()
    raw['contradiction']['bull']['evidence'][0]['dimension'] = 'desk_cost'
    raw['contradiction']['bear']['evidence'][0]['dimension'] = 'deleted_id'
    raw['thresholds'][0]['dimension'] = 'desk_cost'
    raw['falsifiers'][0]['dimension'] = 'desk_cost'
    payload = JudgmentIn(**raw)
    from types import SimpleNamespace
    row = SimpleNamespace(**payload.model_dump(mode='json'), id=4, version=2, status='草稿', review_due=date(2030, 2, 2))
    row.written_at = payload.written_at
    block = judgment_record_block(row, date(2030, 1, 2))
    assert block['contradiction']['bull']['evidence'][0]['dimension'] == 'desk_cost'
    assert block['thresholds'][0]['dimension'] == block['falsifiers'][0]['dimension'] == 'desk_cost'
    source = {'as_of': '2030-01-02', 'judgment': block, 'research_dimensions': CUSTOM,
              'signals': [], 'observations': []}
    before = deepcopy(source)
    view = present_snapshot(source)
    assert view['judgment']['cases'][0]['evidence'][0]['dimension'] == 'desk_cost'
    assert view['judgment']['cases'][0]['evidence'][0]['dimension_label'] == '成本与利润'
    assert view['judgment']['cases'][1]['evidence'][0]['dimension_label'] == '已删除维度'
    from tin.web import app as web
    html = fromstring(web.templates.env.get_template('_report_judgment.html').render(report=view, period_kind='daily'))
    assert html.xpath('//*[@data-dimension-label and text()="成本与利润"]')
    assert html.xpath('//*[@data-dimension-label and text()="已删除维度"]')
    assert len(html.xpath('//th[text()="维度"]')) == 2
    text = readable_markdown(source)
    assert '[成本与利润] Synthetic evidence' in text and '[已删除维度] Other evidence' in text
    assert '[成本与利润] Synthetic limit' in text and '[成本与利润] Synthetic falsifier' in text
    assert source == before
    current = [{**CUSTOM[0], 'label': '新名称'}]
    assert present_snapshot(source, dimensions=current)['judgment']['cases'][0]['evidence'][0]['dimension_label'] == '新名称'
    export = dump_markdown(payload, dimensions=CUSTOM)
    assert '成本与利润（`desk_cost`）' in export and '已删除维度（`deleted_id`）' in export
    assert exported_rules(export) == exported_rules(dump_markdown(JudgmentIn(**legacy_fields())))

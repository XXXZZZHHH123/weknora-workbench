"""Dimension names and evidence groups come only from explicit IDs and desk config."""
from copy import deepcopy
from datetime import date, datetime, timezone

from lxml.html import fromstring

from tests.test_judgment_dimension_schema import legacy_fields
from tests.test_judgment_research_view import facts, saved, DAY
from tests.test_judgment_research_template import render
from tin.export.board import judgment_record_block
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


def test_default_groups_include_support_and_opposition_and_put_untagged_last(facts):
    row = saved(bull=[{'desc': 'No tag'}, {'desc': 'Higher explicit price', 'dimension': 'price'}],
                bear=[{'desc': 'Lower explicit price', 'dimension': 'price'},
                      {'desc': 'Deleted assignment', 'dimension': 'obsolete'}])
    view = judgment_research_view(facts, row, DAY)
    assert [item['label'] for item in view['dimension_groups']] == ['价格与价差', '已删除维度', '未标注']
    group = view['dimension_groups'][0]
    assert group['bull'][0]['desc'] == 'Higher explicit price' and group['bull'][0]['evidence_index'] == 1
    assert group['bear'][0]['desc'] == 'Lower explicit price'
    assert view['dimension_groups'][-1]['bull'][0]['desc'] == 'No tag'
    html = fromstring(render(**view))
    groups = html.xpath('//*[@data-dimension-group]')
    assert [item.get('data-dimension-group') for item in groups] == ['price', 'obsolete', '']
    assert groups[0].xpath('.//*[@data-hypothesis="bull"]//*[contains(text(),"Higher explicit price")]')
    assert groups[0].xpath('.//*[@data-hypothesis="bear"]//*[contains(text(),"Lower explicit price")]')
    assert html.xpath('//*[@data-judgment-library-target="evidence:bull:1"]')
    assert html.xpath('//*[@id="judgment-thresholds"]//th[text()="维度"]')
    assert html.xpath('//*[@id="judgment-falsifiers"]//th[text()="维度"]')
    restricted = fromstring(render(restricted_view=True, **view))
    assert not restricted.xpath('//*[@data-judgment-library-target]')


def test_custom_dimensions_and_renames_use_current_labels_without_reassigning_ids(facts):
    configure(facts, CUSTOM)
    row = saved(bull=[{'desc': 'Cost change', 'dimension': 'desk_cost'},
                      {'desc': 'No silent remap', 'dimension': 'supply'}])
    first = judgment_research_view(facts, row, DAY)
    assert [item['label'] for item in first['dimension_groups']] == ['成本与利润', '已删除维度']
    config = facts.get(SourceMapping, mapping_key(facts, DIMENSIONS_KEY))
    config.config = {'dimensions': [{**CUSTOM[0], 'label': '成本变化'}]}
    facts.commit()
    assert judgment_research_view(facts, row, DAY)['dimension_groups'][0]['label'] == '成本变化'
    assert row.contradiction['bull']['evidence'][0]['dimension'] == 'desk_cost'


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

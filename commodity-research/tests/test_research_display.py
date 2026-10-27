"""Visible research labels use names while storage and source text remain intact."""
from copy import deepcopy
from datetime import date
from html.parser import HTMLParser
from pathlib import Path
import shutil
import subprocess

from jinja2 import ChoiceLoader, DictLoader, Environment, FileSystemLoader
from tests.test_judgment_generation import generator
from tests.test_knowledge_facts import canonical


FIELD = 'kb-' + 'c' * 48
TECH = 'SHFE.EXAMPLE.main.settle'
NAMES = {FIELD: '库存 / ID123456 / 周 [' + TECH + ']'}


class Text(HTMLParser):
    def __init__(self):
        super().__init__(); self.parts = []; self.hidden = 0
    def handle_starttag(self, tag, attrs):
        if tag in {'script', 'style'}: self.hidden += 1
    def handle_endtag(self, tag):
        if tag in {'script', 'style'}: self.hidden -= 1
    def handle_data(self, value):
        if not self.hidden: self.parts.append(value)


def environment():
    env = Environment(loader=ChoiceLoader([DictLoader({'base.html': '{% block main %}{% endblock %}'}),
        FileSystemLoader(Path(__file__).parents[1] / 'src/tin/web/templates')]), autoescape=True)
    env.globals.update(research_workbench=lambda: None, research_prefix=lambda: '',
        research_static_prefix=lambda: '', static_version=lambda: 'test')
    env.filters.update(num=lambda value, *_: str(value), local=lambda value: str(value))
    try:
        from tin.web.research_display import register_display
    except ImportError:
        pass
    else:
        register_display(env)
    return env


def visible(source):
    parser = Text(); parser.feed(source); return ' '.join(parser.parts)


def test_human_labels_hide_protocol_tails_and_keep_real_contract_names():
    from tin.web.research_display import human_text, indicator_name, reference_label
    assert FIELD not in human_text(FIELD + '回落，合约 AB2601', NAMES)
    assert indicator_name(FIELD, NAMES) == '库存 / 周'
    assert TECH not in human_text('价格 [' + TECH + ']')
    assert 'AB2601' in human_text('跟踪 AB2601')
    assert reference_label({'title': '研判 v23', 'written_at': '2026-01-09'}) == '研究判断 · 2026-01-09'
    assert '42' not in reference_label({'id': '42', 'revision': 23})


def test_actual_judgment_dom_shows_metric_names_without_storage_ids():
    from tin.schemas.judgment import JudgmentIn
    j = JudgmentIn(variety='EXAMPLE', author='owner', written_at=date(2026, 1, 9),
        contradiction={'bull': {'claim': FIELD + '下降', 'evidence': [{'desc': '观察 ' + FIELD, 'series_id': FIELD}]}},
        whitelist=[FIELD], falsifiers=[{'id': 'private-rule-id', 'desc': '核对库存', 'series_id': FIELD,
            'expression': 'change >= 2', 'unit_confirmed': '吨', 'side': 'bull'}]).model_dump()
    j.update(id=42, version=23, status='草稿', review_due='2026-02-09')
    html = environment().get_template('judgment.html').render(j=j, names=NAMES, versions=[], radar=[{
        'name': '库存观察', 'series_id': FIELD, 'series_name': NAMES[FIELD], 'type': '观察位',
        'horizon': '短期', 'condition': '> 10', 'value': 11, 'unit': '吨', 'as_of': None,
        'distance': '接近', 'progress': 50, 'state': '未触发'}],
        block={'expired_days': 0, 'blockers': []}, restricted_view=False,
        selected_judgment_id=None, logic_input=None)
    text = visible(html)
    assert FIELD not in text and TECH not in text and 'ID123456' not in text
    assert '库存' in text and '◈' not in text
    assert 'v23' not in text and '2026-01-09' in text


def test_actual_strategy_plan_dom_resolves_bound_quote_name_without_changing_plan():
    plan = {'legs': [{'contract': 'AB2601', 'side': 'buy', 'ratio': 1, 'quote_series_id': FIELD}],
            'entry_condition': FIELD + '回落 [' + TECH + ']'}
    before = deepcopy(plan)
    html = environment().get_template('_strategy_plan_readonly.html').render(s={'plan': plan}, names=NAMES)
    assert FIELD not in visible(html) and TECH not in visible(html) and 'ID123456' not in visible(html)
    assert 'AB2601' in visible(html) and '库存' in visible(html)
    assert plan == before


def test_report_research_uses_natural_names_in_conditions_and_saved_text_without_mutation():
    from tin.web.report_view import present_snapshot
    payload = {'as_of': '2026-01-09', 'observations': [{'series_id': FIELD, 'name': NAMES[FIELD], 'unit': '吨'}],
        'judgment': {'version': 23, 'written_at': '2026-01-09', 'tone_note': FIELD + '回落',
            'contradiction': {'bull': {'claim': FIELD + '回落'}},
            'thresholds': [{'id': 'private-rule-id', 'name': '', 'series_id': FIELD, 'operator': '>', 'value': 10}]}}
    before = deepcopy(payload)
    view = present_snapshot(payload)
    rule = view['judgment']['thresholds'][0]
    assert rule['name'] != 'private-rule-id' and '库存' in rule['series_name']
    assert FIELD not in view['panels']['research']['text']
    assert TECH not in rule['series_name'] and 'ID123456' not in rule['series_name']
    assert payload == before


def test_actual_authorized_route_registers_helpers_and_renders_saved_binding_names(generator):
    from tests.test_judgment_generation import generate, apply
    from tests.test_knowledge_facts import FIRST, SERIES, page
    from tests.test_workbench_shared_ui import login
    client, headers, state, response, calls = generator
    response['judgment']['thresholds'][0]['name'] += ' [' + TECH + ']'
    preview = generate(client, headers)
    assert preview.status_code == 200, preview.text
    saved = apply(client, headers, preview.json())
    assert saved.status_code == 200, saved.text
    login(client, headers)
    result = client.get(page(FIRST, '/sn/judgment'))
    assert result.status_code == 200, result.text
    text = visible(result.text)
    assert SERIES not in text and TECH not in text
    assert 'Measured quantity' in text and 'Stock watch' in text
    assert 'research-display.js' in result.text and '◈' not in text
    assert len(calls) == 1


def test_actual_shared_javascript_display_keeps_binding_identity():
    node = shutil.which('node')
    assert node, 'Node is required for the actual display script checks'
    result = subprocess.run([node, str(Path(__file__).with_name('research-display-ui.cjs'))],
                            capture_output=True, text=True, timeout=10)
    assert result.returncode == 0, result.stdout + result.stderr
    assert 'research-display UI checks passed' in result.stdout

"""The saved research page explains opposing hypotheses, not storage fields."""
from datetime import date
from types import SimpleNamespace

from jinja2 import ChoiceLoader, DictLoader

from tin.schemas.judgment import JudgmentIn
from tin.web import app as web


def render(**overrides):
    payload = JudgmentIn(variety='XY', author='Synthetic', written_at=date(2026, 1, 2),
        contradiction={'bull': {'claim': 'Supply is constrained'},
                       'bear': {'claim': 'Demand is soft'}}).model_dump(mode='json')
    judgment = SimpleNamespace(**payload, id=1, status='草稿', review_due=date(2026, 2, 1))
    metric = {'name': 'Shared measured stock', 'unit': 'kg', 'latest': 0, 'previous': 2,
        'date': '2026-01-02', 'previous_date': '2026-01-01', 'change': -2,
        'change_pct': -100, 'point_count': 2, 'segments': ['0,3 240,45']}
    data = {'sides': {
        'bull': {'claim': 'Supply is constrained', 'evidence': [{'desc': 'Supply evidence', 'metric': metric}], 'falsifiers': []},
        'bear': {'claim': 'Demand is soft', 'evidence': [{'desc': 'Demand evidence', 'metric': metric}], 'falsifiers': []}},
        'shared': [{'metric': metric, 'bull_texts': ['Supply evidence'], 'bear_texts': ['Demand evidence'],
                    'bull_falsifiers': [], 'bear_falsifiers': []}],
        'counts': {'bound': 1, 'available': 1, 'thresholds': 0, 'falsifiers': 0, 'triggered': 0},
        'pending_conditions': [{'text': 'Measured stock above 12 kg', 'requires_manual_observation': True}]}
    data.update(overrides)
    env = web.templates.env.overlay(loader=ChoiceLoader([
        DictLoader({'base.html': '{% block main %}{% endblock %}', '_judgment_manager.html': ''}),
        web.templates.env.loader]))
    env.globals = dict(web.templates.env.globals)
    env.globals.update(research_workbench=lambda: None, research_identity=lambda: None)
    return env.get_template('judgment.html').render(j=judgment, names={}, research=data,
        block={'expired_days': 0, 'blockers': []}, versions=[], radar=[], d=date(2026, 1, 2))


def test_hypotheses_precede_field_details_and_show_their_actual_evidence():
    html = render()
    assert '多方逻辑' in html and '空方逻辑' in html
    assert html.index('Supply is constrained') < html.index('阈值监测')
    assert 'Supply evidence' in html and 'Demand evidence' in html
    assert 'data-metric-trend' in html and 'Shared measured stock' in html
    assert '前次' in html and '2026-01-01' in html
    assert '其余字段' not in html and '九字段结构化研判' not in html


def test_shared_indicator_compares_both_explanations_without_an_invented_strength_score():
    html = render()
    assert 'data-shared-indicator' in html
    assert 'data-hypothesis="bull"' in html and 'data-hypothesis="bear"' in html
    assert '多空分歧对照' in html
    assert 'data-evidence-weight' not in html and '多方胜率' not in html


def test_pending_numeric_conditions_are_visible_even_with_no_executable_threshold():
    html = render()
    assert 'Measured stock above 12 kg' in html
    assert '待验证条件' in html
    assert '该判断没有登记阈值' not in html
    assert 'data-threshold-fill' not in html


def test_no_data_is_not_presented_as_zero_or_as_a_trigger():
    data = {'sides': {'bull': {'claim': 'Supply claim', 'evidence': [{'desc': 'No data', 'metric': {
        'name': 'Missing field', 'unit': 'kg', 'latest': None, 'previous': None,
        'change': None, 'change_pct': None, 'segments': [], 'point_count': 0}}], 'falsifiers': []},
        'bear': {'claim': '', 'evidence': [], 'falsifiers': []}}, 'shared': []}
    html = render(**data)
    assert '暂无可用数值' in html
    assert 'data-metric-trend' not in html

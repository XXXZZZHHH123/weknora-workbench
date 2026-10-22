from datetime import date, datetime, timezone
from pathlib import Path
from types import SimpleNamespace
import json

from jinja2 import ChoiceLoader, DictLoader, Environment, FileSystemLoader, select_autoescape
import pytest
from sqlalchemy import event

from tin.models import DailyBrief, Judgment, Researcher, ReviewTask
from tin.web.restricted_home import build_home_context


SERIES = 'kb-' + 'a' * 48
STAMP = datetime(2026, 1, 2, tzinfo=timezone.utc)


def records(session):
    owners = {}
    for actor in ('owner-account', 'visitor-account', 'other-account'):
        row = Researcher(username=actor, weknora_user_id=actor, display_name=actor,
                         varieties=[], status='在职', created_at=STAMP)
        session.add(row); session.flush(); owners[actor] = row
        session.add(DailyBrief(researcher_id=row.id, variety='SN', trade_date='2026-01-02',
            status='已采纳', tone='区间震荡', stance='观望', summary=actor + ' shared summary',
            range_low=10, range_high=20, claims=[{'text': actor + ' shared claim', 'evidence': [
                {'series_id': SERIES, 'value': 99008877, 'source_snapshots': ['PRIVATE_SNAPSHOT']}]}],
            unverified=[], playbook_item_ids=[], signal_ids=[], generated_at=STAMP))
    judgment = Judgment(researcher_id=owners['owner-account'].id, variety='SN', version=1,
        author='owner-account', written_at=date(2026, 1, 2), review_due=date(2026, 2, 2),
        contradiction={}, marginal_focus=[], pricing_power={}, whitelist=[], thresholds=[], falsifiers=[],
        status='生效', created_at=STAMP)
    session.add(judgment); session.flush()
    session.add(ReviewTask(judgment_id=judgment.id, trigger_type='判断到期', assignee='owner-account',
                           state='待处理', created_at=STAMP))
    session.commit()


def scope(owner='owner-account'):
    return SimpleNamespace(metadata={'owner_id': owner, 'name': 'Shared workbench'},
                           identity=SimpleNamespace(user_id='visitor-account'),
                           view_modules=('home', 'market', 'judgments'))


def values():
    return {SERIES: {'name': 'Allowed indicator', 'unit': 'tonnes', 'value': 18,
                     'date': '2026-01-02', 'change': .5, 'change_pct': 2.857}}


def test_home_projects_shared_owner_records_and_minimal_fresh_values(session):
    records(session)
    result = build_home_context(session, scope(), [SERIES], values())
    assert result['brief']['summary'] == 'owner-account shared summary'
    assert result['brief']['claims'][0]['text'] == 'owner-account shared claim'
    assert result['brief']['claims'][0]['evidence'][0]['value'] == 18
    assert len(result['todos']) == 1
    assert result['changes']['core'][0]['value'] == 18
    assert result['cards'][0].value == 18
    encoded = json.dumps(result, default=lambda x: x.__dict__)
    assert 'visitor-account shared' not in encoded and 'other-account shared' not in encoded
    assert 'PRIVATE_SNAPSHOT' not in encoded and '99008877' not in encoded


@pytest.mark.parametrize('owner', [None, '', 'nonexistent-owner'])
def test_missing_owner_never_falls_back_to_visitors_or_default_researcher(session, owner):
    records(session)
    result = build_home_context(session, scope(owner), [SERIES], values())
    assert result['brief'] is None and result['todos'] == []
    assert result['owner_unavailable'] is True
    assert '归属' in result['brief_gap']


def test_legacy_results_without_workbench_binding_are_not_shared(session):
    records(session); session.info['source_storage_legacy'] = True
    result = build_home_context(session, scope(), [SERIES], values())
    assert result['legacy_unassigned'] is True
    assert result['brief'] is None and result['todos'] == []
    assert '未归属' in result['brief_gap']
    assert result['cards'][0].value == 18


def test_home_never_queries_local_facts_or_whole_indicator_catalog(session):
    records(session)
    statements = []
    def inspect(conn, cursor, statement, parameters, context, executemany):
        statements.append(statement.lower())
    bind = session.get_bind(); event.listen(bind, 'before_cursor_execute', inspect)
    try:
        result = build_home_context(session, scope(), [SERIES], values())
    finally:
        event.remove(bind, 'before_cursor_execute', inspect)
    assert result['brief']['summary']
    assert not any(any('from ' + table in query for table in
                       ('observations', 'indicators', 'derived', 'trading_days')) for query in statements)


def test_readonly_template_keeps_four_original_sections_without_edit_controls(session):
    records(session); current = scope()
    result = build_home_context(session, current, [SERIES], values())
    directory = Path(__file__).parents[1] / 'src/tin/web/templates'
    environment = Environment(loader=ChoiceLoader([
        DictLoader({'base.html': '{% block main %}{% endblock %}'}), FileSystemLoader(directory)]),
        autoescape=select_autoescape())
    environment.filters['num'] = lambda value, *args: str(value) if value is not None else '—'
    environment.globals.update(research_prefix=lambda: '/research', research_workbench=lambda: current)
    html = environment.get_template('readonly_home.html').render(restricted_data=result, restricted_view=True)
    for title in ('待办', '今日简报', '昨夜到今早什么变了', '全景数据墙'):
        assert title in html
    for marker in ('brief-adopt', 'brief-edit-open', 'brief-reject-open', 'brief-claims', '<form', 'download='):
        assert marker not in html
    assert 'owner-account shared summary' in html
    assert 'PRIVATE_SNAPSHOT' not in html and '99008877' not in html
    assert 'data-readonly-home' in html

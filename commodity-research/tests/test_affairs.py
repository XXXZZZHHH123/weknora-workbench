"""Workbench affairs use authored metadata, never a market-wide fact read."""
from dataclasses import replace
from datetime import date, datetime, timezone
import json
from pathlib import Path
from uuid import uuid4

from fastapi import HTTPException
from jinja2 import ChoiceLoader, DictLoader, Environment, FileSystemLoader, select_autoescape
import pytest
from sqlalchemy import event, select

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session
from tin.models import Judgment, Researcher, Signal, Strategy
from tin.models.workbench import WorkbenchArtifact
from tin.web.research_references import remember_record_scope

WB = '7ac25ec4-378c-4071-a8ee-c3679617b67a'
OTHER = 'd100e26c-84b5-43de-bc40-7b47762d86bc'
TODAY = date(2026, 3, 3)
STAMP = datetime(2026, 3, 2, 8, tzinfo=timezone.utc)


@pytest.fixture
def affairs_records(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    identity = Identity('owner', '', 'Fixture', 17, 'owner')
    scope = WorkbenchScope(identity, WB, 'fixture-kb', 17,
        {'workspace_mode': 'isolated', 'can_edit': True, 'can_read_source': True}, 'Bearer fixture')
    from tin.web.report_drafts import report_id
    with artifact_session(scope) as session:
        owner = Researcher(username='fixture-owner', weknora_user_id='owner', display_name='Fixture',
            varieties=[], status='在职', created_at=STAMP)
        session.add(owner); session.flush()
        judgment = Judgment(variety='XY', version=1, researcher_id=owner.id, author='owner',
            written_at=date(2026, 2, 1), review_due=date(2026, 3, 1), status='草稿',
            contradiction={}, marginal_focus=[], pricing_power={}, whitelist=[],
            thresholds=[{'id': 'real-rule', 'name': 'Saved rule name'}], falsifiers=[], created_at=STAMP)
        plan = Strategy(title='Actual pending plan', researcher_id=owner.id, variety='XY', kind='directional',
            mode='observation', status='draft', plan={'private': 'NOT_HOME_FACT_PAYLOAD'}, author='owner', created_at=STAMP)
        session.add_all([judgment, plan]); session.flush()
        remember_record_scope(session, 'judgment', str(judgment.id))
        remember_record_scope(session, 'plan', str(plan.id))
        card = WorkbenchArtifact(id=str(uuid4()), module='strategies', actor='owner', title='Due research card',
            status='draft', body=json.dumps({'format': 'research-strategy-card.v1', 'review_date': '2026-03-01',
                'thesis': 'NOT_HOME_CARD_BODY'}), source_ids=[], source_snapshots=[], revision=1,
            created_at=STAMP, updated_at=STAMP)
        report = WorkbenchArtifact(id=report_id(scope, '2026-03-02', 'weekly'), module='briefs', actor='owner', title='Actual weekly draft',
            status='draft', body=json.dumps({'format': 'research-report-draft.v1', 'date': '2026-03-02',
                'period': 'weekly', 'workbench_id': WB, 'sections': {'summary': 'NOT_HOME_REPORT_BODY'}}),
            source_ids=[], source_snapshots=[], revision=1, created_at=STAMP, updated_at=STAMP)
        session.add_all([card, report]); session.flush()
        for row in (card, report): remember_record_scope(session, 'artifact', row.id)
        session.add_all([
            Signal(judgment_id=judgment.id, rule_id='real-rule', rule_type='threshold', trade_date='2026-03-01',
                state='触发', current_value=12, evidence=[], generated_at=STAMP),
            Signal(judgment_id=judgment.id, rule_id='real-rule', rule_type='threshold', trade_date='2026-03-02',
                state='未触发', current_value=7, evidence=[], generated_at=STAMP),
            Signal(judgment_id=judgment.id, rule_id='gap-rule', rule_type='falsifier', trade_date='2026-03-02',
                state='数据缺失', gap_note='Recorded missing point', evidence=[], generated_at=STAMP),
        ])
        session.commit()
        return scope, {'judgment': judgment.id, 'plan': plan.id, 'card': card.id, 'report': report.id}


def test_affairs_uses_real_due_dates_drafts_and_latest_recorded_alerts(affairs_records):
    from tin.web.affairs import build_affairs
    scope, identifiers = affairs_records
    with artifact_session(scope) as session:
        result = build_affairs(session, scope, today=TODAY)
    assert len(result['review']) == 2
    assert len(result['approvals']) == 1 and len(result['reports']) == 1
    assert result['approvals'][0]['href'] == '/sn/strategies/' + str(identifiers['plan'])
    assert result['reports'][0]['href'] == '/sn/reports?date=2026-03-02&period=weekly'
    assert any('judgment_id=' + str(identifiers['judgment']) in item['href'] for item in result['review'])
    assert len(result['alerts']) == 1 and result['alerts'][0]['kind'] == '数据缺失'
    assert result['alerts'][0]['trade_date'] == '2026-03-02'
    assert result['alerts'][0]['recorded_at']
    assert 'recent' not in result, 'The affairs centre must not duplicate other pages history'
    encoded = json.dumps(result, ensure_ascii=False)
    assert 'NOT_HOME_' not in encoded and 'current_value' not in encoded


def test_affairs_does_not_read_fact_tables_or_write_records(affairs_records):
    from tin.web.affairs import build_affairs
    scope, _ = affairs_records
    statements = []
    with artifact_session(scope) as session:
        engine = session.get_bind()
        def record(conn, cursor, statement, parameters, context, executemany): statements.append(statement.lower())
        event.listen(engine, 'before_cursor_execute', record)
        try: build_affairs(session, scope, today=TODAY)
        finally: event.remove(engine, 'before_cursor_execute', record)
    assert statements
    for statement in statements:
        assert not any(name in statement for name in [' observations', ' indicators', ' trading_days'])
        assert not any(name in statement for name in ['judgments.contradiction', 'judgments.source_note'])
        assert not statement.lstrip().startswith(('insert ', 'update ', 'delete '))


def test_affairs_reviews_only_current_formal_version_and_ignores_fake_report_notes(affairs_records):
    from tin.web.affairs import build_affairs
    scope, identifiers = affairs_records
    with artifact_session(scope) as session:
        old = session.get(Judgment, identifiers['judgment'])
        second = Judgment(variety='XY', version=2, researcher_id=old.researcher_id, author='owner',
            written_at=old.written_at, review_due=old.review_due, status='草稿', contradiction={},
            marginal_focus=[], pricing_power={}, whitelist=[], thresholds=[], falsifiers=[],
            created_at=datetime(2026, 3, 3, tzinfo=timezone.utc))
        fake = WorkbenchArtifact(id=str(uuid4()), module='briefs', actor='owner', title='Generic note pretending report',
            status='draft', body=json.dumps({'format': 'research-report-draft.v1', 'date': '2026-03-02',
                'period': 'weekly', 'workbench_id': WB}), source_ids=[], source_snapshots=[], revision=1,
            created_at=STAMP, updated_at=STAMP)
        session.add_all([second, fake]); session.flush()
        remember_record_scope(session, 'judgment', str(second.id))
        remember_record_scope(session, 'artifact', fake.id)
        session.commit()
        result = build_affairs(session, scope, today=TODAY)
        assert [item['href'] for item in result['review'] if '/sn/judgment?' in item['href']] == [
            '/sn/judgment?judgment_id=' + str(second.id)]
        assert len(result['reports']) == 1 and result['reports'][0]['title'] == 'Actual weekly draft'
        old.status = '生效'; session.commit()
        result = build_affairs(session, scope, today=TODAY)
        assert [item['href'] for item in result['review'] if '/sn/judgment?' in item['href']] == [
            '/sn/judgment?judgment_id=' + str(old.id)]


def test_affairs_does_not_enumerate_other_actors_workbenches_or_disabled_modules(affairs_records):
    from tin.web.affairs import build_affairs
    scope, _ = affairs_records
    with artifact_session(scope) as session:
        session.info['workbench_id'] = OTHER
        owner = session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == 'owner'))
        foreign_judgment = Judgment(variety='ZZ', version=1, researcher_id=owner, author='owner',
            written_at=TODAY, review_due=TODAY, status='生效', contradiction={}, marginal_focus=[],
            pricing_power={}, whitelist=[], thresholds=[], falsifiers=[],
            created_at=datetime(2026, 3, 3, 12, tzinfo=timezone.utc))
        session.add(foreign_judgment); session.flush()
        remember_record_scope(session, 'judgment', str(foreign_judgment.id))
        for module in ['strategies', 'briefs']:
            row = WorkbenchArtifact(id=str(uuid4()), module=module, actor='owner', title='FOREIGN_WORKBENCH',
                body=json.dumps({'format': 'research-strategy-card.v1', 'review_date': '2026-01-01'}),
                source_ids=[], source_snapshots=[], status='draft', revision=1, created_at=STAMP, updated_at=STAMP)
            session.add(row); session.flush(); remember_record_scope(session, 'artifact', row.id)
        session.commit()
    with artifact_session(scope) as session:
        result = build_affairs(session, scope, today=TODAY)
        assert 'FOREIGN_WORKBENCH' not in json.dumps(result)
        assert len(result['review']) == 2 and 'ZZ' not in json.dumps(result)
        hidden = build_affairs(session, replace(scope, view_modules=('home',)), today=TODAY)
        assert all(hidden[key] == [] for key in ['alerts', 'review', 'approvals', 'reports'])
    stranger = replace(scope, identity=Identity('stranger', '', 'Stranger', 17, 'viewer'))
    with artifact_session(stranger) as session:
        empty = build_affairs(session, stranger, today=TODAY)
        assert all(empty[key] == [] for key in ['alerts', 'review', 'approvals', 'reports'])
        assert session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == 'stranger')) is None


def test_affairs_refuses_unverified_or_restricted_scope(affairs_records):
    from tin.web.affairs import build_affairs
    scope, _ = affairs_records
    with artifact_session(scope) as session:
        for invalid in [None, replace(scope, metadata={**scope.metadata, 'can_read_source': False}),
                        replace(scope, view_modules=('judgments',))]:
            with pytest.raises(HTTPException): build_affairs(session, invalid, today=TODAY)


def test_affairs_template_has_alerts_and_real_deep_links_without_fake_health_claims(affairs_records):
    from tin.web.affairs import build_affairs
    scope, _ = affairs_records
    with artifact_session(scope) as session:
        result = build_affairs(session, scope, today=TODAY)
    result['alerts'][0]['title'] = '<img src=x onerror=unsafe>'
    templates = Path(__file__).parents[1] / 'src/tin/web/templates'
    environment = Environment(loader=ChoiceLoader([DictLoader({'base.html': '{% block head %}{% endblock %}{% block main %}{% endblock %}'}),
        FileSystemLoader(templates)]), autoescape=select_autoescape(['html']))
    from tin.web.app import _num
    environment.filters['num'] = _num
    environment.globals.update(research_url=lambda value: '/wb' + value, research_static_prefix=lambda: '/static',
        static_version=lambda: 'fixture', research_workbench=lambda: scope, research_prefix=lambda: '/wb')
    html = environment.get_template('home.html').render(affairs=result, err=None)
    assert '事务中心' in html and '已记录预警' in html
    assert 'affairs-check' in html and 'period=weekly' in html
    assert '<img src=x' not in html and '&lt;img' in html
    assert '全景数据墙' not in html and '昨夜到今早' not in html
    assert '证伪条件没触发' not in html

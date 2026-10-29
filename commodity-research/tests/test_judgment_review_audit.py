"""Review transaction failure boundaries using synthetic, temporary storage."""
import asyncio
from copy import deepcopy
from types import SimpleNamespace

import pytest
from fastapi import HTTPException
from sqlalchemy import event, func, select
from sqlalchemy.orm import sessionmaker

from tin.db import Base, make_engine
from tin.judgments.analysis import assess_draft
from tin.models import AuditLog, SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_review as review


@pytest.fixture
def decisions(tmp_path, monkeypatch):
    engine = make_engine('sqlite:///' + str(tmp_path / 'review-audit.sqlite'))
    Base.metadata.create_all(engine, tables=[SourceMapping.__table__, AuditLog.__table__])
    factory = sessionmaker(engine, expire_on_commit=False)
    monkeypatch.setattr(review, 'artifact_session', lambda scope: factory())
    store = generation.PreviewStore()
    monkeypatch.setattr(generation, 'previews', store)
    scope = SimpleNamespace(identity=SimpleNamespace(tenant_id=23, user_id='synthetic-user'),
        workbench_id='10000000-0000-4000-8000-000000000123',
        knowledge_base_id='synthetic-kb', knowledge_base_tenant_id=23)
    item = {'id': 'item-synthetic', 'path': ['marginal_focus', 0],
        'value': {'desc': 'Synthetic quantity', 'series_id': 'synthetic-series'},
        'series_id': 'synthetic-series', 'status': 'confirmed', 'source_quote': 'Synthetic source'}
    before = {'variety': 'XY', 'text': 'Synthetic source',
        'review': {'revision': 1, 'audit': [], 'items': [{**item, 'status': 'pending'}]}}
    changed = deepcopy(before)
    changed['review'].update(revision=2, items=[item])
    token = store.put(scope, before)
    catalog = [{'id': 'synthetic-series', 'name': 'Synthetic quantity', 'unit': 'kg'}]
    yield SimpleNamespace(engine=engine, factory=factory, store=store, scope=scope,
        item=item, before=before, changed=changed, token=token, catalog=catalog)
    engine.dispose()


def assert_no_decision_rows(case):
    with case.factory() as session:
        assert session.scalar(select(func.count()).select_from(SourceMapping)) == 0
        assert session.scalar(select(func.count()).select_from(AuditLog)) == 0


@pytest.mark.parametrize('invalidated', ['revision', 'expired', 'consumed'])
def test_failed_preview_cas_cannot_commit_audit_or_remembered_choice(decisions, invalidated):
    case = decisions
    if invalidated == 'revision':
        other = deepcopy(case.before)
        other['review']['revision'] = 3
        case.store.replace(case.scope, case.token, other, 1)
    elif invalidated == 'expired':
        _, owner, value = case.store.items[case.token]
        case.store.items[case.token] = (0, owner, value)
    else:
        case.store.consume(case.token)

    with pytest.raises(HTTPException) as caught:
        review.record_decisions(case.scope, case.token, case.before, case.changed,
            case.catalog, [(case.item, True)])

    assert caught.value.status_code == 409
    assert_no_decision_rows(case)
    if invalidated == 'revision':
        assert case.store.get(case.scope, case.token)['review']['revision'] == 3


def test_database_commit_failure_rolls_back_preview_audit_and_binding(decisions):
    case = decisions

    def reject_commit(session):
        session.flush()
        raise RuntimeError('synthetic commit failure')

    event.listen(case.factory, 'before_commit', reject_commit)
    try:
        with pytest.raises(RuntimeError, match='synthetic commit failure'):
            review.record_decisions(case.scope, case.token, case.before, case.changed,
                case.catalog, [(case.item, True)])
    finally:
        event.remove(case.factory, 'before_commit', reject_commit)

    assert_no_decision_rows(case)
    assert case.store.get(case.scope, case.token) == case.before


def test_successful_decision_commits_exactly_one_private_memory_and_audit(decisions):
    case = decisions
    review.record_decisions(case.scope, case.token, case.before, case.changed,
        case.catalog, [(case.item, True)])

    with case.factory() as session:
        preference = session.scalars(select(SourceMapping)).one()
        audit = session.scalars(select(AuditLog)).one()
        assert preference.confirmed_by == case.scope.identity.user_id
        assert preference.config['workbench_id'] == case.scope.workbench_id
        assert preference.config['user_id'] == case.scope.identity.user_id
        assert preference.config['series_id'] == case.item['series_id']
        assert audit.detail['draft_sha256'] == review.digest(case.token)
        assert audit.detail['source_quote_sha256'] == review.digest(case.item['source_quote'])
    assert case.store.get(case.scope, case.token)['review']['revision'] == 2


def test_same_preview_cannot_be_reviewed_while_save_holds_mutation(decisions):
    case = decisions

    async def concurrent_mutations():
        async with case.store.mutation(case.scope, case.token):
            with pytest.raises(HTTPException) as caught:
                async with case.store.mutation(case.scope, case.token):
                    pytest.fail('The second mutation entered a locked preview')
            assert caught.value.status_code == 409
        async with case.store.mutation(case.scope, case.token):
            pass

    asyncio.run(concurrent_mutations())


@pytest.mark.parametrize('declared_units', [{}, {'synthetic-series': 'lb'}])
def test_quantitative_unit_declaration_cannot_be_silently_repaired_by_confirmation(declared_units):
    source = 'Synthetic amount above 12 kg requires review.'
    catalog = [{'id': 'synthetic-series', 'name': 'Synthetic amount', 'unit': 'kg'}]
    document = {'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'watch', 'name': 'Synthetic condition', 'series_id': 'synthetic-series',
        'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': declared_units, 'rule_quotes': {'watch': 'Synthetic amount above 12 kg'},
        'unresolved': []}
    assessment = assess_draft(document, source, catalog, variety='XY', actor='synthetic-user')
    assert not assessment.document['judgment']['thresholds']
    preview = {'text': source, 'variety': 'XY', 'written_at': '2026-01-01'}
    review.initialize_review(preview, assessment, catalog)
    item = next(item for item in preview['review']['items'] if item['kind'] == 'thresholds')
    scope = SimpleNamespace(identity=SimpleNamespace(user_id='synthetic-user'))

    assert review.confirmation_error(preview, item, catalog, scope), (
        'A missing or incorrect model unit declaration needs an explicit selection of the real field')

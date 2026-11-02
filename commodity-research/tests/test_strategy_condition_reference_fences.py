"""Pinned condition citations fence deletion without reading indicator facts."""
from dataclasses import replace
from datetime import datetime, timezone
import json
from uuid import uuid4

from fastapi import HTTPException
import pytest
from sqlalchemy import select

from tin.config import settings
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session
from tin.judgments.service import save_version
from tin.models import Judgment
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_lifecycle, reference_writes
from tin.web.research_references import remember_record_scope
from tests.test_structured_judgment_import import payload


@pytest.fixture
def scope(tmp_path, monkeypatch):
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    return WorkbenchScope(Identity('condition-owner', '', '', 17, 'owner'), str(uuid4()),
        'synthetic-library', 17, {'workspace_mode': 'isolated', 'can_edit': True}, 'Bearer synthetic')


def seed(scope):
    body = payload()
    sid = body['whitelist'][0]
    body.update(thresholds=[{'id': 'threshold_edge', 'name': 'A numeric observation', 'series_id': sid,
        'operator': '>', 'value': 12, 'type': '观察位', 'horizon': '短期'}],
        falsifiers=[{'id': 'falsifier_edge', 'desc': 'A contrary observation', 'series_id': sid,
            'expression': 'change > 0', 'unit_confirmed': '吨', 'side': 'bull', 'consecutive_periods': 2}])
    with artifact_session(scope) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog={sid: {'unit': '吨'}})
        row = save_version(session, JudgmentIn.model_validate(body), actor=scope.identity.user_id)
        session.commit()
        return row.id, row.version


def citation(identifier, version, *, kind='threshold', rule_id=None):
    return {'field': 'trigger', 'kind': 'judgment_condition', 'id': str(identifier),
            'revision': version, 'rule_kind': kind,
            'rule_id': rule_id or kind + '_edge'}


def add_card(session, scope, body, *, historical=None):
    now = datetime.now(timezone.utc)
    row = WorkbenchArtifact(id=str(uuid4()), module='strategies', actor=scope.identity.user_id,
        title='Synthetic authored card', body=json.dumps(body), status='draft', source_ids=[],
        source_snapshots=[], revision=2 if historical is not None else 1, created_at=now, updated_at=now)
    session.add(row); session.flush()
    remember_record_scope(session, 'artifact', row.id)
    if historical is not None:
        session.add(WorkbenchArtifactRevision(artifact_id=row.id, revision=1, title=row.title,
            body=json.dumps(historical), status='draft', source_ids=[], source_snapshots=[],
            edited_by=scope.identity.user_id, recorded_at=now))
    return row


@pytest.mark.parametrize('kind', ['threshold', 'falsifier'])
def test_exact_condition_resolves_only_from_the_pinned_authorized_judgment(scope, kind):
    from tin.web.research_references import resolve_judgment_condition

    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        value = resolve_judgment_condition(session, scope, citation(identifier, version, kind=kind))
        assert value['rule_kind'] == kind and value['rule_id'] == kind + '_edge'
        assert value['condition']['id'] == kind + '_edge'
        assert value['id'] == str(identifier) and value['revision'] == version


@pytest.mark.parametrize('change', [
    lambda c: c.update(revision=2), lambda c: c.update(revision=True),
    lambda c: c.update(rule_id='missing'), lambda c: c.update(rule_kind='falsifier'),
    lambda c: c.update(rule_kind='unknown'), lambda c: c.update(id=1),
    lambda c: c.update(rule_id=''),
])
def test_condition_identity_never_falls_back_to_name_other_rule_or_latest_version(scope, change):
    from tin.web.research_references import resolve_judgment_condition

    identifier, version = seed(scope)
    body = citation(identifier, version); change(body)
    with artifact_session(scope) as session, pytest.raises(HTTPException):
        resolve_judgment_condition(session, scope, body)


def test_condition_lookup_keeps_owner_workbench_and_scope_guards(scope):
    from tin.web.research_references import resolve_judgment_condition

    identifier, version = seed(scope)
    alternate = replace(scope, identity=Identity('other-owner', '', '', 17, 'owner'))
    with artifact_session(scope) as session, pytest.raises(HTTPException):
        resolve_judgment_condition(session, alternate, citation(identifier, version))
    other = replace(scope, workbench_id=str(uuid4()))
    with artifact_session(other) as session, pytest.raises(HTTPException):
        resolve_judgment_condition(session, other, citation(identifier, version))


def test_exact_existing_unicode_condition_id_is_not_replaced_by_its_display_name(scope):
    from tin.web.research_references import resolve_judgment_condition

    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        row = session.get(Judgment, identifier)
        row.thresholds = [{**rule, 'id': '观察条件甲'} for rule in row.thresholds]
        session.commit()
        result = resolve_judgment_condition(session, scope,
            citation(identifier, version, rule_id='观察条件甲'))
        assert result['condition']['id'] == '观察条件甲'
        with pytest.raises(HTTPException):
            resolve_judgment_condition(session, scope,
                citation(identifier, version, rule_id=result['condition']['name']))


def test_generic_fence_checks_each_condition_even_when_the_judgment_was_already_seen(scope):
    identifier, version = seed(scope)
    body = {'format': 'research-strategy-card.v1', 'citations': [
        citation(identifier, version), citation(identifier, version, rule_id='missing')]}
    with artifact_session(scope) as session, pytest.raises(HTTPException):
        reference_writes.fence_artifact_references(session, scope, 'strategies', json.dumps(body))


@pytest.mark.parametrize('format_value', [None, 'research-text.v1'])
def test_generic_writer_cannot_hide_condition_pins_by_omitting_or_changing_card_format(scope, format_value):
    identifier, version = seed(scope)
    body = {'citations': [citation(identifier, version, rule_id='missing')]}
    if format_value:
        body['format'] = format_value
    with artifact_session(scope) as session, pytest.raises(HTTPException):
        reference_writes.fence_artifact_references(session, scope, 'strategies', json.dumps(body))


@pytest.mark.parametrize('change', ['deletion', 'version'])
def test_fenced_condition_cannot_publish_after_deletion_or_version_change(scope, change):
    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        reference_writes.fence_reference(session, scope, citation(identifier, version))
        session.rollback()
    with artifact_session(scope) as session:
        row = session.get(Judgment, identifier)
        if change == 'deletion':
            row.status = '已删除'
        else:
            row.version += 1
        session.commit()
    with artifact_session(scope) as session, pytest.raises(HTTPException):
        reference_writes.fence_reference(session, scope, citation(identifier, version))


@pytest.mark.parametrize('historical', [False, True])
def test_condition_citation_protects_judgment_deletion_in_current_and_historical_cards(scope, historical):
    identifier, version = seed(scope)
    body = {'format': 'research-strategy-card.v1', 'citations': [citation(identifier, version)]}
    with artifact_session(scope) as session:
        add_card(session, scope, {'format': 'research-strategy-card.v1'} if historical else body,
                 historical=body if historical else None)
        session.commit()
        with pytest.raises(HTTPException) as caught:
            judgment_lifecycle.ensure_unreferenced(session, scope, session.get(Judgment, identifier))
        assert caught.value.status_code == 409


def test_indicator_and_file_citations_do_not_impersonate_judgment_identity(scope):
    identifier, version = seed(scope)
    body = {'format': 'research-strategy-card.v1', 'citations': [
        {'field': 'trigger', 'kind': 'indicator', 'id': str(identifier), 'revision': version},
        {'field': 'trigger', 'kind': 'knowledge_file', 'id': str(identifier), 'revision': version}]}
    with artifact_session(scope) as session:
        reference_writes.fence_artifact_references(session, scope, 'strategies', json.dumps(body))
        add_card(session, scope, body); session.commit()
        judgment_lifecycle.ensure_unreferenced(session, scope, session.get(Judgment, identifier))


def test_nested_fixed_strategy_version_is_checked_without_typing_away_condition_metadata(scope):
    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        child = add_card(session, scope, {'format': 'research-strategy-card.v1', 'citations': [
            citation(identifier, version, rule_id='missing')]})
        session.commit()
        body = {'format': 'research-strategy-card.v1', 'reference': {
            'kind': 'strategy', 'id': 'research:' + child.id, 'revision': child.revision}}
        with pytest.raises(HTTPException):
            reference_writes.fence_artifact_references(session, scope, 'strategies', json.dumps(body))


def test_legacy_whole_judgment_pin_and_plain_strategy_notes_keep_existing_behavior(scope):
    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        reference_writes.fence_reference(session, scope,
            {'kind': 'judgment', 'id': str(identifier), 'revision': version})
        reference_writes.fence_artifact_references(session, scope, 'strategies', 'An ordinary saved note')
        session.rollback()


def test_stale_condition_lookup_cannot_publish_after_another_transaction_deletes_source(scope):
    from tin.web.research_references import resolve_judgment_condition

    identifier, version = seed(scope)
    with artifact_session(scope) as stale:
        resolve_judgment_condition(stale, scope, citation(identifier, version))
        with artifact_session(scope) as deleting:
            deleting.get(Judgment, identifier).status = '已删除'
            deleting.commit()
        with pytest.raises(HTTPException) as blocked:
            reference_writes.fence_reference(stale, scope, citation(identifier, version))
        assert blocked.value.status_code in {404, 409}
        stale.rollback()
    with artifact_session(scope) as session:
        assert session.get(Judgment, identifier).status == '已删除'
        assert list(session.scalars(select(WorkbenchArtifact))) == []


def test_nested_pin_reads_exact_historical_strategy_body_instead_of_latest(scope):
    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        child = add_card(session, scope, {'format': 'research-strategy-card.v1'}, historical={
            'format': 'research-strategy-card.v1', 'citations': [citation(identifier, version, rule_id='missing')]})
        session.commit()
        old = {'kind': 'strategy', 'id': 'research:' + child.id, 'revision': 1}
        with pytest.raises(HTTPException):
            reference_writes.fence_reference(session, scope, old)
        reference_writes.fence_reference(session, scope, {**old, 'revision': 2})


def test_nested_deletion_scan_follows_fixed_strategy_pins_even_inside_a_cycle(scope):
    identifier, version = seed(scope)
    with artifact_session(scope) as session:
        first = add_card(session, scope, {'format': 'research-strategy-card.v1'})
        second = add_card(session, scope, {'format': 'research-strategy-card.v1',
            'reference': {'kind': 'strategy', 'id': 'research:' + first.id, 'revision': 1},
            'citations': [citation(identifier, version)]})
        first.body = json.dumps({'format': 'research-strategy-card.v1',
            'reference': {'kind': 'strategy', 'id': 'research:' + second.id, 'revision': 1}})
        session.commit()
        parent = {'reference': {'kind': 'strategy', 'id': 'research:' + first.id, 'revision': 1}}
        reference_writes.fence_artifact_references(session, scope, 'strategies', json.dumps({
            'format': 'research-strategy-card.v1', **parent}))
        assert judgment_lifecycle._references(parent, identifier, version, session=session, scope=scope)


@pytest.mark.parametrize('citations', [None, {}, [None], [{'kind': 'unknown'}]])
def test_generic_artifact_cannot_hide_malformed_condition_citation_transport(scope, citations):
    with artifact_session(scope) as session, pytest.raises(HTTPException):
        reference_writes.fence_artifact_references(session, scope, 'strategies', json.dumps({
            'format': 'research-strategy-card.v1', 'citations': citations}))

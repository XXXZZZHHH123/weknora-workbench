"""Real workbench storage with synthetic sources and separate verified identities."""
from contextlib import contextmanager
from copy import deepcopy
from dataclasses import replace
from types import SimpleNamespace

import pytest
from fastapi import HTTPException
from sqlalchemy import func, select

from tin.config import settings
from tin.integration.identity import Identity, current_identity
from tin.integration.workbench_scope import WorkbenchScope, artifact_session, current_workbench
from tin.judgments.analysis import assess_draft
from tin.judgments.service import JudgmentError, current, latest, latest_workbench_payload, save_version
from tin.models import Indicator, Judgment, JudgmentSeriesRef, Observation, SourceMapping
from tin.web import judgment_generation as generation
from tin.web import judgment_review as review
from tin.web import judgment_structured as structured
from tin.web.judgment_panels import list_panels, register_panel, selected_judgment


WB = '10000000-0000-4000-8000-000000000231'
OTHER_WB = '10000000-0000-4000-8000-000000000232'
FIRST_SERIES = 'kb-' + 'a' * 48
SECOND_SERIES = 'kb-' + 'b' * 48
SOURCE = 'Synthetic supply is uncertain. Synthetic quantity is useful evidence.'
VERSION = 'c' * 64


@contextmanager
def active(scope):
    owner_token = current_identity.set(scope.identity)
    workbench_token = current_workbench.set(scope)
    try:
        yield
    finally:
        current_workbench.reset(workbench_token)
        current_identity.reset(owner_token)


@pytest.fixture(params=[('isolated', 1), ('legacy', 1), ('isolated', 2), ('legacy', 2)])
def multiuser(request, tmp_path, monkeypatch):
    mode, protocol = request.param
    monkeypatch.setattr(settings, 'data_dir', tmp_path)
    monkeypatch.setattr(settings, 'weknora_api_url', 'http://synthetic.invalid')
    store = generation.PreviewStore()
    monkeypatch.setattr(generation, 'previews', store)
    sources = ({'id': 'synthetic-binding', 'source_type': 'knowledge_base',
        'source_id': 'synthetic-kb', 'knowledge_base_id': 'synthetic-kb', 'tenant_id': 31,
        'permission': 'editor', 'delegated': False, 'view_modules': ['judgments']},)

    def scope(user, workbench=WB):
        identity = Identity(user, user + '@synthetic.invalid', user, 31, 'contributor')
        return WorkbenchScope(identity, workbench, 'synthetic-kb', 31,
            {'workspace_mode': mode, 'can_edit': True, 'can_read_source': True},
            'Bearer synthetic', scope_version=protocol, sources_revision=1,
            sources=sources if protocol == 2 else ())

    catalog = [{'id': identifier, 'name': 'Synthetic quantity ' + str(index), 'unit': 'kg',
        'source_file': 'synthetic.csv', 'sheet': 'Synthetic', 'source_version': VERSION,
        'knowledge_base_id': 'synthetic-kb', 'knowledge_base_tenant_id': 31}
        for index, identifier in enumerate((FIRST_SERIES, SECOND_SERIES))]
    metadata = {'version': VERSION, 'datasets': [], 'catalog': {
        field['id']: {**field, 'series_id': field['id'], 'metric': field['name']}
        for field in catalog}}
    from tin.integration import kb_facts
    monkeypatch.setattr(kb_facts, 'catalog_snapshot', lambda scope: deepcopy(metadata))
    yield SimpleNamespace(first=scope('synthetic-a'), second=scope('synthetic-b'),
        scope=scope, store=store, catalog=catalog, metadata=metadata, mode=mode,
        protocol=protocol, directory=tmp_path)


def draft(case):
    document = {'judgment': {'contradiction': {'bull': {'claim': 'Synthetic supply is uncertain'}},
        'marginal_focus': [{'desc': 'Synthetic quantity is useful evidence',
            'series_id': FIRST_SERIES}]}, 'binding_units': {FIRST_SERIES: 'kg'},
        'rule_quotes': {}, 'unresolved': []}
    assessment = assess_draft(document, SOURCE, case.catalog, variety='XY', actor='synthetic')
    preview = {'variety': 'XY', 'text': SOURCE, 'written_at': '2030-01-02',
        'fact_version': VERSION, 'base_version': 0, 'source_name': 'Synthetic source',
        'model_id': 'synthetic-model', 'validation_issues': [], 'repair_attempts': 0}
    review.initialize_review(preview, assessment, case.catalog)
    return preview


def decide(case, scope, preview, *, bind):
    token = case.store.put(scope, preview)
    changed = deepcopy(preview)
    item = next(item for item in changed['review']['items'] if item['kind'] == 'binding')
    item.update(status='confirmed' if bind else 'kept_text',
        series_id=SECOND_SERIES if bind else item['series_id'], binding_explicit=bind, origin='user')
    changed['review']['revision'] += 1
    changed['document'] = review.build_document(changed, case.catalog)
    review.checked(changed, changed['document'], case.catalog, scope)
    with active(scope):
        review.record_decisions(scope, token, preview, changed, case.catalog, [(item, bind)])
    return token, changed


def save(case, scope, preview, *, base=0):
    with active(scope), artifact_session(scope) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog=case.metadata['catalog'])
        checked = review.checked(preview, preview['document'], case.catalog, scope)
        row = save_version(session, checked.payload, actor='forged-client-author',
            expected_base_version=base)
        register_panel(session, scope, row)
        session.commit()
        return row.id, row.version, row.researcher_id


def test_same_workbench_binding_and_text_decisions_have_private_previews_and_memories(multiuser):
    case = multiuser
    initial = draft(case)
    first_token, first = decide(case, case.first, initial, bind=True)
    second_token, second = decide(case, case.second, initial, bind=False)

    assert first['document']['judgment']['marginal_focus'][0]['series_id'] == SECOND_SERIES
    assert second['document']['judgment']['marginal_focus'][0]['series_id'] is None
    assert case.store.get(case.first, first_token) == first
    assert case.store.get(case.second, second_token) == second
    for scope, foreign_token in ((case.first, second_token), (case.second, first_token)):
        with pytest.raises(HTTPException) as error:
            case.store.get(scope, foreign_token)
        assert error.value.status_code == 409

    for scope, remembered in ((case.first, True), (case.second, False)):
        new = draft(case)
        original_document = deepcopy(new['document'])
        original_item = next(item for item in new['review']['items'] if item['kind'] == 'binding')
        original_status = original_item['status']
        with active(scope):
            review.apply_preferences(scope, new, case.catalog)
        item = next(item for item in new['review']['items'] if item['kind'] == 'binding')
        assert item['remembered'] is remembered
        assert item['series_id'] == (SECOND_SERIES if remembered else FIRST_SERIES)
        assert item['status'] == ('pending' if remembered else original_status)
        assert item['status'] != 'confirmed'
        # Reuse only prefills an item; the original draft is not rebound.
        assert new['document'] == original_document

    with artifact_session(case.second) as session:
        rows = session.scalars(select(SourceMapping).where(
            SourceMapping.confirmed_by.in_(['synthetic-a', 'synthetic-b']))).all()
        assert len(rows) == 1 and rows[0].confirmed_by == 'synthetic-a'
        assert rows[0].config['user_id'] == 'synthetic-a'


def test_same_workbench_saved_versions_refs_and_panels_cannot_overwrite_other_researcher(multiuser):
    case = multiuser
    initial = draft(case)
    _, first = decide(case, case.first, initial, bind=True)
    _, second = decide(case, case.second, initial, bind=False)
    first_id, first_version, first_owner = save(case, case.first, first)
    second_id, second_version, second_owner = save(case, case.second, second)
    assert first_id != second_id and first_owner != second_owner
    assert first_version == second_version == 1

    for scope, own_id, other_id, expected_series in (
        (case.first, first_id, second_id, SECOND_SERIES),
        (case.second, second_id, first_id, None),
    ):
        with active(scope), artifact_session(scope) as session:
            own = selected_judgment(session, own_id, scope=scope)
            assert own.author == scope.identity.user_id
            assert own.marginal_focus[0]['series_id'] == expected_series
            assert latest(session, 'XY').id == own_id
            assert current(session, 'XY', scope=scope).id == own_id
            assert [row['judgment_id'] for row in list_panels(session, scope)['items']] == [own_id]
            with pytest.raises(HTTPException) as denied:
                selected_judgment(session, other_id, scope=scope)
            assert denied.value.status_code == 404
            refs = session.scalars(select(JudgmentSeriesRef).where(
                JudgmentSeriesRef.judgment_id == own_id)).all()
            assert [ref.series_id for ref in refs] == ([expected_series] if expected_series else [])
            assert session.scalar(select(func.count()).select_from(Indicator)) == 0
            assert session.scalar(select(func.count()).select_from(Observation)) == 0
            assert str(case.directory) in str(session.get_bind().url.database)

    _, next_version, owner = save(case, case.first, first, base=1)
    assert next_version == 2 and owner == first_owner
    with active(case.second), artifact_session(case.second) as session:
        assert latest(session, 'XY').id == second_id
        assert session.get(Judgment, second_id).version == 1
        assert session.get(Judgment, first_id).marginal_focus[0]['series_id'] == SECOND_SERIES


@pytest.mark.parametrize('multiuser', [('legacy', 2)], indirect=True)
def test_legacy_comparison_payload_does_not_copy_other_workbench_content(multiuser):
    case = multiuser
    _, first = decide(case, case.first, draft(case), bind=True)
    first_id, first_version, _ = save(case, case.first, first)
    other = replace(case.first, workbench_id=OTHER_WB)
    with active(other), artifact_session(other) as session:
        assert latest(session, 'XY').version == first_version == 1
        assert latest_workbench_payload(session, 'XY', other) is None
        assert current(session, 'XY', scope=other) is None
        with pytest.raises(HTTPException) as denied:
            selected_judgment(session, first_id, scope=other)
        assert denied.value.status_code == 404


@pytest.mark.parametrize('multiuser', [('legacy', 2)], indirect=True)
def test_legacy_comparisons_use_own_latest_draft_with_global_version_gaps(multiuser):
    case = multiuser
    _, first = decide(case, case.first, draft(case), bind=True)
    first_id, _, _ = save(case, case.first, first)
    other = replace(case.first, workbench_id=OTHER_WB)
    _, second = decide(case, other, draft(case), bind=False)
    second_id, second_version, _ = save(case, other, second, base=1)
    assert second_version == 2

    with active(case.first), artifact_session(case.first) as session:
        # Display still favors an active version, but comparison content must
        # later move to this workbench's newest draft.
        session.get(Judgment, first_id).status = '生效'
        session.commit()
        assert latest(session, 'XY').id == second_id
        payload = latest_workbench_payload(session, 'XY', case.first)
        assert payload.marginal_focus[0].series_id == SECOND_SERIES

    third = deepcopy(first)
    third['text'] += ' Synthetic first-workbench revision three.'
    third_id, third_version, _ = save(case, case.first, third, base=2)
    assert third_version == 3
    with active(case.first), artifact_session(case.first) as session:
        assert current(session, 'XY', scope=case.first).id == first_id
        assert latest_workbench_payload(session, 'XY', case.first).source_note == third['text']
        assert session.get(Judgment, third_id).status == '草稿'
    with active(other), artifact_session(other) as session:
        payload = latest_workbench_payload(session, 'XY', other)
        assert payload.source_note == second['text']
        assert payload.marginal_focus[0].series_id is None
        assert latest(session, 'XY').version == 3


@pytest.mark.parametrize('multiuser', [('legacy', 2)], indirect=True)
def test_legacy_global_cas_still_rejects_preview_stale_after_other_workbench_save(multiuser):
    case = multiuser
    _, first = decide(case, case.first, draft(case), bind=True)
    save(case, case.first, first)
    other = replace(case.first, workbench_id=OTHER_WB)
    _, second = decide(case, other, draft(case), bind=False)
    save(case, other, second, base=1)

    with pytest.raises(JudgmentError, match='已有新版本'):
        save(case, case.first, first, base=1)
    with active(case.first), artifact_session(case.first) as session:
        assert session.scalar(select(func.count()).select_from(Judgment)) == 2
        assert latest(session, 'XY').version == 2
        assert latest_workbench_payload(session, 'XY', case.first).marginal_focus[0].series_id == SECOND_SERIES


@pytest.mark.parametrize('multiuser', [('legacy', 2)], indirect=True)
def test_legacy_generation_review_and_import_keep_global_cas_without_foreign_payload(multiuser, monkeypatch):
    case = multiuser
    _, first = decide(case, case.first, draft(case), bind=True)
    save(case, case.first, first)
    other = replace(case.first, workbench_id=OTHER_WB)
    prospective = draft(case)
    prospective['base_version'] = 1
    page = {'version': VERSION, 'series': list(case.metadata['catalog'].values()),
        'total': len(case.catalog), 'offset': 0, 'next_offset': None, 'datasets': []}
    monkeypatch.setattr(review, 'catalog_page', lambda *args, **kwargs: deepcopy(page))
    from tin.integration import kb_facts
    monkeypatch.setattr(kb_facts, 'catalog_page', lambda *args, **kwargs: deepcopy(page))

    with active(other):
        generated = generation.snapshot('XY')
        reviewed = review.review_snapshot(other, prospective)
        payload = review.checked(prospective, prospective['document'], case.catalog, other).payload
        imported = structured._state(other, payload)

    assert generated['base_version'] == imported['base_version'] == 1
    assert generated['base_payload'] is None
    assert reviewed['base_payload'] is None
    assert imported['before'] is None

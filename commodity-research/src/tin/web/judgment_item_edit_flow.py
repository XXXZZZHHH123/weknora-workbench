"""The read-only draft and transactional save for edits to a saved judgment."""
from datetime import datetime, timezone

from fastapi import HTTPException
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy.exc import IntegrityError, OperationalError
from starlette.concurrency import run_in_threadpool

from tin.integration.page_facts import LEGACY_ID, SERIES, catalog_page, catalog_snapshot
from tin.integration.workbench_scope import artifact_session, require_shared_write, resolve_workbench
from tin.judgments.analysis import AnalysisError
from tin.judgments.markdown import dump_markdown, version_diff
from tin.judgments.service import (
    JudgmentError, JudgmentVersionConflict, _refs, latest, save_version, stale_payload_dimensions, to_payload,
)
from tin.models import AuditLog
from tin.schemas.judgment import JudgmentIn
from tin.web import judgment_generation as generation, judgment_item_edit as editing
from tin.web.judgment_panels import advance_panel, panel_for_judgment, selected_judgment
from tin.web.research_dimensions import read_dimensions, summaries

SOURCE_CHANGED = '研究工作台来源或权限已改变，请刷新后重新逐条修改'
VERSION_CHANGED = '研判已有新版本或已删除，请刷新后重新逐条修改'


class EditRequest(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    judgment_id: int = Field(ge=1, le=2**63 - 1)
    expected_base_version: int = Field(ge=1, le=2**31 - 1)


def item_catalog(scope, identifiers, version=None):
    identifiers = list(dict.fromkeys(identifiers))
    if not identifiers:
        return {'catalog': {}, 'version': read_page(scope, version, limit=1)['version']}
    catalog = {}
    for offset in range(0, len(identifiers), 200):
        batch = identifiers[offset:offset + 200]
        page = read_page(scope, version, series_ids=batch, limit=200)
        version = page['version']
        found = {field['series_id']: field for field in page['series']}
        if (page.get('next_offset') is not None or page.get('truncated')
                or len(found) != len(page['series']) or found.keys() - set(batch)):
            raise HTTPException(409, '当前指标目录未完整核验，请刷新后重新逐条修改')
        catalog.update(found)
    return {'catalog': catalog, 'version': version}


def read_page(scope, version=None, **query):
    try:
        page = catalog_page(scope, version=version, apply_dedup=False, **query)
    except HTTPException as error:
        if error.status_code in (401, 403, 404, 409):
            raise HTTPException(409, SOURCE_CHANGED) from None
        raise
    if version is not None and page['version'] != version:
        raise HTTPException(409, SOURCE_CHANGED)
    return page


def can_edit(scope):
    return (scope.can_edit and not scope.restricted and scope.metadata.get('can_read_source') is not False
            and 'judgments' in scope.view_modules)


async def verified_scope(scope):
    try:
        checked = await resolve_workbench(scope.identity, scope.workbench_id, scope.authorization)
        require_shared_write(checked)
    except HTTPException as error:
        if error.status_code in (401, 403, 404, 409):
            raise HTTPException(409, SOURCE_CHANGED) from None
        raise
    if (not can_edit(checked) or generation.scope_key(checked) != generation.scope_key(scope)
            or checked.additional_knowledge_bases != scope.additional_knowledge_bases
            or checked.source_delegations != scope.source_delegations):
        raise HTTPException(409, SOURCE_CHANGED)
    return checked


def open_draft(scope, body):
    from tin.web.judgment_review import field_catalog

    with artifact_session(scope) as session:
        row = selected_judgment(session, body.judgment_id, scope=scope)
        _, historical = panel_for_judgment(session, scope, row)
        if row.version != body.expected_base_version or historical:
            raise HTTPException(409, VERSION_CHANGED)
        current = latest(session, row.variety)
        payload = to_payload(row)
        dimensions = summaries(read_dimensions(session))
        saved_audit = editing.read_saved_audit(session, row)
        preview = {'item_edit': True, 'base_judgment_id': row.id,
            'base_judgment_version': row.version, 'base_version': current.version}
    wanted = {identifier for _, _, identifier in _refs(payload)}
    metadata = item_catalog(scope, sorted(wanted))
    if wanted - metadata['catalog'].keys():
        raise HTTPException(409, '原有指标已不在当前授权目录中，请核对来源后重新逐条修改')
    preview['fact_version'] = metadata['version']
    catalog = field_catalog(metadata)
    editing.initialize(preview, payload, catalog, saved_audit, dimensions=dimensions)
    token = generation.previews.put(scope, preview, prefix=generation.ITEM_EDIT_DRAFT_PREFIX)
    return response(scope, token, preview, {'catalog': catalog, 'research_dimensions': dimensions})


def snapshot(scope, preview, page_query=None, series_ids=()):
    from tin.web.judgment_review import field_catalog

    catalog = {field['id']: field for field in preview['review']['catalog']}
    pattern = SERIES if scope.scope_version == 2 else LEGACY_ID
    extra_ids = sorted(sid for sid in set(series_ids) - catalog.keys() if pattern.fullmatch(sid))
    if extra_ids:
        catalog.update((field['id'], field) for field in field_catalog(
            item_catalog(scope, extra_ids, preview['fact_version'])))
    page = None
    if page_query is not None or not extra_ids:
        page = read_page(scope, preview['fact_version'], **(page_query or {'limit': 1}))
    with artifact_session(scope) as session:
        current = latest(session, preview['variety'])
        if current is None or current.version != preview['base_version']:
            raise HTTPException(409, VERSION_CHANGED)
        dimensions = summaries(read_dimensions(session))
    return {'catalog': list(catalog.values()), 'page': page, 'research_dimensions': dimensions}


def response(scope, token, preview, state):
    from tin.web.judgment_review import review_response

    dimensions = state['research_dimensions']
    current = editing.payload(preview, preview['document'], scope.identity.user_id)
    base = JudgmentIn.model_validate(preview['base_payload'])
    units = preview['document']['binding_units']
    bindings = [{'series_id': field['id'], 'name': field['name'], 'unit': units[field['id']]}
                for field in state['catalog'] if field['id'] in units]
    return {'item_edit': True, 'draft_id': token, 'base_version': preview['base_version'],
        'base_judgment_id': preview['base_judgment_id'],
        'variety': preview['variety'], 'source_text': preview['text'],
        'markdown': dump_markdown(current, dimensions=dimensions),
        'diff': version_diff(base, current, dimensions=dimensions, items_only=True), 'bindings': bindings,
        'unresolved': preview['document']['unresolved'], 'validation_issues': [], 'repair_attempts': 0,
        'review_available': True,
        'review': review_response(scope, token, preview, state['catalog'], dimensions=dimensions),
        'notice': ''}


def recommendations(scope, preview, item, search):
    from tin.web.judgment_recall import catalog_index, RECOMMENDATION_LIMIT
    from tin.web.judgment_review import binding, binding_ids, field_catalog

    try:
        metadata = catalog_snapshot(scope, apply_dedup=False)
    except HTTPException as error:
        if error.status_code in (401, 403, 404, 409):
            raise HTTPException(409, SOURCE_CHANGED) from None
        raise
    if metadata['version'] != preview['fact_version']:
        raise HTTPException(409, SOURCE_CHANGED)
    bound = set(binding_ids(item))
    eligible = {field['id']: field for field in field_catalog(metadata) if field['id'] not in bound}
    index = catalog_index(eligible.values())
    query = search.strip() or item['source_quote'] or item['description']
    return [binding(eligible[sid]) for sid in index.ranked(query, RECOMMENDATION_LIMIT)]


def persist(scope, preview):
    changes = editing.changes(preview, preview['document'])
    if not changes:
        raise HTTPException(400, '没有修改')
    try:
        payload = editing.payload(preview, preview['document'], scope.identity.user_id)
    except AnalysisError as error:
        raise HTTPException(422, str(error)) from None
    wanted = {identifier for _, _, identifier in _refs(payload)}
    metadata = item_catalog(scope, sorted(wanted), preview['fact_version'])
    if wanted - metadata['catalog'].keys():
        raise HTTPException(409, SOURCE_CHANGED)
    with artifact_session(scope) as session:
        session.info.update(knowledge_facts=True, knowledge_facts_catalog=metadata['catalog'])
        try:
            try:
                base = selected_judgment(session, preview['base_judgment_id'], scope=scope)
            except HTTPException as error:
                if error.status_code == 404:
                    raise HTTPException(409, VERSION_CHANGED) from None
                raise
            if base.version != preview['base_judgment_version']:
                raise HTTPException(409, VERSION_CHANGED)
            if issues := stale_payload_dimensions(payload, read_dimensions(session)):
                raise HTTPException(422, '；'.join(issues))
            declarations = {rule.id: rule.caliber_confirmation.model_dump()
                for rule in [*payload.thresholds, *payload.falsifiers] if rule.caliber_confirmation is not None}
            row = save_version(session, payload, actor=scope.identity.user_id,
                expected_base_version=preview['base_version'], caliber_confirmations=declarations,
                saved_base=base)
            panel = advance_panel(session, scope, base, row)
            document = preview['document']
            session.add(AuditLog(at=datetime.now(timezone.utc), actor=scope.identity.user_id,
                action=editing.SAVE_ACTION, target_type='judgment', target_id=str(row.id), detail={
                    'workbench_id': scope.workbench_id, 'judgment_id': row.id,
                    'version': row.version, 'base_version': base.version, 'base_judgment_id': base.id,
                    'fact_version': preview['fact_version'], 'model_calls': 0, 'changes': changes,
                    'rule_quotes': document['rule_quotes'], 'binding_units': document['binding_units'],
                    'source_coverage': preview['source_coverage'],
                    'saved_items': editing.saved_items(preview, document)}))
            session.commit()
            return {'id': row.id, 'panel': panel, 'version': row.version, 'status': row.status,
                'notice': '已保存为新版本。'}
        except (JudgmentVersionConflict, IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, VERSION_CHANGED) from None
        except JudgmentError as error:
            session.rollback()
            raise HTTPException(422, str(error)) from None


async def apply(scope, body):
    if not can_edit(scope):
        if generation.previews.belongs_to(scope, body.draft_id):
            raise HTTPException(409, SOURCE_CHANGED)
        raise HTTPException(403, '当前角色不能保存研判')
    async with generation.previews.mutation(scope, body.draft_id):
        preview = generation.previews.get(scope, body.draft_id)
        if preview['base_version'] != body.base_version:
            raise HTTPException(409, VERSION_CHANGED)
        if body.review_revision != preview['review']['revision']:
            raise HTTPException(409, '逐项确认已更新，请刷新预览后保存')
        checked_scope = await verified_scope(scope)
        result = await run_in_threadpool(persist, checked_scope, preview)
        generation.previews.consume(body.draft_id)
        return result

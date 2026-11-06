"""Offer explicit, revision-fenced changes to confirmed indicator references."""
from copy import deepcopy
from datetime import datetime, timezone
import json
from typing import Literal
from types import SimpleNamespace
from uuid import NAMESPACE_URL, uuid5

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, Field, StrictBool, StrictInt, field_validator
from sqlalchemy import update

from tin.integration.identity import research_url
from tin.integration.page_facts import SERIES
from tin.integration.workbench_scope import artifact_session
from tin.models import SourceMapping
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.web import charts, favorites, ppt, restricted_views
from tin.web.chart_data import catalog_fields, definition_series_ids
from tin.web.workbench_artifacts import record_revision

router = APIRouter(prefix='/api/sn/indicator-dedup/references')
ReferenceKind = Literal['favorites', 'core', 'chart', 'ppt', 'judgment', 'strategy']


class RewriteInput(BaseModel):
    model_config = ConfigDict(extra='forbid')
    kind: ReferenceKind
    id: str = Field(default='', max_length=128)
    revision: StrictInt = Field(ge=1)
    sources_revision: StrictInt = Field(ge=1)
    confirmed: StrictBool
    replacements: dict[str, str] = Field(min_length=1)

    @field_validator('replacements')
    @classmethod
    def real_replacements(cls, value):
        if any(not SERIES.fullmatch(source) or not SERIES.fullmatch(target) for source, target in value.items()):
            raise ValueError('引用对应需要明确的真实指标ID')
        return value


def _replacements(scope, identifiers):
    if not identifiers:
        return {}
    from tin.integration.indicator_dedup import confirmed_replacements
    return confirmed_replacements(scope, identifiers)


def _explicit_ids(value):
    """Read declared bindings only; prose and display labels are never searched."""
    result = []
    if isinstance(value, dict):
        for key, child in value.items():
            if key in {'series_id', 'quote_series_id'} and isinstance(child, str) and SERIES.fullmatch(child):
                result.append(child)
            elif key in {'series_ids', 'related_series_ids', 'whitelist'} and isinstance(child, list):
                result.extend(sid for sid in child if isinstance(sid, str) and SERIES.fullmatch(sid))
            elif isinstance(child, (dict, list)):
                result.extend(_explicit_ids(child))
    elif isinstance(value, list):
        for child in value:
            result.extend(_explicit_ids(child))
    return list(dict.fromkeys(result))


def _load(session, scope, kind, identifier):
    if kind == 'favorites':
        row = session.scalar(favorites.owned_query(scope))
        items = [item.model_dump(mode='json') for item in favorites.stored_items(row)]
        return row, {'items': items}, [item['id'] for item in items if item['kind'] == 'indicator'], row.revision if row else 0
    if kind == 'core':
        row = session.get(SourceMapping, restricted_views.mapping_key(session, restricted_views.CORE_KEY))
        items = restricted_views.core_items(session)
        return row, {'items': items}, [item['id'] for item in items if item['kind'] == 'indicator'], row.version if row else 0
    if kind == 'chart':
        row = charts.load_artifact(session, identifier)
        definition = charts.decode_definition(row)
        return row, definition.model_dump(mode='json'), definition_series_ids(definition), row.revision
    if kind == 'ppt':
        row = ppt.load_template(session, scope, identifier)
        value = ppt.contents(row)
        ids = [binding['series_id'] for binding in value['bindings'].values()
               if isinstance(binding, dict) and binding.get('kind') == 'series']
        return row, value, ids, row.revision
    from tin.web.research_references import resolve_reference
    if not identifier:
        raise HTTPException(422, '请明确选择研究记录')
    result = resolve_reference(session, scope, kind, identifier)
    return None, result['payload'], list(dict.fromkeys(result['series_ids'] + _explicit_ids(result['payload']))), result['revision']


def _related_charts(session, kind, value):
    if kind in {'judgment', 'strategy'}:
        # Personal research refers to a fixed chart revision, even if the
        # library's current definition has since replaced every curve.
        result, seen = [], set()
        for pin in value.get('chart_references', []) + value.get('citations', []):
            if not isinstance(pin, dict) or pin.get('kind') != 'chart':
                continue
            key = pin.get('id'), pin.get('revision')
            if not isinstance(key[0], str) or type(key[1]) is not int or key in seen:
                continue
            seen.add(key)
            try:
                row = charts.load_artifact(session, key[0], deleted=True)
            except HTTPException as exc:
                if exc.status_code == 404:
                    continue
                raise
            snapshot = session.get(WorkbenchArtifactRevision, key)
            if snapshot is not None and snapshot.status != 'deleted':
                result.append((SimpleNamespace(id=row.id, title=snapshot.title),
                    definition_series_ids(charts.decode_definition(snapshot))))
        return result
    ids = ([item['id'] for item in value['items'] if item['kind'] == 'chart'] if kind in {'favorites', 'core'} else
           [binding['chart_id'] for binding in value['bindings'].values()
            if isinstance(binding, dict) and binding.get('kind') == 'chart'] if kind == 'ppt' else [])
    result = []
    for identifier in dict.fromkeys(ids):
        try:
            row = charts.load_artifact(session, identifier)
        except HTTPException as exc:
            if exc.status_code == 404:
                continue
            raise
        result.append((row, definition_series_ids(charts.decode_definition(row))))
    return result


@router.get('')
def reference_notices(kind: ReferenceKind, id: str = ''):
    scope = charts.require_scope()
    with artifact_session(scope) as session:
        _, value, identifiers, revision = _load(session, scope, kind, id)
        related = _related_charts(session, kind, value)
    identifiers = list(dict.fromkeys(identifiers + [sid for _, ids in related for sid in ids]))
    replacements = _replacements(scope, list(dict.fromkeys(identifiers + [sid for _, ids in related for sid in ids])))
    fields = catalog_fields(scope, list(dict.fromkeys([sid for sid in identifiers if sid in replacements]
                            + list(replacements.values())))) if replacements else {}
    items = [{'personal_id': sid, 'default_id': replacements[sid],
              'personal_name': fields.get(sid, {}).get('metric') or fields.get(sid, {}).get('name') or sid,
              'default_name': fields.get(replacements[sid], {}).get('metric') or fields.get(replacements[sid], {}).get('name') or replacements[sid],
              'url': research_url('/sn/indicators/' + replacements[sid])}
             for sid in identifiers if sid in replacements and replacements[sid] in fields]
    can_write = scope.identity.can_write if kind == 'favorites' else scope.can_edit
    return {'kind': kind, 'id': id, 'revision': revision, 'sources_revision': scope.sources_revision,
            'items': items, 'can_rewrite': bool(can_write and kind in {'favorites', 'core', 'chart', 'ppt'}),
            'reason': ('研判与策略仅提示，请进入原记录核对后编辑' if kind in {'judgment', 'strategy'} else
                       '' if can_write else '当前账号没有此项内容的编辑权限'),
            'related_charts': [{'id': row.id, 'title': row.title, 'url': research_url('/sn/charts/' + row.id)}
                               for row, ids in related if any(sid in replacements for sid in ids)]}


def _replace_items(items, replacements):
    result, seen = [], set()
    for item in items:
        item = dict(item)
        if item['kind'] == 'indicator':
            item['id'] = replacements.get(item['id'], item['id'])
        key = item['kind'], item['id']
        if key not in seen:
            result.append(item)
            seen.add(key)
    return result


def _core_history(session, scope, value, revision):
    """Reuse the existing immutable artifact history for shared core revisions."""
    identifier = str(uuid5(NAMESPACE_URL, 'core-focus.v1:' + scope.workbench_id))
    history = session.get(WorkbenchArtifact, identifier)
    now = datetime.now(timezone.utc)
    if history is None:
        history = WorkbenchArtifact(id=identifier, module='core_focus', actor=scope.identity.user_id,
            title='核心关注', body=json.dumps({'format': 'core-focus.v1', **value}, ensure_ascii=False),
            status='draft', source_ids=[], source_snapshots=[], revision=revision,
            created_at=now, updated_at=now)
        session.add(history)
        session.flush()
        from tin.web.research_references import remember_record_scope
        remember_record_scope(session, 'artifact', history.id)
        record_revision(session, history, scope.identity.user_id)
    elif history.revision != revision:
        # Normal core edits can occur between two dedup acceptances. Preserve
        # the actual config being replaced as well as earlier dedup revisions.
        history.body = json.dumps({'format': 'core-focus.v1', **value}, ensure_ascii=False)
        history.revision, history.updated_at = revision, now
        record_revision(session, history, scope.identity.user_id)
    return history


@router.post('/rewrite')
def rewrite_references(body: RewriteInput):
    scope = charts.require_scope()
    if body.confirmed is not True:
        raise HTTPException(422, '改写引用需要研究员明确确认')
    if body.sources_revision != scope.sources_revision:
        raise HTTPException(409, '研究台来源已更新，请重新核对对应关系')
    if body.kind not in {'favorites', 'core', 'chart', 'ppt'}:
        raise HTTPException(422, '研判与策略暂只提供提示，不支持自动改写')
    if body.kind == 'favorites':
        if not scope.identity.can_write:
            raise HTTPException(403, '当前空间角色只有读取权限')
    else:
        from tin.integration.workbench_scope import require_shared_write
        require_shared_write(scope)
    with artifact_session(scope) as session:
        _, value, identifiers, revision = _load(session, scope, body.kind, body.id)
    if body.revision != revision:
        raise HTTPException(409, '引用已有新修订，请刷新后重新确认')
    replacements = _replacements(scope, identifiers)
    fields = catalog_fields(scope, list(dict.fromkeys(replacements.values()))) if replacements else {}
    replacements = {sid: target for sid, target in replacements.items() if target in fields}
    if not replacements:
        raise HTTPException(409, '当前没有仍可读取的已确认对应，请重新核对')
    if replacements != body.replacements:
        raise HTTPException(409, '指标对应关系已变化，请重新查看并确认实际default指标；未改写引用')
    if body.kind == 'favorites':
        result = favorites.put_favorites(favorites.FavoritesInput.model_validate({
            'revision': revision, 'items': _replace_items(value['items'], replacements)}))
    elif body.kind == 'chart':
        curves = deepcopy(value['curves'])
        for curve in curves:
            if curve.get('series_id') in replacements:
                curve['series_id'] = replacements[curve['series_id']]
                field = fields[curve['series_id']]
                curve['descriptor'] = {key: str(field.get(key) or '') for key in ('source_file', 'sheet', 'metric', 'column')}
            if curve.get('derived'):
                for key in ('a', 'b'):
                    curve['derived'][key] = replacements.get(curve['derived'][key], curve['derived'][key])
        from tin.schemas.chart import ChartUpdate
        result = charts.update_chart(body.id, ChartUpdate.model_validate({'revision': revision, 'curves': curves}))
    elif body.kind == 'ppt':
        from tin.schemas.ppt import BindingsUpdate
        slots = [{'slot_id': slot, 'binding': {**binding, 'series_id': replacements[binding['series_id']]}}
                 for slot, binding in value['bindings'].items()
                 if isinstance(binding, dict) and binding.get('kind') == 'series' and binding['series_id'] in replacements]
        result = ppt.patch_bindings(body.id, BindingsUpdate.model_validate({'revision': revision, 'slots': slots}))
    else:
        updated = _replace_items(value['items'], replacements)
        with artifact_session(scope) as session:
            row, current, _, current_revision = _load(session, scope, 'core', '')
            if current_revision != revision:
                raise HTTPException(409, '核心关注已有新修订，请刷新后重新确认')
            history = _core_history(session, scope, current, revision)
            changed = session.execute(update(SourceMapping).where(SourceMapping.id == row.id,
                SourceMapping.version == revision).values(config={'items': updated}, version=revision + 1,
                    confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
            if changed.rowcount != 1:
                raise HTTPException(409, '核心关注已有新修订，请刷新后重新确认')
            session.info['knowledge_facts_catalog'] = fields
            restricted_views.remember_origins(session, list(replacements.values()))
            history.body = json.dumps({'format': 'core-focus.v1', 'items': updated}, ensure_ascii=False)
            history.revision, history.updated_at = revision + 1, datetime.now(timezone.utc)
            record_revision(session, history, scope.identity.user_id)
            session.commit()
            result = {'revision': revision + 1, 'items': updated}
    return {'kind': body.kind, 'id': body.id, 'revision': result['revision'], 'changed': replacements}

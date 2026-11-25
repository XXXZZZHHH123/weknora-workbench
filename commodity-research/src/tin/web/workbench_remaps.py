"""Apply confirmed same-original field ID changes to verified workbench bindings."""
from collections import defaultdict
from contextlib import ExitStack
from datetime import datetime, timezone
from copy import deepcopy
import hashlib
import json

from fastapi import HTTPException
from sqlalchemy import delete, select, update
from sqlalchemy.exc import OperationalError

from tin.integration.workbench_scope import (artifact_session, canonical_workbench_id,
    current_workbench, go_request, resolve_workbench)
from tin.judgments.service import _refs, to_payload
from tin.models import Judgment, JudgmentSeriesRef, Researcher, SourceMapping
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision
from tin.schemas.chart import ChartDefinition
from tin.web import charts, favorites, monitors, report_drafts, restricted_views
from tin.web.chart_data import catalog_fields
from tin.web.dedup_references import _core_history, _explicit_ids, _replace_curves, _replace_items
from tin.web.research_dimensions import DIMENSIONS_KEY
from tin.web.research_references import record_in_workbench
from tin.web.workbench_artifacts import record_revision

FORMAT = 'workbench-indicator-remap.v1'
JUDGMENT_FIELDS = ('whitelist', 'contradiction', 'marginal_focus', 'cost_line', 'thresholds',
                   'falsifiers', 'priority_rule', 'unstructured')
SERIES_KEYS = {'series_id', 'quote_series_id'}
SERIES_LISTS = {'series_ids', 'related_series_ids', 'whitelist'}


def _bindings(value, old, new):
    if isinstance(value, list):
        return [_bindings(item, old, new) for item in value]
    if not isinstance(value, dict):
        return value
    replaced = {}
    for key, child in value.items():
        if key in SERIES_KEYS and child == old:
            replaced[key] = new
        elif key in SERIES_LISTS and isinstance(child, list) and old in child:
            replaced[key] = list(dict.fromkeys(new if item == old else item for item in child))
        elif key == 'source_versions' and isinstance(child, dict) and old in child:
            replaced[key] = {new if sid == old else sid: version for sid, version in child.items()}
        else:
            replaced[key] = _bindings(child, old, new)
    if replaced.get('series_id') == new and new in (replaced.get('related_series_ids') or []):
        replaced['related_series_ids'] = [sid for sid in replaced['related_series_ids'] if sid != new]
    return replaced


def _restore(before, after, current):
    """Undo a journaled delta while preserving independent edits."""
    if before == after or before == current:
        return current
    if after == current:
        return before
    if isinstance(before, dict) and isinstance(after, dict) and isinstance(current, dict):
        result = dict(current)
        for key in before.keys() | after.keys():
            if key not in before:
                if result.get(key) == after[key]:
                    result.pop(key, None)
                elif key in result:
                    break
            elif key not in after:
                if key in result and result[key] != before[key]:
                    break
                result[key] = before[key]
            elif key in result:
                result[key] = _restore(before[key], after[key], result[key])
            else:
                break
        else:
            return result
    if isinstance(before, list) and isinstance(after, list) and isinstance(current, list):
        if len(before) == len(after) == len(current):
            return [_restore(prior, applied, actual) for prior, applied, actual in zip(before, after, current)]
    raise HTTPException(409, '已改写的绑定后来又被编辑，请先核对冲突再撤销指标对应')


def _artifact_body(row):
    try:
        body = json.loads(row.body)
    except (ValueError, TypeError):
        return {}
    return body if isinstance(body, dict) else {}


def _report_body(body, old, new):
    if not any(figure.get('series_id') == old for figure in body.get('figures', [])) and (
            'indicator:' + old not in body.get('figure_conclusions', {})):
        return deepcopy(body)
    updated = deepcopy(body)
    seen, figures = set(), []
    for figure in updated.get('figures', []):
        sid = figure['series_id']
        figure['series_id'] = new if sid == old else sid
        if figure['series_id'] not in seen:
            figures.append(figure)
            seen.add(figure['series_id'])
    updated['figures'] = figures
    notes = updated.get('figure_conclusions', {})
    old_key = 'indicator:' + old
    if old_key in notes:
        notes.setdefault('indicator:' + new, notes.pop(old_key))
    return updated


def _dimension_bindings(value, old, new, pins):
    updated = deepcopy(value)
    has_new = any(item['kind'] == 'indicator' and item['id'] == new
                  for dim in value['dimensions'] for section in dim.get('sections', [])
                  for item in section['items'])
    for dim in updated['dimensions']:
        for section in dim.get('sections', []):
            if has_new:
                section['items'] = [item for item in section['items']
                                    if (item['kind'], item['id']) != ('indicator', old)]
            else:
                section['items'] = _replace_items(section['items'], {old: new})
            for item in section['items']:
                if item['kind'] == 'chart':
                    pin = pins.get((item['id'], item['revision']))
                    if pin:
                        item['revision'] = pin['revision']
    return updated


def _chart_pins(value, pins):
    if isinstance(value, list):
        return [_chart_pins(item, pins) for item in value]
    if not isinstance(value, dict):
        return value
    if value.get('kind') == 'chart' and isinstance(value.get('id'), str):
        pin = pins.get((value['id'], value.get('revision')))
        if pin is not None:
            return {**value, **{key: deepcopy(data) for key, data in pin.items()
                                if key == 'revision' or key in value}}
    return {key: _chart_pins(child, pins) for key, child in value.items()}


def _artifact_write(session, scope, row, body, now):
    revision = row.revision
    if session.get(WorkbenchArtifactRevision, (row.id, revision)) is None:
        record_revision(session, row, scope.identity.user_id)
    changed = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == row.id,
        WorkbenchArtifact.revision == revision, WorkbenchArtifact.status != 'deleted').values(
        body=json.dumps(body, ensure_ascii=False, allow_nan=False), revision=revision + 1, updated_at=now))
    if changed.rowcount != 1:
        raise HTTPException(409, '研究记录已有新修订，请刷新后重试')
    session.refresh(row)
    record_revision(session, row, scope.identity.user_id)


def _mapping_write(session, scope, row, body, now):
    revision = row.version
    core = row.id == restricted_views.mapping_key(session, restricted_views.CORE_KEY)
    history = _core_history(session, scope, row.config, revision) if core else None
    changed = session.execute(update(SourceMapping).where(SourceMapping.id == row.id,
        SourceMapping.version == revision).values(config=body, version=revision + 1,
        confirmed_by=scope.identity.user_id, confirmed_at=now))
    if changed.rowcount != 1:
        raise HTTPException(409, '研究台绑定已有新修订，请刷新后重试')
    if history is not None:
        history.body = json.dumps({'format': 'core-focus.v1', **body}, ensure_ascii=False)
        history.revision, history.updated_at = revision + 1, now
        record_revision(session, history, scope.identity.user_id)


def _judgment_write(session, row, body):
    # Rebind only the declared series within the saved analysis. Its version
    # and rule IDs are pinned by reports, panels and existing signal history.
    session.execute(update(Judgment).where(Judgment.id == row.id).values(**body))
    desired = _refs(to_payload(row))
    session.execute(delete(JudgmentSeriesRef).where(JudgmentSeriesRef.judgment_id == row.id))
    session.add_all(JudgmentSeriesRef(judgment_id=row.id, ref_type=kind, ref_id=identifier, series_id=sid)
                    for kind, identifier, sid in desired)


def _owned_judgments(session, scope):
    researcher = session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == scope.identity.user_id))
    if researcher is None:
        return []
    rows = session.scalars(select(Judgment).where(Judgment.researcher_id == researcher,
        Judgment.status != '已删除'))
    return [row for row in rows if record_in_workbench(session, scope, 'judgment', row.id, row)]


def _report_references(session, scope, report):
    snapshots = {}
    for item in report.get('reference_snapshots', []):
        key = (item['kind'], str(item['id']), item['revision'], item['section'])
        try:
            snapshots[key] = report_drafts.freeze_references(session, scope, [item],
                include_charts='chart_references' in item)[0]
        except HTTPException as error:
            if error.status_code not in {403, 404, 409}:
                raise
    return snapshots


def _rewrite_one(session, scope, item, now, records):
    old, field = item['personal']['series_id'], item['default']
    new = field['series_id']
    replacements, fields = {old: new}, {new: field}
    changes = []

    def save(row, kind, before, after):
        if before == after:
            return
        if kind == 'mapping':
            _mapping_write(session, scope, row, after, now)
        elif kind == 'judgment':
            _judgment_write(session, row, after)
        else:
            _artifact_write(session, scope, row, after, now)
        changes.append({'kind': kind, 'id': row.id, 'before': before, 'after': after})

    pins = {}
    for row in records['charts']:
        body = _artifact_body(row)
        if not isinstance(body.get('curves'), list):
            continue
        changed = {**body, 'curves': _replace_curves(body['curves'], replacements, fields)}
        if changed != body:
            definition = ChartDefinition.model_validate(changed)
            from tin.web.chart_references import definition_summary
            pins[(row.id, row.revision)] = {'revision': row.revision + 1, 'definition': changed,
                'definition_summary': definition_summary(definition)}
            save(row, 'chart', body, changed)

    for row in records['mappings']:
        before = row.config
        changed = (_dimension_bindings(before, old, new, pins) if 'dimensions' in before
                   else {**before, 'items': _replace_items(before['items'], replacements)})
        save(row, 'mapping', before, changed)

    favorite = records['favorites']
    if favorite is not None:
        before = _artifact_body(favorite)
        save(favorite, 'favorites', before, {**before, 'items': _replace_items(before['items'], replacements)})

    for row in records['monitors']:
        before = _artifact_body(row)
        changed = _bindings(before, old, new)
        if before != changed:
            previous_revision = row.revision
            save(row, 'monitor', before, changed)
            state = monitors._state(session, scope, row.id)
            if state.get('monitor_revision') == previous_revision and state.get('condition_key') == monitors._condition_key(before):
                state_row = session.scalar(monitors._query(scope, monitors.STATE_MODULE)
                    .where(WorkbenchArtifact.id == monitors._state_id(scope, row.id)))
                if state_row is not None:
                    save(state_row, 'monitor_state', state, {**_bindings(state, old, new),
                        'condition_key': monitors._condition_key(changed), 'monitor_revision': row.revision})

    for row in records['monitor_drafts']:
        before = _artifact_body(row)
        save(row, 'monitor_draft', before, _bindings(before, old, new))

    judgments = {}
    for row in records['judgments']:
        before = {key: getattr(row, key) for key in JUDGMENT_FIELDS}
        changed = _bindings(_chart_pins(before, pins), old, new)
        if before != changed:
            judgments[row.id] = (row, before, changed)

    reports = [(row, _artifact_body(row)) for row in records['reports']]
    impacted = [(row, body, _report_references(session, scope, body))
                for row, body in reports if judgments and body.get('reference_snapshots')]
    for row, before, changed in judgments.values():
        save(row, 'judgment', before, changed)
    refreshed = {row.id: (snapshots, _report_references(session, scope, body))
                 for row, body, snapshots in impacted}
    for row, before in reports:
        changed = _report_body(before, old, new)
        if row.id in refreshed:
            prior, current = refreshed[row.id]
            for index, snapshot in enumerate(changed['reference_snapshots']):
                key = (snapshot['kind'], str(snapshot['id']), snapshot['revision'], snapshot['section'])
                previous, latest = prior.get(key), current.get(key)
                if (previous is not None and latest is not None
                        and all(snapshot.get(key) == value for key, value in previous.items() if key != 'status')):
                    changed['reference_snapshots'][index] = {**snapshot, **latest, 'status': snapshot.get('status')}
        save(row, 'report', before, changed)
    if changes:
        session.info['knowledge_facts_catalog'] = fields
        restricted_views.remember_origins(session, fields)
    return changes


def _records(session, scope):
    mappings = []
    for module, name in (('indicators', DIMENSIONS_KEY), ('market', restricted_views.CORE_KEY)):
        if module not in scope.view_modules:
            continue
        row = session.get(SourceMapping, restricted_views.mapping_key(session, name))
        if row is not None:
            mappings.append(row)
    return {
        'mappings': mappings,
        'charts': list(session.scalars(charts.artifact_query())) if 'indicators' in scope.view_modules else [],
        'favorites': session.scalar(favorites.owned_query(scope)) if 'indicators' in scope.view_modules else None,
        'monitors': list(session.scalars(monitors._query(scope))) if 'home' in scope.view_modules else [],
        'monitor_drafts': list(session.scalars(monitors._query(scope, monitors.DRAFT_MODULE)))
            if 'home' in scope.view_modules else [],
        'judgments': _owned_judgments(session, scope) if 'judgments' in scope.view_modules else [],
        'reports': [row for row in session.scalars(charts.artifact_query('briefs').where(
            WorkbenchArtifact.actor == scope.identity.user_id))
            if _artifact_body(row).get('format') == report_drafts.FORMAT] if 'reports' in scope.view_modules else [],
    }


def _journal_id(scope, identifier, revision):
    return hashlib.sha256(json.dumps([FORMAT, scope.workbench_id, identifier, revision]).encode()).hexdigest()


def _declared_ids(records):
    identifiers = set()
    for row in records['charts']:
        for curve in _artifact_body(row).get('curves') or []:
            formula = curve.get('derived') or {}
            identifiers.update((curve.get('series_id'), formula.get('a'), formula.get('b')))
    for row in records['mappings']:
        sections = [section for dim in row.config.get('dimensions', []) for section in dim.get('sections', [])]
        identifiers.update(item['id'] for section in [row.config, *sections] for item in section.get('items', [])
                           if item['kind'] == 'indicator')
    row = records['favorites']
    if row is not None:
        identifiers.update(item['id'] for item in _artifact_body(row).get('items', []) if item['kind'] == 'indicator')
    for row in records['judgments']:
        identifiers.update(_explicit_ids({key: getattr(row, key) for key in JUDGMENT_FIELDS}))
    for row in [*records['reports'], *records['monitors'], *records['monitor_drafts']]:
        body = _artifact_body(row)
        identifiers.update(_explicit_ids(body))
        if row.module == 'briefs':
            identifiers.update(key.removeprefix('indicator:') for key in body.get('figure_conclusions', {})
                               if key.startswith('indicator:'))
    return identifiers


def _apply(session, scope, items, now, *, verify_current=False):
    records = _records(session, scope)
    identifiers = _declared_ids(records)
    affected, journal_ids = [], {}
    for item in items:
        journal_ids[_journal_id(scope, item['id'], item['revision'])] = item
    keys, saved = list(journal_ids), set()
    for offset in range(0, len(keys), 400):
        saved.update(session.scalars(select(SourceMapping.id).where(SourceMapping.id.in_(keys[offset:offset+400]))))
    if verify_current:
        expected = {item['default']['series_id']: item['default'] for key, item in journal_ids.items()
                    if key not in saved and item['personal']['series_id'] in identifiers}
        current = catalog_fields(scope, expected)
        source_keys = ('knowledge_base_id', 'knowledge_base_tenant_id', 'source_knowledge_id',
                       'source_sha256', 'source_version', 'dataset_id')
        if any(sid not in current or any(field.get(k) != current[sid].get(k) for k in source_keys)
               for sid, field in expected.items()):
            raise HTTPException(409, '其他研究台的当前原件字段已变化，请刷新后重新确认')
    for key, item in journal_ids.items():
        if key in saved:
            affected.append(item['id'])
            continue
        if item['personal']['series_id'] not in identifiers:
            continue
        changes = _rewrite_one(session, scope, item, now, records)
        if changes:
            session.add(SourceMapping(id=key, confirmed_by=scope.identity.user_id, confirmed_at=now,
                config={'format': FORMAT, 'changes': changes}))
            affected.append(item['id'])
    return affected


def _undo(session, scope, item, now):
    journal = session.get(SourceMapping, _journal_id(scope, item['id'], item['revision']))
    if journal is None:
        return
    for change in reversed(journal.config['changes']):
        kind = change['kind']
        if kind == 'mapping':
            row = session.get(SourceMapping, change['id'])
            writer = lambda row, body: _mapping_write(session, scope, row, body, now)
            current = row.config if row is not None else None
        elif kind == 'judgment':
            row = session.get(Judgment, change['id'])
            if row is not None and not record_in_workbench(session, scope, 'judgment', row.id, row):
                raise HTTPException(403, '请由原研究记录作者撤销对应')
            writer = lambda row, body: _judgment_write(session, row, body)
            current = {key: getattr(row, key) for key in JUDGMENT_FIELDS} if row is not None else None
        else:
            row = session.get(WorkbenchArtifact, change['id'])
            if row is not None and kind != 'chart' and row.actor != scope.identity.user_id:
                raise HTTPException(403, '请由原研究记录作者撤销对应')
            if row is not None and row.status == 'deleted':
                continue
            writer = lambda row, body: _artifact_write(session, scope, row, body, now)
            current = _artifact_body(row) if row is not None else None
        if row is None:
            continue
        restored = _restore(change['before'], change['after'], current)
        if kind == 'monitor_state':
            # The rule definitions are restored below, into another revision.
            monitor = session.get(WorkbenchArtifact, restored['monitor_id'])
            if monitor is not None and monitor.status != 'deleted':
                restored['monitor_revision'] = monitor.revision + 1
        if restored != current:
            writer(row, restored)
    session.delete(journal)


def _eligible(scope, field):
    return (scope.scope_version == 2 and not scope.restricted and scope.can_edit
            and any(not source['delegated'] and source['knowledge_base_id'] == field['knowledge_base_id']
                    and source['tenant_id'] == field['knowledge_base_tenant_id']
                    for source in scope.sources))


async def authorized_workbenches(scope, items, *, undo=False):
    """Use the platform's visible directory and freshly verify each grant."""
    scopes = {scope.workbench_id: (scope, items)}
    directory = (await go_request(scope, '/api/v1/workspaces/research')).get('data')
    if not isinstance(directory, list):
        raise HTTPException(503, '无法完整核验本空间研究台，未改写引用')
    previous = set(items[0].get('workbench_ids', [])) if undo else set()
    for row in directory:
        if not isinstance(row, dict) or row.get('can_edit') is not True:
            continue
        identifier = canonical_workbench_id(row.get('id'))
        if identifier in scopes or undo and previous and identifier not in previous:
            continue
        try:
            target = await resolve_workbench(scope.identity, identifier, scope.authorization)
        except HTTPException as error:
            if error.status_code in {403, 404}:
                continue
            raise
        eligible = [pair for pair in items if _eligible(target, pair.get('default') or pair['personal'])]
        if eligible:
            scopes[identifier] = (target, eligible)
    if previous - scopes.keys():
        raise HTTPException(403, '部分已改写的研究台当前无法编辑，请恢复相应工作台和来源权限后撤销')
    return list(scopes.values())


def rewrite_workbenches(scopes, *, undo=False, origin=None):
    """Stage all workbench writes; each local journal commits with its references."""
    now, affected = datetime.now(timezone.utc), defaultdict(list)
    with ExitStack() as stack:
        sessions = {}
        try:
            for scope, items in sorted(scopes, key=lambda pair: pair[0].workbench_id):
                if not scope.can_edit or scope.restricted:
                    raise HTTPException(403, '当前账号没有研究工作台编辑权限')
                marker = current_workbench.set(scope)
                try:
                    # Legacy workbenches in a space share one results database.
                    key = 'legacy' if scope.metadata.get('workspace_mode') == 'legacy' else scope.workbench_id
                    if key not in sessions:
                        sessions[key] = stack.enter_context(artifact_session(scope))
                    session = sessions[key]
                    session.info.update(workbench_id=scope.workbench_id, knowledge_base_id=scope.knowledge_base_id,
                                        knowledge_base_tenant_id=scope.knowledge_base_tenant_id)
                    if undo:
                        _undo(session, scope, items[0], now)
                    else:
                        for identifier in _apply(session, scope, items, now,
                                                 verify_current=origin is not None and origin != scope.workbench_id):
                            affected[identifier].append(scope.workbench_id)
                    session.flush()
                finally:
                    current_workbench.reset(marker)
            for session in sessions.values():
                session.commit()
        except OperationalError as error:
            raise HTTPException(409, '研究台引用写入未全部完成，请刷新后重试同一项确认或撤销') from error
    return dict(affected)

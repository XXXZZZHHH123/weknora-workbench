"""Version-pinned authored research inside one verified workbench."""
from collections.abc import Mapping
from contextlib import contextmanager
from contextvars import ContextVar
from datetime import datetime, timezone
import hashlib
import json
import re
from uuid import UUID

from fastapi import APIRouter, HTTPException, Query
from sqlalchemy import case, false, func, select
from sqlalchemy.orm import load_only

from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import Judgment, Researcher, SourceMapping, Strategy
from tin.models.workbench import WorkbenchArtifact, WorkbenchArtifactRevision

router = APIRouter(prefix='/api/sn/research-references')
KINDS = {'judgment': 'judgments', 'strategy': 'strategies'}
REGISTRY_FORMAT = 'research-record-scope.v1'
_directory_scope = ContextVar('research_reference_directory_scope', default=None)


def _get(row, name, default=None):
    return row.get(name, default) if isinstance(row, Mapping) else getattr(row, name, default)


def _registry_id(kind, identifier):
    return hashlib.sha256(json.dumps(['research-record-scope.v1', kind, str(identifier)],
        separators=(',', ':')).encode()).hexdigest()


def remember_record_scope(session, kind, identifier):
    """Called only after a server-created record has an ID, inside its transaction."""
    if kind not in {'artifact', 'judgment', 'plan'}:
        raise ValueError('Unknown research record kind')
    workbench = session.info.get('workbench_id')
    actor = session.info.get('weknora_user_id')
    if not workbench or not actor:
        return  # Standalone records retain their existing storage contract.
    values = {'format': REGISTRY_FORMAT, 'workbench_id': workbench, 'actor': str(actor),
              'kind': kind, 'record_id': str(identifier)}
    key = _registry_id(kind, identifier)
    row = session.get(SourceMapping, key)
    if row is not None:
        if row.config != values:
            raise HTTPException(403, '研究记录已归属其他工作台或账号')
        return
    session.add(SourceMapping(id=key, config=values, version=1, confirmed_by=str(actor),
                             confirmed_at=datetime.now(timezone.utc)))
    session.flush()


def _owner_id(session, scope):
    return session.scalar(select(Researcher.id).where(Researcher.weknora_user_id == scope.identity.user_id))


def _owned(session, scope, model):
    owner = _owner_id(session, scope)
    return select(model).where(model.researcher_id == owner) if owner is not None else select(model).where(false())


@contextmanager
def _directory_proofs(session, scope, groups):
    """Bounded proof metadata, scoped to this one read and always reset."""
    keys = {_registry_id(kind, row.id) for kind, rows in groups.items() for row in rows}
    registry = {}
    ordered = sorted(keys)
    for offset in range(0, len(ordered), 400):
        registry.update(session.execute(select(SourceMapping.id, SourceMapping.config)
            .where(SourceMapping.id.in_(ordered[offset:offset + 400]))).all())
    cache = {'session': session, 'workbench': scope.workbench_id, 'actor': scope.identity.user_id,
             'keys': keys, 'registry': registry, 'artifact_proofs': {}, 'visible_judgment': None}
    if scope.metadata.get('workspace_mode') == 'legacy':
        ids = [row.id for row in groups.get('artifact', []) if _registry_id('artifact', row.id) not in registry]
        if ids:
            # Evaluate only the download-URL proof inside SQLite. Large snapshot
            # contents never cross the database/Python boundary in a directory.
            item = func.json_each(WorkbenchArtifact.source_snapshots).table_valued('key', 'value', 'type')
            url = case((item.c.type == 'object', func.json_extract(item.c.value, '$.download_url')), else_=None)
            text = func.typeof(url) == 'text'
            prefix = '/research/api/workbenches/' + scope.workbench_id + '/sources/'
            any_url = select(1).select_from(item).where(text).correlate(WorkbenchArtifact).exists()
            other_url = select(1).select_from(item).where(text, ~url.startswith(prefix, autoescape=True))\
                .correlate(WorkbenchArtifact).exists()
            for offset in range(0, len(ids), 400):
                for identifier, has_url, outside in session.execute(select(WorkbenchArtifact.id, any_url, other_url)
                        .where(WorkbenchArtifact.id.in_(ids[offset:offset + 400]))):
                    cache['artifact_proofs'][identifier] = 'foreign' if outside else 'current' if has_url else 'unassigned'
        if groups.get('judgment'):
            owner = _owner_id(session, scope)
            latest_variety = session.scalar(select(Judgment.variety).where(Judgment.researcher_id == owner)
                .order_by(Judgment.created_at.desc(), Judgment.id.desc()).limit(1)) if owner is not None else None
            if latest_variety is not None:
                cache['visible_judgment'] = session.scalar(select(Judgment.id).where(Judgment.researcher_id == owner,
                    Judgment.variety == latest_variety).order_by(case((Judgment.status == '生效', 0), else_=1),
                    Judgment.version.desc()).limit(1))
    marker = _directory_scope.set(cache)
    try:
        yield
    finally:
        _directory_scope.reset(marker)


def record_scope_status(session, scope, kind, identifier, record=None):
    """Scope proof complements, rather than replaces, the caller's owner query."""
    if scope is None or session.info.get('workbench_id') != scope.workbench_id:
        return 'foreign'
    if str(session.info.get('weknora_user_id') or '') != scope.identity.user_id:
        return 'foreign'
    cache = _directory_scope.get()
    cache = cache if cache and cache['session'] is session and cache['workbench'] == scope.workbench_id \
        and cache['actor'] == scope.identity.user_id else None
    key = _registry_id(kind, identifier)
    if cache is not None and key in cache['keys']:
        saved = cache['registry'].get(key)
        present = key in cache['registry']
    else:
        marker = session.get(SourceMapping, key)
        present, saved = marker is not None, marker.config if marker is not None else None
    if present:
        return 'current' if saved == {'format': REGISTRY_FORMAT, 'workbench_id': scope.workbench_id,
            'actor': scope.identity.user_id, 'kind': kind, 'record_id': str(identifier)} else 'foreign'
    if scope.metadata.get('workspace_mode') != 'legacy':
        return 'current'
    if kind == 'artifact' and record is not None:
        if cache is not None and str(identifier) in cache['artifact_proofs']:
            return cache['artifact_proofs'][str(identifier)]
        urls = [item.get('download_url') for item in _get(record, 'source_snapshots', []) or []
                if isinstance(item, dict) and isinstance(item.get('download_url'), str)]
        if urls:
            prefix = '/research/api/workbenches/' + scope.workbench_id + '/sources/'
            return 'current' if all(url.startswith(prefix) for url in urls) else 'foreign'
    if kind == 'judgment':
        if cache is not None:
            return 'current' if str(cache['visible_judgment']) == str(identifier) else 'unassigned'
        # A legacy result DB has no workbench column. Keep its one already
        # displayed judgment available, without turning all tenant history into
        # a same-workbench reference directory. New imports have registry proof.
        newest = session.scalar(_owned(session, scope, Judgment).order_by(
            Judgment.created_at.desc(), Judgment.id.desc()).limit(1))
        if newest is not None:
            visible = session.scalar(_owned(session, scope, Judgment).where(Judgment.variety == newest.variety)
                .order_by(case((Judgment.status == '生效', 0), else_=1), Judgment.version.desc()).limit(1))
            if visible is not None and str(visible.id) == str(identifier):
                return 'current'
    return 'unassigned'


def record_in_workbench(session, scope, kind, identifier, record=None):
    return record_scope_status(session, scope, kind, identifier, record) == 'current'


def _guard(session, scope, kind):
    if kind not in KINDS:
        raise HTTPException(422, '引用类型无效')
    if scope is None:
        raise HTTPException(409, '请先进入当前研究工作台')
    if scope.restricted or KINDS[kind] not in scope.view_modules:
        raise HTTPException(403, '当前授权不开放此类个人研究引用')
    if (session.info.get('workbench_id') != scope.workbench_id
            or str(session.info.get('weknora_user_id') or '') != scope.identity.user_id):
        raise HTTPException(403, '研究引用的工作台或账号不匹配')


def _positive_id(value):
    if not isinstance(value, str) or not re.fullmatch(r'[1-9][0-9]{0,17}', value):
        raise HTTPException(404, '研究引用不存在或无权访问')
    return int(value)


def _revision(value):
    if value is not None and (type(value) is not int or value < 1):
        raise HTTPException(422, '引用版本无效')


def _text(value, limit):
    return value[:limit] if isinstance(value, str) else ''


def _judgment_metadata(row):
    contradiction = row.contradiction if isinstance(row.contradiction, dict) else {}
    return {'kind': 'judgment', 'id': str(row.id), 'revision': row.version, 'resource_type': 'judgment',
        'title': _text(contradiction.get('text'), 100) or '研判 v' + str(row.version),
        'status': row.status, 'variety': row.variety, 'written_at': row.written_at.isoformat(),
        'summary': _text(row.tone_note or contradiction.get('text'), 200),
        'origin_path': '/sn/judgment?judgment_id=' + str(row.id)}


def _artifact_metadata(row):
    try:
        body = json.loads(row.body)
    except (ValueError, TypeError):
        body = {}
    structured = isinstance(body, dict) and body.get('format') == 'research-strategy-card.v1'
    identifier = _get(row, 'id', _get(row, 'artifact_id'))
    return {'kind': 'strategy', 'id': 'research:' + identifier, 'revision': row.revision,
        'resource_type': 'research-card' if structured else 'research-note', 'title': row.title,
        'status': row.status, 'summary': _text((body.get('response_action') or body.get('trigger') or body.get('thesis')) if structured else row.body, 200),
        'can_apply': structured, 'origin_path': '/sn/strategies?selected=research:' + identifier,
        'origin_notice': '此链接打开当前记录；旧版本请使用当前引用预览核对'}


def _plan_metadata(row):
    body = row.plan if isinstance(row.plan, dict) else {}
    return {'kind': 'strategy', 'id': 'plan:' + str(row.id), 'revision': row.version,
        'resource_type': 'trade-plan', 'title': row.title, 'status': row.status, 'variety': row.variety,
        'summary': _text(body.get('thesis'), 200), 'origin_path': '/sn/strategies/' + str(row.id)}


def list_references(session, scope, kind, limit=50, offset=0):
    _guard(session, scope, kind)
    if type(limit) is not int or not 1 <= limit <= 100 or type(offset) is not int or not 0 <= offset <= 10000:
        raise HTTPException(422, '研究引用分页无效')
    # Filter before paging; legacy scope proof is deliberately checked per row.
    # The budget is explicit rather than silently dropping an unbounded history.
    candidates = []
    if kind == 'judgment':
        query = _owned(session, scope, Judgment).options(load_only(Judgment.id, Judgment.created_at,
            Judgment.contradiction, Judgment.version, Judgment.status, Judgment.variety, Judgment.written_at,
            Judgment.tone_note)).order_by(Judgment.created_at.desc(), Judgment.id.desc()).limit(1001)
        rows = list(session.scalars(query))
        if len(rows) > 1000:raise HTTPException(413, '研判目录超过1000项，请先整理历史记录')
        with _directory_proofs(session, scope, {'judgment': rows}):
            candidates = [(row.created_at, _judgment_metadata(row)) for row in rows
                          if record_in_workbench(session, scope, 'judgment', row.id, row)]
    else:
        cards = list(session.scalars(select(WorkbenchArtifact).where(WorkbenchArtifact.module == 'strategies',
            WorkbenchArtifact.actor == scope.identity.user_id).options(load_only(WorkbenchArtifact.id,
            WorkbenchArtifact.title, WorkbenchArtifact.body, WorkbenchArtifact.status, WorkbenchArtifact.revision,
            WorkbenchArtifact.updated_at)).order_by(WorkbenchArtifact.updated_at.desc(),
            WorkbenchArtifact.id).limit(1001)))
        plans = list(session.scalars(_owned(session, scope, Strategy).options(load_only(Strategy.id,
            Strategy.title, Strategy.plan, Strategy.version, Strategy.status, Strategy.variety,
            Strategy.created_at)).order_by(Strategy.created_at.desc(), Strategy.id.desc()).limit(1001)))
        if len(cards) + len(plans) > 1000:raise HTTPException(413, '策略目录超过1000项，请先整理历史记录')
        with _directory_proofs(session, scope, {'artifact': cards, 'plan': plans}):
            candidates = [(row.updated_at, _artifact_metadata(row)) for row in cards
                          if record_in_workbench(session, scope, 'artifact', row.id, row)]
            candidates += [(row.created_at, _plan_metadata(row)) for row in plans
                           if record_in_workbench(session, scope, 'plan', row.id, row)]
    candidates.sort(key=lambda item: (item[0], item[1]['id']), reverse=True)
    total = len(candidates)
    return {'items': [row for _, row in candidates[offset:offset+limit]], 'total': total,
            'offset': offset, 'truncated': offset+limit < total}


def resolve_reference(session, scope, kind, reference_id, revision=None):
    _guard(session, scope, kind);_revision(revision)
    if kind == 'judgment':
        identifier = _positive_id(reference_id)
        row = session.scalar(_owned(session, scope, Judgment).where(Judgment.id == identifier))
        if row is None or not record_in_workbench(session, scope, 'judgment', identifier, row):
            raise HTTPException(404, '研判不存在或不属于当前工作台')
        if revision is not None and revision != row.version:raise HTTPException(404, '所选研判版本不存在')
        from tin.judgments.service import _refs, to_payload
        from tin.judgments.markdown import dump_markdown
        payload = to_payload(row)
        data = payload.model_dump(mode='json', by_alias=True)
        text = dump_markdown(payload)
        return {**_judgment_metadata(row), 'payload': data, 'source_ids': [],
                'series_ids': list(dict.fromkeys(identifier for _, _, identifier in _refs(payload))),
                'body_text': text[:2000], 'body_text_truncated': len(text) > 2000,
                'summary': _text(row.tone_note, 800), 'markdown': text}
    if not isinstance(reference_id, str):raise HTTPException(404, '策略引用无效')
    if reference_id.startswith('research:'):
        identifier = reference_id.removeprefix('research:')
        try:
            if str(UUID(identifier)) != identifier:raise ValueError
        except ValueError:raise HTTPException(404, '策略引用不存在') from None
        row = session.scalar(select(WorkbenchArtifact).where(WorkbenchArtifact.id == identifier,
            WorkbenchArtifact.module == 'strategies', WorkbenchArtifact.actor == scope.identity.user_id))
        if row is None or not record_in_workbench(session, scope, 'artifact', identifier, row):
            raise HTTPException(404, '策略不存在或不属于当前工作台')
        chosen = row
        if revision is not None and revision != row.revision:
            chosen = session.get(WorkbenchArtifactRevision, (identifier, revision))
            if chosen is None:raise HTTPException(404, '所选策略版本未保留')
        from tin.web.research_strategy import ResearchStrategyCard
        try:
            card = ResearchStrategyCard.model_validate_json(chosen.body)
            data = card.model_dump(mode='json', exclude_none=True)
            labels = [('thesis', '适用范围与目标'), ('watch_for', '执行前核对'), ('trigger', '适用／触发'),
                      ('response_action', '应对措施'), ('invalidation', '暂停／撤回'), ('review_date', '复核日期')]
            text = '\n'.join(label + '：' + str(data[field]) for field, label in labels if data.get(field))
        except (ValueError, TypeError):
            data = {'format': 'research-text.v1', 'title': chosen.title, 'text': chosen.body}
            text = chosen.body
        metadata = {**_artifact_metadata(chosen), 'revision': chosen.revision, 'title': chosen.title,
                    'status': chosen.status}
        identifiers = []
        linked = data.get('judgment_reference')
        if isinstance(linked, dict) and 'judgments' in scope.view_modules:
            child = resolve_reference(session, scope, 'judgment', linked.get('id'), linked.get('revision'))
            identifiers = child['series_ids']
        return {**metadata, 'payload': data, 'source_ids': list(chosen.source_ids),
                'series_ids': identifiers, 'body_text': text[:2000], 'body_text_truncated': len(text) > 2000,
                'summary': _text(data.get('thesis') or text, 800)}
    if reference_id.startswith('plan:'):
        identifier = _positive_id(reference_id.removeprefix('plan:'))
        row = session.scalar(_owned(session, scope, Strategy).where(Strategy.id == identifier))
        if row is None or not record_in_workbench(session, scope, 'plan', identifier, row):
            raise HTTPException(404, '交易计划不存在或不属于当前工作台')
        if revision is not None and revision != row.version:
            raise HTTPException(409, '该交易计划旧版本未保留，请重新选择实际版本')
        data = row.plan if isinstance(row.plan, dict) else {}
        identifiers = [leg.get('quote_series_id') for leg in data.get('legs') or [] if isinstance(leg, dict)]
        identifiers += [item.get('series_id') for item in data.get('evidence') or [] if isinstance(item, dict)]
        text = '\n'.join(str(data.get(field) or '') for field in ['thesis', 'invalidation', 'review_at'])
        return {**_plan_metadata(row), 'payload': data, 'source_ids': [],
                'series_ids': list(dict.fromkeys(value for value in identifiers if isinstance(value, str))),
                'body_text': text[:2000], 'body_text_truncated': len(text) > 2000,
                'summary': _text(data.get('thesis'), 800)}
    raise HTTPException(404, '策略引用不存在')


@router.get('')
def reference_directory(kind: str, limit: int = Query(50, ge=1, le=100), offset: int = Query(0, ge=0, le=10000)):
    scope = current_workbench.get()
    if scope is None:raise HTTPException(409, '请先进入当前研究工作台')
    with artifact_session(scope) as session:
        return list_references(session, scope, kind, limit, offset)


@router.get('/{kind}/{reference_id}')
def reference_preview(kind: str, reference_id: str, revision: int | None = Query(None, ge=1)):
    scope = current_workbench.get()
    if scope is None:raise HTTPException(409, '请先进入当前研究工作台')
    with artifact_session(scope) as session:
        return resolve_reference(session, scope, kind, reference_id, revision)

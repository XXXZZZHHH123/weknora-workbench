"""Owner-scoped immutable diagram structures, separate from editable layouts.

Callers must freshly validate source grants and model eligibility before every
claim/reuse, and validate the graph against the current source catalog before
publishing or delivering it. This module never calls a model or reads facts.
"""
import base64
from contextlib import contextmanager
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import re
import secrets
import time

from fastapi import HTTPException
from sqlalchemy import and_, delete, or_, select, update
from sqlalchemy.exc import IntegrityError, OperationalError

from tin.integration.workbench_scope import artifact_session
from tin.models import SourceMapping

KIND = 'diagram-analysis-snapshot.v1'
PURPOSE = 'diagram'
LEASE_SECONDS = 660
MAX_BYTES = 1024 * 1024
KEY = re.compile(r'[a-f0-9]{64}\Z')


def digest(value):
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True,
        separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def _authorize(scope):
    identity = getattr(scope, 'identity', None)
    metadata = getattr(scope, 'metadata', {})
    if (identity is None or type(getattr(identity, 'tenant_id', None)) is not int
            or identity.tenant_id <= 0 or not getattr(identity, 'user_id', '')
            or not getattr(scope, 'workbench_id', '')
            or getattr(identity, 'can_write', None) is False
            or metadata.get('can_edit') is False or getattr(scope, 'can_edit', None) is False
            or metadata.get('can_read_source') is False
            or getattr(scope, 'restricted', False)
            or getattr(scope, 'source_delegations', ())
            or any(source.get('delegated') is not False for source in getattr(scope, 'sources', ()))):
        raise HTTPException(403, '当前权限不能生成或复用产业图结构')


def _scope_digest(scope):
    # No bearer or credentials enter the persisted identity.
    return digest({'tenant_id': scope.identity.tenant_id, 'user_id': scope.identity.user_id,
        'workbench_id': scope.workbench_id, 'knowledge_base_id': getattr(scope, 'knowledge_base_id', None),
        'knowledge_base_tenant_id': getattr(scope, 'knowledge_base_tenant_id', None),
        'scope_version': getattr(scope, 'scope_version', 1),
        'sources_revision': getattr(scope, 'sources_revision', 0),
        'sources': sorted(getattr(scope, 'sources', ()), key=lambda item: item['id']),
        'additional_sources': sorted(getattr(scope, 'additional_knowledge_bases', ()),
            key=lambda item: (item.get('tenant_id', 0), item.get('knowledge_base_id', ''))),
        'view_modules': sorted(getattr(scope, 'view_modules', ()))})


def image_digest(image):
    if image is None or image == '':
        return None
    if isinstance(image, bytes):
        raw = image
    elif isinstance(image, str):
        try:
            header, encoded = image.split(',', 1)
            if header not in {'data:image/png;base64', 'data:image/jpeg;base64',
                    'data:image/webp;base64', 'data:image/gif;base64'}:
                raise ValueError
            raw = base64.b64decode(encoded, validate=True)
        except (ValueError, TypeError):
            raise HTTPException(422, '产业图图片无法核验，请重新选择') from None
    else:
        raise HTTPException(422, '产业图图片无法核验，请重新选择')
    if not raw or len(raw) > 5 * 1024 * 1024:
        raise HTTPException(422, '产业图图片无法核验，请重新选择')
    return hashlib.sha256(raw).hexdigest()


def identity(scope, *, instructions, image, source_identity, catalog_version, catalog, model, protocol):
    """Fingerprint exact input and stable-ID catalog; never store model secrets."""
    _authorize(scope)
    if not isinstance(instructions, str) or not isinstance(catalog_version, str) or not catalog_version:
        raise HTTPException(422, '产业图资料或目录身份不完整')
    if not model or not protocol or not isinstance(catalog, list):
        raise HTTPException(422, '产业图模型或协议身份不完整')
    ids = [row.get('id') for row in catalog if isinstance(row, dict)]
    if len(ids) != len(catalog) or any(not isinstance(item, str) or not item for item in ids) or len(set(ids)) != len(ids):
        raise HTTPException(422, '产业图指标目录身份不完整')
    return digest([KIND, PURPOSE, _scope_digest(scope), hashlib.sha256(instructions.encode()).hexdigest(),
        image_digest(image), digest(source_identity), catalog_version,
        sorted(catalog, key=lambda row: row['id']), digest(model), protocol])


def _key(key):
    if not isinstance(key, str) or not KEY.fullmatch(key):
        raise HTTPException(422, '产业图结构记录身份无效')


def _owner_filters(scope):
    return (SourceMapping.confirmed_by == scope.identity.user_id,
        SourceMapping.config['kind'].as_string() == KIND,
        SourceMapping.config['purpose'].as_string() == PURPOSE,
        SourceMapping.config['tenant_id'].as_integer() == scope.identity.tenant_id,
        SourceMapping.config['user_id'].as_string() == scope.identity.user_id,
        SourceMapping.config['workbench_id'].as_string() == scope.workbench_id)


def _owned(row, scope, key):
    value = row.config if row is not None else None
    if (not isinstance(value, dict) or type(row.version) is not int or row.version < 1
            or row.confirmed_by != scope.identity.user_id or value.get('kind') != KIND
            or value.get('purpose') != PURPOSE or value.get('key') != key
            or not isinstance(value.get('record_token'), str) or len(value['record_token']) != 32
            or value.get('tenant_id') != scope.identity.tenant_id
            or value.get('user_id') != scope.identity.user_id
            or value.get('workbench_id') != scope.workbench_id
            or value.get('scope_digest') != _scope_digest(scope)):
        raise HTTPException(409, '已存产业图结构无法核验，请重新生成')
    return value


def _snapshot(snapshot):
    if (not isinstance(snapshot, dict) or set(snapshot) != {'graph', 'markdown', 'structure', 'metadata'}
            or not isinstance(snapshot['graph'], dict) or not isinstance(snapshot['markdown'], str)
            or not isinstance(snapshot['structure'], dict) or not snapshot['structure']
            or not isinstance(snapshot['metadata'], dict)):
        raise HTTPException(422, '产业图结构尚未完整校验，未保存复用结果')
    metadata = snapshot['metadata']
    candidates = metadata.get('candidate_ids')
    if (not isinstance(candidates, list) or any(not isinstance(value, str) or not value for value in candidates)
            or len(set(candidates)) != len(candidates) or type(metadata.get('candidate_subset')) is not bool
            or type(metadata.get('candidate_count')) is not int or metadata['candidate_count'] != len(candidates)
            or not isinstance(metadata.get('model_id'), str) or not metadata['model_id']
            or not isinstance(metadata.get('prompt_version'), str) or not metadata['prompt_version']
            or type(metadata.get('repair_attempts')) is not int or metadata['repair_attempts'] < 0):
        raise HTTPException(422, '产业图结构来源记录不完整，未保存复用结果')
    from tin.web.diagram_draft import compile_graph
    if compile_graph(snapshot['graph']) != snapshot['markdown']:
        raise HTTPException(422, '产业图结构与配置不一致，未保存复用结果')
    try:
        encoded = json.dumps(snapshot, ensure_ascii=False, sort_keys=True, separators=(',', ':'), allow_nan=False)
    except (ValueError, TypeError, RecursionError):
        raise HTTPException(422, '产业图结构包含无效内容，未保存复用结果') from None
    if len(encoded.encode()) > MAX_BYTES:
        raise HTTPException(413, '产业图结构超过暂存容量，请缩小范围')
    return digest(snapshot)


def _read_snapshot(value):
    snapshot = value.get('snapshot')
    try:
        valid = isinstance(snapshot, dict) and value.get('snapshot_sha256') == _snapshot(snapshot)
    except HTTPException:
        valid = False
    if not valid:
        raise HTTPException(409, '已存产业图结构内容无法核验，请重新生成')
    return deepcopy(snapshot)


@contextmanager
def storage(scope):
    _authorize(scope)
    with artifact_session(scope) as session:
        try:
            yield session
        except (IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, '相同资料的产业图正在更新，请稍后重试') from None


def _write(session, row, config):
    changed = session.execute(update(SourceMapping).where(SourceMapping.id == row.id,
        SourceMapping.version == row.version,
        SourceMapping.config['record_token'].as_string() == row.config['record_token']).values(config=config, version=row.version + 1,
            confirmed_at=datetime.now(timezone.utc)), execution_options={'synchronize_session': False})
    if changed.rowcount != 1:
        raise HTTPException(409, '产业图结构记录正在更新，请稍后重试')


def _restore(value):
    previous = value.get('previous_ready')
    if previous is None:
        return None
    # Validate a previous success before restoring it after a failed refresh.
    _read_snapshot(previous)
    return {key: item for key, item in value.items() if key not in {
        'fence', 'lease_until', 'previous_ready', 'created_order'}} | previous | {'state': 'ready'}


def _cleanup_expired(session, scope, now, keep_key):
    rows = session.execute(select(SourceMapping).where(*_owner_filters(scope), SourceMapping.id != keep_key,
        SourceMapping.config['lease_until'].as_float() <= now,
        or_(SourceMapping.config['state'].as_string() == 'pending',
            and_(SourceMapping.config['state'].as_string() == 'ready',
                SourceMapping.config['delivery_pending'].as_boolean().is_(True))))).scalars().all()
    for row in rows:
        if row.config.get('scope_digest') != _scope_digest(scope):
            # Old grants cannot be restored or returned under a new grant.
            session.execute(delete(SourceMapping).where(SourceMapping.id == row.id,
                SourceMapping.version == row.version,
                SourceMapping.config['record_token'].as_string() == row.config.get('record_token'),
                *_owner_filters(scope)))
            continue
        value = _owned(row, scope, row.id)
        restored = _restore(value)
        if restored is None:
            session.execute(delete(SourceMapping).where(SourceMapping.id == row.id,
                SourceMapping.version == row.version,
                SourceMapping.config['record_token'].as_string() == row.config.get('record_token'),
                *_owner_filters(scope)))
        else:
            _write(session, row, restored)
    session.expire_all()


def claim(scope, key, refresh=False):
    _key(key)
    if type(refresh) is not bool:
        raise HTTPException(422, '产业图重新生成参数无效')
    now, fence = time.time(), secrets.token_hex(16)
    with storage(scope) as session:
        _cleanup_expired(session, scope, now, key)
        row = session.get(SourceMapping, key)
        old = _owned(row, scope, key) if row is not None else None
        recovered = False
        if old and (old.get('state') == 'pending' or old.get('delivery_pending') is True):
            if type(old.get('lease_until')) not in (int, float):
                raise HTTPException(409, '产业图结构租约无法核验，请稍后重试')
            if old['lease_until'] <= now:
                old, recovered = _restore(old), True
            elif old.get('state') == 'pending':
                raise HTTPException(409, '相同资料正在生成产业图，请等待完成')
        if old and old.get('state') == 'ready' and not refresh:
            snapshot = _read_snapshot(old)
            # Expired refresh restores a persisted success, including across restart.
            if recovered:
                _write(session, row, old)
            session.commit()
            return {'key': key, 'snapshot': snapshot}
        if old is not None and old.get('state') != 'ready':
            raise HTTPException(409, '已存产业图结构状态无法核验')
        value = {'kind': KIND, 'purpose': PURPOSE, 'key': key,
            'tenant_id': scope.identity.tenant_id, 'user_id': scope.identity.user_id,
            'workbench_id': scope.workbench_id, 'scope_digest': _scope_digest(scope),
            'state': 'pending', 'fence': fence, 'record_token': secrets.token_hex(16),
            'lease_until': now + LEASE_SECONDS,
            'created_order': time.time_ns()}
        if old:
            _read_snapshot(old)
            value['previous_ready'] = {name: deepcopy(old[name]) for name in (
                'snapshot', 'snapshot_sha256', 'delivery_pending', 'created_order')}
        if row is None:
            session.add(SourceMapping(id=key, config=value, version=1,
                confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        else:
            _write(session, row, value)
        session.commit()
    return {'key': key, 'fence': fence}


def publish(scope, ticket, snapshot):
    _key(ticket.get('key'))
    checksum = _snapshot(snapshot)
    with storage(scope) as session:
        row = session.get(SourceMapping, ticket['key'])
        value = _owned(row, scope, ticket['key'])
        if (value.get('state') != 'pending' or not ticket.get('fence')
                or value.get('fence') != ticket['fence'] or value.get('lease_until', 0) <= time.time()):
            raise HTTPException(409, '产业图生成任务已中断，请重新打开')
        config = dict(value)
        config.update(state='ready', snapshot=deepcopy(snapshot), snapshot_sha256=checksum, delivery_pending=True)
        _write(session, row, config)
        session.commit()


def abandon(scope, ticket):
    if not ticket.get('fence'):
        return
    _key(ticket.get('key'))
    with storage(scope) as session:
        row = session.get(SourceMapping, ticket['key'])
        if row is None:
            return
        value = _owned(row, scope, ticket['key'])
        if (value.get('fence') != ticket['fence'] or value.get('state') not in {'pending', 'ready'}
                or value.get('state') == 'ready' and value.get('delivery_pending') is not True):
            return
        restored = _restore(value)
        if restored is None:
            session.execute(delete(SourceMapping).where(SourceMapping.id == row.id,
                SourceMapping.version == row.version,
                SourceMapping.config['record_token'].as_string() == row.config.get('record_token'),
                *_owner_filters(scope)))
        else:
            _write(session, row, restored)
        session.commit()


def prune_after_success(scope, keep_key):
    """Only a delivered success retires older successes; never evict pending work."""
    _key(keep_key)
    with storage(scope) as session:
        row = session.get(SourceMapping, keep_key)
        value = _owned(row, scope, keep_key)
        if value.get('state') != 'ready':
            raise HTTPException(409, '产业图结构尚未完成，未替换上次结果')
        _read_snapshot(value)
        order = value.get('created_order')
        if type(order) is not int:
            raise HTTPException(409, '产业图结构记录顺序无法核验')
        delivered = {name: item for name, item in value.items() if name not in {'previous_ready', 'lease_until', 'fence'}}
        _write(session, row, {**delivered, 'delivery_pending': False})
        session.execute(delete(SourceMapping).where(*_owner_filters(scope), SourceMapping.id != keep_key,
            SourceMapping.config['state'].as_string() == 'ready',
            SourceMapping.config['delivery_pending'].as_boolean().is_(False),
            SourceMapping.config['created_order'].as_integer() < order))
        # An older request completing late cannot displace a newer delivered result.
        newer = session.execute(select(SourceMapping.id).where(*_owner_filters(scope),
            SourceMapping.config['state'].as_string() == 'ready',
            SourceMapping.config['delivery_pending'].as_boolean().is_(False),
            SourceMapping.config['created_order'].as_integer() > order)).first()
        if newer is not None:
            session.execute(delete(SourceMapping).where(SourceMapping.id == keep_key, *_owner_filters(scope)))
        session.commit()


mark_delivered = prune_after_success

"""Immutable, owner-scoped model analyses; human review is always a new draft."""
from copy import deepcopy
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import json
import secrets
import time

from fastapi import HTTPException
from sqlalchemy import delete, update
from sqlalchemy.exc import IntegrityError, OperationalError

from tin.models import SourceMapping

KIND = 'judgment-analysis-snapshot.v1'
MAX_BYTES = 1024 * 1024
LEASE_SECONDS = 600


def digest(value):
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True,
        separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def identity(scope, body, before, model, protocol):
    from tin.web.judgment_generation import scope_key
    return digest([KIND, scope_key(scope), scope.additional_knowledge_bases,
        scope.source_delegations, before['variety'], model, protocol,
        hashlib.sha256(body.text.encode('utf-8')).hexdigest(), before['fact_version'],
        sorted(before['catalog'], key=lambda row: row['id'])])


def _owned(row, scope, key):
    value = row.config if row is not None else {}
    if (not isinstance(value, dict) or type(row.version) is not int or row.version < 1
            or row.confirmed_by != scope.identity.user_id
            or value.get('kind') != KIND or value.get('key') != key
            or value.get('tenant_id') != scope.identity.tenant_id
            or value.get('user_id') != scope.identity.user_id
            or value.get('workbench_id') != scope.workbench_id):
        raise HTTPException(409, '已存解析记录无法核验，原有研判保持不变')
    return value


@contextmanager
def storage(scope):
    from tin.web.judgment_generation import result_session
    with result_session(scope) as session:
        try:
            yield session
        except (IntegrityError, OperationalError):
            session.rollback()
            raise HTTPException(409, '相同原文的解析记录正在更新，请稍后重试') from None


def claim(scope, key):
    now = time.time()
    fence = secrets.token_hex(16)
    value = {'kind': KIND, 'key': key, 'tenant_id': scope.identity.tenant_id,
        'user_id': scope.identity.user_id, 'workbench_id': scope.workbench_id,
        'state': 'pending', 'fence': fence, 'lease_until': now + LEASE_SECONDS}
    with storage(scope) as session:
        row = session.get(SourceMapping, key)
        if row is not None:
            old = _owned(row, scope, key)
            if old.get('state') == 'ready':
                snapshot = old.get('snapshot')
                if not isinstance(snapshot, dict) or old.get('snapshot_sha256') != digest(snapshot):
                    raise HTTPException(409, '已存解析内容无法核验，原有研判保持不变')
                return {'key': key, 'snapshot': deepcopy(snapshot)}
            if old.get('state') != 'pending' or old.get('lease_until', now + 1) > now:
                raise HTTPException(409, '相同原文正在分析，请等待该任务完成后重试')
            changed = session.execute(update(SourceMapping).where(SourceMapping.id == key,
                SourceMapping.version == row.version).values(config=value, version=row.version + 1,
                    confirmed_at=datetime.now(timezone.utc)))
            if changed.rowcount != 1:
                raise HTTPException(409, '相同原文正在分析，请稍后重试')
        else:
            session.add(SourceMapping(id=key, config=value, version=1,
                confirmed_by=scope.identity.user_id, confirmed_at=datetime.now(timezone.utc)))
        session.commit()
    return {'key': key, 'fence': fence}


def publish(scope, ticket, snapshot):
    if len(json.dumps(snapshot, ensure_ascii=False, allow_nan=False).encode()) > MAX_BYTES:
        raise HTTPException(413, '解析结果超过保存容量，请缩小原文范围；现有研判保持不变')
    with storage(scope) as session:
        row = session.get(SourceMapping, ticket['key'])
        if row is None:
            raise HTTPException(409, '解析任务已中断，请重新分析')
        value = _owned(row, scope, ticket['key'])
        if (value.get('state') != 'pending' or value.get('fence') != ticket.get('fence')
                or value.get('lease_until', 0) <= time.time()):
            raise HTTPException(409, '解析任务已中断，请重新分析')
        config = {**value, 'state': 'ready', 'snapshot': deepcopy(snapshot),
            'snapshot_sha256': digest(snapshot)}
        changed = session.execute(update(SourceMapping).where(SourceMapping.id == row.id,
            SourceMapping.version == row.version).values(config=config, version=row.version + 1,
                confirmed_at=datetime.now(timezone.utc)))
        if changed.rowcount != 1:
            raise HTTPException(409, '解析结果已更新，请重新打开')
        session.commit()


def abandon(scope, ticket):
    if 'fence' not in ticket:
        return
    with storage(scope) as session:
        session.execute(delete(SourceMapping).where(SourceMapping.id == ticket['key'],
            SourceMapping.confirmed_by == scope.identity.user_id,
            SourceMapping.config['kind'].as_string() == KIND,
            SourceMapping.config['state'].as_string() == 'pending',
            SourceMapping.config['fence'].as_string() == ticket['fence']))
        session.commit()

"""Read PPT originals through the caller's existing, workbench-scoped KB ACLs.

The request middleware freshly verifies ``scope.sources``.  This module never
lists a tenant's other libraries, uses a saved inventory, or retains a download.
"""
import hashlib
import re
from urllib.parse import quote

import httpx
from fastapi import HTTPException

from tin.config import settings
from tin.ppt.package import MAX_UPLOAD

MAX_INVENTORY = 10000
FINGERPRINT = re.compile(r'(?:[0-9a-fA-F]{32}|[0-9a-fA-F]{64})\Z')


def _headers(scope):
    if not isinstance(scope.authorization, str) or not scope.authorization.startswith('Bearer '):
        raise HTTPException(401, '请重新从主平台打开研究台以核验知识库权限')
    if not settings.weknora_api_url:
        raise HTTPException(503, '知识库PPT入口需要连接WeKnora')
    return {'Authorization': scope.authorization, 'X-Tenant-ID': str(scope.identity.tenant_id)}


def _sources(scope):
    _headers(scope)
    if scope.scope_version != 2:
        raise HTTPException(409, '知识库PPT需要来源版本2，请重新打开研究台')
    if scope.restricted:
        raise HTTPException(403, '受限视角暂不开放PPT原件')
    # A central dataset's backing library is not an original-document grant.
    return {source['knowledge_base_id']: source for source in scope.sources
            if source.get('source_type') == 'knowledge_base' and not source.get('delegated')}


def _source(scope, knowledge_base_id):
    if (not isinstance(knowledge_base_id, str) or not knowledge_base_id
            or len(knowledge_base_id) > 128 or knowledge_base_id not in _sources(scope)):
        raise HTTPException(403, '只能使用当前研究台已授权来源知识库的PPT原件')


def _changed():
    return HTTPException(409, {'code': 'source_changed',
        'message': '知识库原件已变化，请重新选择并确认PPT版式后导出'})


def _deleted():
    return HTTPException(409, {'code': 'source_deleted',
        'message': '知识库原件已删除，请重新选择PPT原件后导出'})


def _inventory(scope, knowledge_base_id):
    """Return a complete current inventory, failing closed on inconsistent pages."""
    _source(scope, knowledge_base_id)
    endpoint = (settings.weknora_api_url.rstrip('/') + '/api/v1/knowledge-bases/'
                + quote(knowledge_base_id, safe='') + '/knowledge')
    try:
        rows, seen, expected_total = [], set(), None
        page, page_size = 1, 200
        while True:
            response = httpx.request('GET', endpoint, params={'page': page, 'page_size': page_size},
                headers=_headers(scope), trust_env=False, follow_redirects=False, timeout=15)
            if response.status_code in {401, 403, 404}:
                raise HTTPException(response.status_code, '知识库来源授权已失效，请重新打开研究台')
            response.raise_for_status()
            document = response.json()
            if not isinstance(document, dict):
                raise ValueError('Invalid inventory envelope')
            batch, total = document.get('data'), document.get('total')
            if (document.get('success') is not True or not isinstance(batch, list)
                    or type(total) is not int or total < 0):
                raise ValueError('Invalid inventory')
            if total > MAX_INVENTORY:
                raise HTTPException(503, '知识库超过10000条目录核验上限，未返回部分PPT清单')
            if expected_total is None:
                expected_total = total
            elif total != expected_total:
                raise ValueError('Inventory changed during pagination')
            if (page != 1 or total != len(batch)) and not {'page', 'page_size'} <= document.keys():
                raise ValueError('Missing pagination metadata')
            actual_page, actual_size = document.get('page', page), document.get('page_size', page_size)
            if (type(actual_page) is not int or actual_page != page
                    or type(actual_size) is not int or not 1 <= actual_size <= 200
                    or (page > 1 and actual_size != page_size)):
                raise ValueError('Invalid pagination')
            page_size = actual_size
            if len(batch) != min(page_size, total - len(seen)):
                raise ValueError('Incomplete inventory page')
            for row in batch:
                if (not isinstance(row, dict) or row.get('knowledge_base_id') != knowledge_base_id
                        or not isinstance(row.get('id'), str) or not 1 <= len(row['id']) <= 128
                        or row['id'] in seen):
                    raise ValueError('Invalid inventory scope')
                seen.add(row['id'])
                if row.get('parse_status') not in {'deleting', 'deleted'} and not row.get('deleted_at'):
                    rows.append(row)
            if len(seen) == total:
                return rows
            page += 1
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, KeyError):
        raise HTTPException(503, '暂时无法核验知识库PPT清单，请稍后重试') from None


def source_inventory(scope):
    """One request-local inventory for checking many template references."""
    return {(knowledge_base_id, row['id']): row for knowledge_base_id in _sources(scope)
            for row in _inventory(scope, knowledge_base_id)}


def _is_ppt(row):
    kind = str(row.get('file_type') or '').lower().lstrip('.')
    filename = str(row.get('file_name') or row.get('title') or '')
    return kind == 'pptx' or (not kind and filename.lower().endswith('.pptx'))


def _fingerprint(value):
    if not isinstance(value, str) or not FINGERPRINT.fullmatch(value):
        raise HTTPException(503, '知识库PPT原件缺少可核验的文件哈希，请在知识库核对原件')
    return value.lower()


def _metadata(row):
    metadata = row.get('metadata')
    metadata = metadata if isinstance(metadata, dict) else {}
    slide_count = next((value for value in (row.get('slide_count'), row.get('page_count'),
        metadata.get('slide_count'), metadata.get('page_count')) if type(value) is int and value > 0), None)
    size = row.get('file_size')
    return {'knowledge_base_id': row['knowledge_base_id'], 'knowledge_id': row['id'],
        'file_hash': _fingerprint(row.get('file_hash')),
        'filename': str(row.get('file_name') or row.get('title') or 'PPT版式.pptx'),
        'created_at': row.get('created_at'), 'size': size if type(size) is int and size >= 0 else None,
        'slide_count': slide_count}


def list_ppt_sources(scope, search='', offset=0, limit=20):
    needle = search.strip().casefold()
    items = [_metadata(row) for row in source_inventory(scope).values()
             if _is_ppt(row) and (not needle or needle in str(row.get('file_name') or row.get('title') or '').casefold())]
    items.sort(key=lambda row: (str(row['created_at'] or ''), row['knowledge_base_id'], row['knowledge_id']), reverse=True)
    total = len(items)
    selected = items[offset:offset + limit]
    return {'items': selected, 'total': total, 'offset': offset,
            'next_offset': offset + len(selected) if offset + len(selected) < total else None}


def _find(scope, knowledge_base_id, knowledge_id, *, inventory=None):
    _source(scope, knowledge_base_id)
    if not isinstance(knowledge_id, str) or not 1 <= len(knowledge_id) <= 128:
        raise HTTPException(422, '知识库文件编号无效')
    if inventory is not None:
        row = inventory.get((knowledge_base_id, knowledge_id))
    else:
        row = next((item for item in _inventory(scope, knowledge_base_id) if item['id'] == knowledge_id), None)
    if row is None:
        raise _deleted()
    return row


def get_ppt_source(scope, knowledge_base_id, knowledge_id):
    row = _find(scope, knowledge_base_id, knowledge_id)
    if not _is_ppt(row):
        raise HTTPException(422, '所选知识库文件不是PPTX')
    return _metadata(row)


def _check_reference(scope, reference, *, inventory=None):
    if not isinstance(reference, dict):
        raise HTTPException(409, 'PPT原件引用无效，请重新选择原件')
    row = _find(scope, reference.get('knowledge_base_id'), reference.get('knowledge_id'), inventory=inventory)
    expected = reference.get('file_hash')
    if (not isinstance(expected, str) or not FINGERPRINT.fullmatch(expected)
            or not _is_ppt(row) or _fingerprint(row.get('file_hash')) != expected.lower()):
        raise _changed()
    return _metadata(row)


def source_state(scope, reference, *, inventory=None):
    try:
        _check_reference(scope, reference, inventory=inventory)
        return {'status': 'ready', 'message': '知识库原件可用'}
    except HTTPException as error:
        if isinstance(error.detail, dict) and error.detail.get('code') in {'source_deleted', 'source_changed'}:
            return {'status': error.detail['code'].removeprefix('source_'), 'message': error.detail['message']}
        status = 'forbidden' if error.status_code in {401, 403} else 'deleted' if error.status_code == 404 else 'unavailable'
        return {'status': status, 'message': str(error.detail)}


def read_ppt_source(scope, reference):
    """Revalidate both registered and downloaded hashes without a local copy."""
    current = _check_reference(scope, reference)
    if current['size'] is not None and current['size'] > MAX_UPLOAD:
        raise HTTPException(413, 'PPT原件不能超过60MB')
    endpoint = (settings.weknora_api_url.rstrip('/') + '/api/v1/knowledge/'
                + quote(current['knowledge_id'], safe='') + '/download')
    try:
        with httpx.stream('GET', endpoint, headers=_headers(scope),
                          trust_env=False, follow_redirects=False, timeout=90) as response:
            if response.status_code in {401, 403}:
                raise HTTPException(response.status_code, '当前账号没有知识库PPT原件读取权限')
            if response.status_code == 404:
                raise _deleted()
            if response.status_code != 200:
                raise HTTPException(503, '知识库PPT原件暂时不可读，请稍后重试')
            pieces, size = [], 0
            for piece in response.iter_bytes():
                size += len(piece)
                if size > MAX_UPLOAD:
                    raise HTTPException(413, 'PPT原件不能超过60MB')
                pieces.append(piece)
        raw = b''.join(pieces)
    except httpx.HTTPError:
        raise HTTPException(503, '知识库PPT原件暂时不可读，请稍后重试') from None
    digest = hashlib.md5(raw).hexdigest() if len(current['file_hash']) == 32 else hashlib.sha256(raw).hexdigest()
    if digest != current['file_hash']:
        raise _changed()
    return raw

"""Read-only catalogue browsing across the current verified workspace.

The caller supplies the request-scoped store, never a tenant or knowledge-base
identifier. Entries retain their exact snapshot so identical series IDs in two
files cannot silently query the other file. This is catalogue recall, not an
intent resolver or a cross-file calculation.
"""


def _scope(store, search, sheet, kind):
    current = [item for item in store.list_datasets()
               if item['dataset_kind'] == 'business' and item['is_latest_for_source']]
    scopes = []
    for item in current:
        arguments = dict(search=search, sheet=sheet, limit=1, offset=0)
        page = getattr(store, 'list_' + kind)(item['dataset_id'], **arguments)
        scopes.append((item, page))
    return scopes


def _page(store, scopes, kind, search, sheet, limit, offset):
    rows = []
    remaining_offset = offset
    for item, first in scopes:
        total = first['total']
        if remaining_offset >= total:
            remaining_offset -= total
            continue
        take = min(limit - len(rows), total - remaining_offset)
        if take <= 0:
            break
        page = first if remaining_offset == 0 and take == 1 else getattr(store, 'list_' + kind)(
            item['dataset_id'], search=search, sheet=sheet, limit=take, offset=remaining_offset)
        rows.extend({**entry, 'dataset_id': item['dataset_id'], 'source_file': item['source_file']}
                    for entry in page[kind])
        remaining_offset = 0
        if len(rows) == limit:
            break
    total = sum(page['total'] for _, page in scopes)
    return {kind: rows, 'total': total, 'offset': offset,
            'next_offset': offset + limit if total > offset + limit else None,
            'truncated': total > offset + limit,
            'dataset_count': len(scopes), 'scope': 'current_business_files'}


def _validate(store, limit, offset):
    store._limit(limit)
    if type(offset) is not int or offset < 0:
        raise ValueError('offset 必须为非负整数。')


def list_series(store, search='', limit=100, offset=0, sheet=None):
    _validate(store, limit, offset)
    scopes = _scope(store, search, sheet, 'series')
    # A partial match in one file must not displace an actual all-term/alias
    # match elsewhere. These remain labelled catalogue candidates, never a
    # business decision about which field answers a user's question.
    strongest = [scope for scope in scopes
                 if scope[1]['total'] and scope[1].get('match_mode') in {'all_terms', 'alias_candidates'}]
    matched = strongest or [scope for scope in scopes if scope[1]['total']]
    result = _page(store, matched, 'series', search, sheet, limit, offset)
    result['dataset_count'] = len(scopes)
    result['match_mode'] = ('alias_candidates' if any(page.get('match_mode') == 'alias_candidates'
                           for _, page in matched) else 'all_terms') if strongest else (
                               'partial_candidates' if matched else 'no_matches')
    result['pending_sheets'] = [{**pending, 'dataset_id': item['dataset_id'], 'source_file': item['source_file']}
                                for item, page in scopes for pending in page.get('pending_sheets', [])]
    result['notice'] = ('跨当前范围内各业务文件的最新导入结果搜索；不含历史版本或验收样例。'
                        '每个指标保留独立文件来源，选择后按该真实版本查询；候选匹配不决定业务口径。')
    return result


def list_charts(store, sheet=None, search='', limit=50, offset=0):
    _validate(store, limit, offset)
    scopes = _scope(store, search, sheet, 'charts')
    result = _page(store, scopes, 'charts', search, sheet, limit, offset)
    result['sheets'] = sorted({name for _, page in scopes for name in page['sheets']})
    result['notice'] = '当前范围内各业务文件最新版本的原生图表；每条曲线按各自原文件查询。'
    return result

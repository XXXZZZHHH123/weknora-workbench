"""Synthetic end-to-end equivalence and cold-request benchmarks.

Frozen functions below come from 45e53c16. They are isolated test references,
not runtime fallbacks, and remain reproducible after HEAD changes. Cycle
references live in tests.cycles_reference. All HTTP is intercepted in-process;
no sockets or running workbench services are used. Real httpx client/TLS-root
initialization, request signing, JSON, SQLite projection and routing are timed.

Run with pytest -q -s tests/test_chart_anomalies_performance.py. Timing is
reported, not asserted: machines and CI scheduling differ. The second case adds
5ms per synthetic upstream call; it does not model production server workload.
"""
from copy import deepcopy
from datetime import date, timedelta
import base64
import json
import math
from time import perf_counter, sleep
from types import SimpleNamespace

import httpx
import pytest

from tin.config import settings
from tin.integration import kb_facts, page_facts
from tin.integration.workbench_scope import artifact_session
from tin.provenance import bridge
from tin.schemas.chart import ChartDefinition
from tin.web import charts, chart_data
from tests import cycles_reference
from tests.chart_fixtures import definition
from tests.test_chart_api import chart_client

CURRENT_REQUEST = bridge.structured_request
ORIGINAL_CLIENT = httpx.Client
CURRENT_PROJECTION = kb_facts.install_projection
CURRENT_CYCLES = charts.compute_cycles
CURRENT_MULTI_CYCLES = charts.compute_multi_cycles

CHARTS_REFERENCE = r'''
def chart_anomalies(date: str | None = None, limit: int | None = Query(None, ge=1)):
    """Compare every saved cycle chart; limit applies after the global sort."""
    scope = require_scope()
    day = cutoff(date)
    grouped, items, failed, compound = {}, [], [], []

    def failure(row, error):
        reason = error.detail if isinstance(error, HTTPException) else str(error)
        failed.append({'id': row.id, 'title': row.title, 'reason': reason or '图表读取或计算失败'})

    with artifact_session(scope) as session:
        rows = session.scalars(artifact_query().order_by(WorkbenchArtifact.id)).all()
        for row in rows:
            try:
                definition = decode_definition(row)
                if definition.time.mode == 'cycle':
                    if len(definition.curves) == 1 and definition.curves[0].series_id:
                        grouped.setdefault(definition.curves[0].series_id, []).append((row, definition))
                    else:
                        compound.append((row, definition))
            except Exception as error:
                failure(row, error)

    def read_batch(identifiers):
        try:
            fields = catalog_fields(scope, identifiers)
            data = read_series([sid for sid in identifiers if sid in fields], day, limit=2000)
        except Exception as error:
            # Authorization and source changes must still reject the read. Do
            # not retry them against a new grant or snapshot within this request.
            if isinstance(error, HTTPException) and error.status_code in {401, 403, 409}:
                raise
            if len(identifiers) > 1:
                middle = len(identifiers) // 2
                read_batch(identifiers[:middle])
                read_batch(identifiers[middle:])
            else:
                for row, _ in grouped[identifiers[0]]:
                    failure(row, error)
            return
        for sid in identifiers:
            for row, definition in grouped[sid]:
                try:
                    field, series = fields.get(sid), data.get(sid)
                    if field is None:
                        raise HTTPException(404, '指标已不在当前来源目录')
                    if series is None:
                        raise HTTPException(404, '指标观测不可读')
                    if (field.get('unit') or '').strip() and field['unit'].strip() != series['unit'].strip():
                        raise HTTPException(409, '图表单位在目录与观测读取之间已变化，请刷新核对')
                    item = anomaly_item(row, definition, field, series, day)
                    if item is not None:
                        items.append(item)
                except Exception as error:
                    failure(row, error)

    identifiers = list(grouped)
    # Retain the chart data window: 2000 points per metric, at most 256 metrics
    # and 20000 points per page projection. Shared metrics are fetched once.
    batch_size = min(256, 20000 // 2000)
    for offset in range(0, len(identifiers), batch_size):
        read_batch(identifiers[offset:offset + batch_size])
    for row, definition in compound:
        try:
            fields = catalog_fields(scope, definition_series_ids(definition))
            identifiers = definition_series_ids(definition)
            point_limit = min(2000, 20000 // max(len(definition.curves), len(identifiers)))
            # Share the resolved observations with current and preceding-date
            # comparisons instead of persisting a derived result anywhere.
            raw = read_series([sid for sid in identifiers if sid in fields], day, limit=point_limit)
            payload = definition_data(scope, definition, day, fields=fields, data=raw, point_limit=point_limit)
            curves = resolve_curves(definition, fields, raw)
            current = [item['cycle'] for item in payload['curves']] if len(curves) > 1 else [payload]
            for index, (curve, series, cycle) in enumerate(zip(definition.curves, curves, current)):
                if series['status'] == 'missing':
                    raise HTTPException(404, '指标已不在当前来源目录')
                field = fields.get(curve.series_id, {})
                item = anomaly_item(row, definition, field, series, day, curve=curve, current=cycle)
                if item is not None:
                    item['curve_index'] = index
                    items.append(item)
        except Exception as error:
            if isinstance(error, HTTPException) and error.status_code in {401, 403, 409}:
                raise
            failure(row, error)
    items.sort(key=lambda item: (not any(reason in {'extreme_high', 'extreme_low'} for reason in item['reasons']),
                                -abs(item['rank_change'] or 0), item['title'], item['id']))
    failed.sort(key=lambda item: item['id'])
    return {'as_of': day.isoformat(), 'items': items[:limit], 'total': len(items), 'failed': failed}


def anomaly_item(row, definition, field, series, day, *, curve=None, current=None):
    curve = curve or definition.curves[0]
    time = definition.time
    template = time.template.model_dump(mode='json', exclude_none=True)
    template['transform'] = time.transform
    current = current or cycle_result(series, template, day, band_cycles=time.band_cycles, cycles=time.cycles)
    position = current['position']
    if position is None or position['n'] < 3 or position.get('rank') is None:
        return None
    previous_days = [point['date'] for point in series['points']
                     if point['value'] is not None and point['date'] < position['date']]
    previous = (cycle_result(series, template, cutoff(previous_days[-1]),
                             band_cycles=time.band_cycles, cycles=time.cycles)['position']
                if previous_days else None)
    previous_rank = previous.get('rank') if previous else None
    rank_change = position['rank'] - previous_rank if previous_rank is not None else None
    reasons = []
    if position['rank'] <= 2:
        reasons.append('extreme_high')
    if position['rank'] >= position['n']:
        reasons.append('extreme_low')
    if rank_change is not None and abs(rank_change) >= 2:
        reasons.append('rank_change')
    if not reasons:
        return None
    recent_values = [peer['value'] for peer in position['peers'][:5]]
    return {'id': row.id, 'title': definition.title, 'revision': row.revision, 'series_id': curve.series_id,
            'derived': curve.derived.model_dump() if curve.derived else None,
            'metric': curve.label if curve.derived else field.get('metric') or series.get('name') or curve.label,
            'date': position['date'], 'value': position['value'],
            'unit': series.get('unit') or field.get('unit'), 'transform': time.transform, 'rank': position['rank'], 'n': position['n'],
            'previous_date': previous['date'] if previous else None, 'previous_rank': previous_rank,
            'previous_n': previous['n'] if previous else None, 'rank_change': rank_change,
            'share_below': position['share_below'], 'vs_recent_mean': position['vs_recent_mean'],
            'recent_mean': sum(recent_values) / len(recent_values), 'recent_cycles': len(recent_values),
            'reasons': reasons, 'warnings': current['warnings']}


def cycle_result(series, template, day, *, band_cycles=5, cycles=None):
    points = [(point['date'], point['value']) for point in series['points'] if point['value'] is not None]
    result = compute_cycles(points, template, day, band_cycles=band_cycles, cycles=cycles)
    if series['limited']:
        result['warnings'].append('observation_window_limited')
    return result


def definition_data(scope, definition, day, *, chart_id=None, revision=None, fields=None, data=None, point_limit=None):
    """Preview and saved charts share the same permission and observation path."""
    identifiers = definition_series_ids(definition)
    fields = catalog_fields(scope, identifiers) if fields is None else fields
    notices = unit_notices(definition, fields)
    if any(item['code'] in {'axis_unit_mismatch', 'derived_unit_invalid', 'too_many_units', 'unit_axis_split'}
           for item in notices):
        raise HTTPException(409, '来源单位已变化，公式或纵轴单位校验未通过，请修改图表并重新核对单位')
    time = definition.time
    if time.mode == 'range':
        day = min(day, time.end)
    # A derived curve consumes its real operands' budgets; repeated/missing
    # curves still retain the existing definition-based cap.
    limit = point_limit or min(2000, 20000 // max(len(definition.curves), len(identifiers)))
    if time.mode == 'recent':
        limit = min(limit, time.points)
    data = read_series([sid for sid in identifiers if sid in fields], day, limit=limit) if data is None else data
    if any((fields[sid].get('unit') or '').strip() and fields[sid]['unit'].strip() != series['unit'].strip()
           for sid, series in data.items()):
        raise HTTPException(409, '图表单位在目录与观测读取之间已变化，请刷新核对')
    curves = resolve_curves(definition, fields, data)
    for curve in curves:
        if curve['status'] == 'linked':
            curve['status'] = 'ok'
        notices.extend({**warning, 'label': curve['label']} for warning in curve.get('warnings', []))
    if time.mode == 'cycle':
        template = time.template.model_dump(mode='json', exclude_none=True)
        template['transform'] = time.transform
        if len(curves) == 1:
            result = cycle_result(curves[0], template, day, band_cycles=time.band_cycles, cycles=time.cycles)
            result.update(series_id=definition.curves[0].series_id, unit=curves[0]['unit'])
        else:
            result = compute_multi_cycles(curves, template, day, band_cycles=time.band_cycles, cycles=time.cycles)
        result.update(id=chart_id, revision=revision, notices=notices, point_limit=limit)
        return result
    for curve in curves:
        points = curve['points']
        if time.mode in {'since', 'range'}:
            points = [point for point in points if point['date'] >= time.start.isoformat()]
        if time.transform != 'raw':
            base = next((point['value'] for point in points if point['value'] is not None), None)
            usable = base is not None and (time.transform != 'rebase' or base > 0)
            if not usable and points:
                notices.append({'code': 'invalid_base', 'label': curve['label']})
            transformed = []
            for point in points:
                value = (point['value'] - base if time.transform == 'change' else point['value'] / base * 100
                         ) if usable and point['value'] is not None else None
                if value is not None and not math.isfinite(value):
                    value = None
                    notices.append({'code': 'transform_non_finite', 'label': curve['label'], 'date': point['date']})
                transformed.append({**point, 'raw': point['value'], 'value': value})
            points = transformed
        curve['points'] = points
    return {'id': chart_id, 'revision': revision, 'as_of': day.isoformat(), 'curves': curves,
            'axes': definition.axes.model_dump(), 'warnings': notices, 'point_limit': limit,
            'transform': time.transform}
'''

PAGE_REFERENCE = r'''
def catalog_page(scope, offset=0, limit=200, search=None, series_ids=None, *, version=None, source_kind=None, apply_dedup=True):
    """Selectors hide confirmed copies; saved real-ID bindings remain readable."""
    if (not apply_dedup or scope.scope_version != 2 or scope.restricted
            or not any(source.get('source_type') == 'central_dataset' for source in scope.sources)):
        return _raw_catalog_page(scope, offset, limit, search, series_ids, version=version, source_kind=source_kind)
    from tin.integration.indicator_dedup import confirmed_replacements, catalog_annotations
    replacements = confirmed_replacements(scope)
    if not replacements:
        result = _raw_catalog_page(scope, offset, limit, search, series_ids, version=version, source_kind=source_kind)
        complete = (result['series'] if not offset and not search and series_ids is None and source_kind is None
                    and result['total'] == len(result['series']) and result.get('next_offset') is None else None)
        return {**result, 'series': catalog_annotations(scope, result['series'], current_fields=complete)}
    if type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 200:
        raise HTTPException(400, '指标目录分页无效')
    # Filter before paginating. Each source request is still metadata-only, <=200.
    selected, raw_offset, raw_version, total, datasets, present = [], 0, None, 0, [], set()
    raw_total, complete = None, [] if not search and series_ids is None and source_kind is None else None
    while True:
        result = _raw_catalog_page(scope, raw_offset, 200, search, series_ids, version=raw_version, source_kind=source_kind)
        if raw_version is None:
            raw_version, raw_total, datasets = result['version'], result['total'], result['datasets']
        if result['total'] != raw_total or result['version'] != raw_version:
            raise HTTPException(409, '指标目录在分页期间变化，请刷新后重试')
        for field in result['series']:
            if field['series_id'] in present:
                raise HTTPException(503, '指标目录分页出现重复')
            present.add(field['series_id'])
            if complete is not None:
                complete.append(field)
            if field['series_id'] in replacements:
                continue
            if offset <= total < offset + limit:
                selected.append(field)
            total += 1
        following = result.get('next_offset')
        if following is None:
            if result.get('truncated') or len(present) != raw_total:
                raise HTTPException(503, '指标目录未完整读取，未返回部分目录')
            break
        if not result['series'] or following != raw_offset + len(result['series']):
            raise HTTPException(503, '指标目录分页进度无效')
        raw_offset = following
    filtered_version = hashlib.sha256(json.dumps([raw_version, sorted(replacements.items())], separators=(',', ':')).encode()).hexdigest()
    if version is not None and version != filtered_version:
        raise HTTPException(409, '指标对应或来源目录已变化，请刷新后重试')
    following = offset + len(selected) if offset + len(selected) < total else None
    return {'series': catalog_annotations(scope, selected, current_fields=complete), 'datasets': datasets,
        'version': filtered_version, 'total': total, 'offset': offset,
        'next_offset': following, 'truncated': following is not None,
        'unavailable_series_ids': sorted(set(series_ids or []) - present)}


def _raw_catalog_page(scope, offset=0, limit=200, search=None, series_ids=None, *, version=None, source_kind=None):
    if type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 200:
        raise HTTPException(400, '指标目录分页无效')
    if source_kind is not None and (not isinstance(source_kind,str) or source_kind not in {'macro','industry'}):
        raise HTTPException(400,'目录来源范围无效')
    if search is not None and (not isinstance(search,str) or len(search)>200):
        raise HTTPException(400,'目录检索参数无效')
    if scope.scope_version != 2:
        return _legacy_catalog_page(scope,offset,limit,search,series_ids,version,source_kind)
    body = {'source_bindings':[source['id'] for source in scope.sources if not source['delegated']],
            'catalog_only':True,'offset':offset,'limit':limit}
    if search is not None: body['search'] = search
    if source_kind is not None: body['source_kind'] = source_kind
    if series_ids is not None: body['series_ids'] = list(series_ids)
    if version is not None: body['version'] = version
    result = _request(scope, body)
    if (result.get('catalog_only') is not True or not isinstance(result.get('series'),list)
            or type(result.get('total')) is not int or result['total'] < 0 or result.get('offset') != offset
            or len(result['series']) > limit or not isinstance(result.get('datasets'),list)):
        raise HTTPException(503, '来源指标目录分页不完整')
    direct = {source['id']:source for source in scope.sources if not source['delegated']}
    for field in result['series']:
        source = direct.get(field.get('source_binding_id')) if isinstance(field,dict) else None
        if (source is None or not isinstance(field.get('series_id'),str) or not SERIES.fullmatch(field['series_id'])
                or field.get('knowledge_base_id') != source['knowledge_base_id']
                or field.get('knowledge_base_tenant_id') != source['tenant_id']):
            raise HTTPException(503, '目录项超出当前直接来源授权')
    return result


def bounded_facts(scope, identifiers, module, *, day=None, points=2, same_day=None, origins=None, direct_only=False, include_invalid=False, include_display_precision=False):
    identifiers = list(dict.fromkeys(identifiers))
    query_ids=[identifier for identifier in identifiers if isinstance(identifier,str) and SERIES.fullmatch(identifier)]
    legacy_unavailable=set(identifiers)-set(query_ids)
    if direct_only and legacy_unavailable:
        raise HTTPException(422,'指定指标必须使用当前知识库真实ID')
    targets = [source for source in scope.sources if not source['delegated'] or not direct_only and module in source['view_modules']]
    if direct_only:
        available = catalog_page(scope,limit=200,series_ids=query_ids,apply_dedup=False)
        if available.get('unavailable_series_ids'):
            raise HTTPException(403,'指定指标不在当前直接来源授权中')
    else:
        used = set()
        direct = {source['id'] for source in targets if not source['delegated']}
        allowed = {(source['knowledge_base_id'],source['tenant_id']):source for source in targets}
        for identifier in query_ids:
            origin = (origins or {}).get(identifier)
            if isinstance(origin,dict):
                matched = allowed.get((origin.get('knowledge_base_id'),origin.get('tenant_id')))
                if matched: used.add(matched['id'])
            else:
                # A missing legacy origin can only be checked against sources
                # the caller can browse directly, never a delegated source.
                used.update(direct)
        targets = [source for source in targets if source['id'] in used]
    body = {'source_bindings':[source['id'] for source in targets],'series_ids':query_ids,
        'view_module':module,'points_per_series':points,
        'as_of':day or datetime.now(SHANGHAI).date().isoformat(), 'allow_unavailable':not direct_only}
    if same_day: body['same_day_series_ids'] = same_day
    if include_invalid:
        if module != 'judgments' or scope.scope_version != 2 or same_day:
            raise HTTPException(400, '缺失观测仅允许新版研究判断的明确窗口')
        body['include_invalid'] = True
    if include_display_precision:
        if module != 'indicators' or scope.scope_version != 2 or scope.restricted or any(source['delegated'] for source in targets):
            raise HTTPException(403, '显示精度仅用于直接授权的指标核实')
        body['include_display_precision'] = True
    result = _request(scope,body)
    unavailable = result.get('unavailable_series_ids',[])
    if (not isinstance(unavailable,list) or any(identifier not in query_ids for identifier in unavailable)
            or len(unavailable)!=len(set(unavailable)) or direct_only and unavailable):
        raise HTTPException(503,'页面绑定失效响应无效')
    unavailable=[*unavailable,*sorted(legacy_unavailable)]
    result={**result,'unavailable_series_ids':unavailable}
    if (result.get('bounded') is not True or not isinstance(result.get('rows'),list)
            or len(result['rows']) > len(identifiers)*points or not isinstance(result.get('indicators'),list)
            or len(result['indicators']) != len(set(identifiers)-set(unavailable))
            or {field.get('series_id') for field in result['indicators'] if isinstance(field,dict)} != set(identifiers)-set(unavailable)):
        raise HTTPException(503,'页面取值响应不完整或超过必要范围')
    expected = {source['id']:source for source in targets}
    source_versions=result.get('source_versions',{})
    if (not isinstance(source_versions,dict) or set(source_versions)!=set(expected)
            or any(not isinstance(value,str) or not re.fullmatch('[a-f0-9]{64}',value) for value in source_versions.values())):
        raise HTTPException(503,'页面来源快照版本不完整')
    for spec in result['indicators']:
        source = expected.get(spec.get('source_binding_id'))
        if (source is None or spec.get('knowledge_base_id') != source['knowledge_base_id']
                or type(spec.get('knowledge_base_tenant_id')) is not int or spec['knowledge_base_tenant_id'] != source['tenant_id']
                or spec.get('source_id') != source['source_id'] or spec.get('source_type') != source['source_type']):
            raise HTTPException(503,'页面字段来源与授权绑定不一致')
    seen,counts = set(),{}
    if include_invalid and result.get('include_invalid') is not True:
        raise HTTPException(503, '来源服务未提供完整的均线观测窗口')
    for row in result['rows']:
        missing = include_invalid and row.get('value_status') == 'missing' and row.get('value') is None if isinstance(row, dict) else False
        if (not isinstance(row,dict) or row.get('series_id') not in identifiers
                or not missing and (type(row.get('value')) not in {int,float} or not math.isfinite(row['value'])
                or row.get('value_status') not in {'valid','ok','正常'})
                or not isinstance(row.get('date'),str) or row['date'] > body['as_of']
                or (row['series_id'],row['date']) in seen):
            raise HTTPException(503,'页面取值包含未授权或重复观测')
        try:
            if date.fromisoformat(row['date']).isoformat() != row['date']: raise ValueError
        except ValueError: raise HTTPException(503,'页面观测日期无效') from None
        seen.add((row['series_id'],row['date']))
        counts[row['series_id']] = counts.get(row['series_id'],0)+1
        if counts[row['series_id']]>points:
            raise HTTPException(503,'页面单字段观测超过必要窗口')
    return result
'''

PROJECTION_REFERENCE = r'''
def _insert(connection, table, rows):
    if not rows:
        return
    columns = list(rows[0])
    sql = 'INSERT INTO temp."_kb_' + table + '" (' + ','.join('"' + c + '"' for c in columns) + ') VALUES (' + ','.join('?' for _ in columns) + ')'
    connection.exec_driver_sql(sql, [tuple(row.get(c) for c in columns) for row in rows])


def install_projection(connection, scope, sources=None, pinned_version=None, *, pages=None):
    connection.connection.driver_connection.set_authorizer(None)
    connection.exec_driver_sql('PRAGMA temp_store=MEMORY')
    connection.exec_driver_sql('DROP TABLE IF EXISTS temp._kb_evidence')
    connection.exec_driver_sql('CREATE TEMP TABLE _kb_evidence '
        '(id INTEGER PRIMARY KEY,series_id TEXT NOT NULL,value_cell TEXT,date_cell TEXT,raw_value TEXT,formula TEXT,source_date TEXT)')
    for table in FACT_TABLES:
        connection.exec_driver_sql('DROP VIEW IF EXISTS temp."' + table + '"')
        connection.exec_driver_sql('DROP TABLE IF EXISTS temp."_kb_' + table + '"')
        connection.exec_driver_sql('CREATE TEMP TABLE "_kb_' + table + '" AS SELECT * FROM main."' + table + '" WHERE 0')
    known, seen, version, datasets, raw_counts = {}, {}, None, [], {}
    include_invalid = False
    fetched_at = _sql_time(datetime.now(timezone.utc))
    caliber = json.dumps({'time_type': '原表日期'})
    for index, page in enumerate(fact_pages(scope, sources, pinned_version) if pages is None else pages):
        include_invalid = include_invalid or page.get('include_invalid') is True
        version = page['version']
        if index == 0:
            datasets = page.get('datasets', [])
            indicators = []
            for spec in page['indicators']:
                if (not isinstance(spec, dict) or not isinstance(spec.get('series_id'), str)
                        or not re.fullmatch('kb-[0-9a-f]{48}', spec['series_id'])
                        or spec['series_id'] in known or not isinstance(spec.get('source_knowledge_id'), str)
                        or not isinstance(spec.get('dataset_id'), str) or not isinstance(spec.get('source_series_id'), str)):
                    raise HTTPException(503, '知识库字段身份不完整，未构建研究事实')
                name = spec.get('metric') or spec.get('name')
                if not isinstance(name, str) or not name.strip():
                    raise HTTPException(503, '知识库真实字段缺少名称')
                known[spec['series_id']] = spec
                indicators.append({'series_id': spec['series_id'], 'name': name, 'variety': '',
                    'category': '知识库字段', 'caliber': json.dumps({'time_type': '原表日期', 'note': '原表日期只用于日期查询，不声明发布时间或当时可知性'}, ensure_ascii=False),
                    'unit': spec.get('unit') or '未注明', 'source': '知识库结构化',
                    'source_url': None, 'frequency': spec.get('frequency') or '未注明', 'fetch_mode': 'readonly',
                    'phase': '', 'owner': None, 'status': '可用', 'is_proxy': 0, 'vendor_code': None,
                    'note': '事实来自绑定知识库当前已确认工作表；在知识库统一维护', 'default_entry_time': None})
            _insert(connection, 'indicators', indicators)
            if page.get('encoding') == FACT_ENCODING:
                bindings = set()
                for dataset in datasets:
                    if (not isinstance(dataset, dict) or not isinstance(dataset.get('dataset_id'), str)
                            or not isinstance(dataset.get('source_knowledge_id'), str)
                            or type(dataset.get('raw_observation_count')) is not int
                            or not 0 <= dataset['raw_observation_count'] <= MAX_ROWS):
                        raise HTTPException(503, '知识库原始观测计数无效')
                    binding = dataset['source_knowledge_id'], dataset['dataset_id']
                    if binding in bindings:
                        raise HTTPException(503, '知识库来源计数重复')
                    bindings.add(binding)
                    raw_counts[dataset['dataset_id']] = raw_counts.get(dataset['dataset_id'], 0) + dataset['raw_observation_count']
                if (sum(raw_counts.values()) > MAX_ROWS or sum(raw_counts.values()) < page['total']
                        or any((spec['source_knowledge_id'], spec['dataset_id']) not in bindings for spec in known.values())):
                    raise HTTPException(503, '知识库原始计数与字段来源不一致')
        rows,evidence_rows = [],[]
        for raw in page['rows']:
            spec = known.get(raw.get('series_id')) if isinstance(raw, dict) else None
            if spec is None or any(raw.get(k) != spec.get(k) for k in ('source_knowledge_id', 'dataset_id', 'source_series_id')):
                raise HTTPException(503, '知识库观测来源与字段身份不一致')
            if page.get('encoding') != FACT_ENCODING:
                raw_counts[spec['dataset_id']] = raw_counts.get(spec['dataset_id'], 0) + 1
            value = raw.get('value')
            missing = page.get('include_invalid') is True and raw.get('value_status') == 'missing' and value is None
            if not missing and (raw.get('value_status') not in {'valid', 'ok', '正常'} or value is None):
                continue
            if not missing and (type(value) not in {int, float} or not math.isfinite(value)):
                raise HTTPException(503, '知识库观测数值无效')
            when = raw.get('date')
            if when is None:
                continue
            try:
                if not isinstance(when, str):
                    raise ValueError
                stamp = _date_stamp(when)
            except (ValueError, TypeError):
                raise HTTPException(503, '知识库日期没有明确映射，未推定研究时点') from None
            unique = raw['series_id'], stamp
            if unique in seen:
                if seen[unique] != value:
                    raise HTTPException(409, '同一知识库字段日期存在冲突观测，请在结构化数据中处理')
                continue
            seen[unique] = value
            identifier = int(hashlib.sha256(json.dumps([spec['source_knowledge_id'], spec['dataset_id'], spec['source_series_id'], raw.get('source_cell'), when], ensure_ascii=False).encode()).hexdigest()[:15], 16)
            rows.append({'id': identifier, 'series_id': raw['series_id'], 'value': value, 'as_of': stamp,
                'fetched_at': fetched_at, 'caliber_snapshot': caliber,
                'source': '知识库结构化', 'source_url': None, 'revision': 0, 'entered_by': None,
                'note': '直接读取知识库已确认结构化数据，原表日期不表示发布时间', 'status': '数据缺失' if missing else '正常'})
            evidence_rows.append((identifier,raw['series_id'],raw.get('source_cell'),raw.get('date_cell'),
                json.dumps(raw.get('raw_value'),ensure_ascii=False),raw.get('formula'),when))
        _insert(connection, 'observations', rows)
        if evidence_rows:
            connection.exec_driver_sql('INSERT INTO temp._kb_evidence VALUES (?,?,?,?,?,?,?)',evidence_rows)
    connection.exec_driver_sql('CREATE INDEX temp._kb_obs_lookup ON _kb_observations(series_id,as_of)')
    connection.exec_driver_sql('CREATE UNIQUE INDEX temp._kb_indicator_lookup ON _kb_indicators(series_id)')
    for table in FACT_TABLES:
        connection.exec_driver_sql('CREATE TEMP VIEW "' + table + '" AS SELECT * FROM temp."_kb_' + table + '"')
    connection.commit()

    def protect(action, first, second, database, trigger):
        if action == sqlite3.SQLITE_READ and database == 'main' and first in FACT_TABLES:
            return sqlite3.SQLITE_DENY
        if action in {sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE} and (first in FACT_TABLES or str(first).startswith('_kb_')):
            return sqlite3.SQLITE_DENY
        return sqlite3.SQLITE_OK

    connection.connection.driver_connection.set_authorizer(protect)
    return {'knowledge_facts': True, 'knowledge_facts_version': version,
            **({'knowledge_facts_include_invalid': True} if include_invalid else {}),
            'knowledge_facts_catalog': known,
            'knowledge_facts_datasets': [{**dataset,
                'series_count': sum(spec['dataset_id'] == dataset.get('dataset_id') for spec in known.values()),
                'observation_count': raw_counts.get(dataset.get('dataset_id'), 0)} for dataset in datasets
                if isinstance(dataset, dict) and any(spec['dataset_id'] == dataset.get('dataset_id') for spec in known.values())]}
'''

BRIDGE_REFERENCE = r'''
def structured_request(method, path, body=None, *, return_error_response=False, timeout=120):
    identity = current_identity.get()
    if identity is None:
        raise PermissionError("结构化桥接需要已核验账号和空间")
    base = settings.structured_data_url.rstrip("/")
    parsed = urlsplit(base)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.path or parsed.query or parsed.fragment or parsed.username:
        raise ValueError("结构化服务地址配置无效")
    content = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode() if body is not None else b""
    key = Path(settings.service_token_file).read_bytes().strip()
    if len(key) < 32:
        raise ValueError("结构化桥接服务密钥未配置")
    caller = {"user_id": identity.user_id, "email": identity.email,
        "display_name": identity.display_name, "tenant_id": identity.tenant_id, "role": identity.role}
    from tin.integration.workbench_scope import current_workbench
    scope = current_workbench.get()
    if scope is not None:
        if scope.identity.user_id != identity.user_id or scope.identity.tenant_id != identity.tenant_id:
            raise PermissionError("结构化桥接与工作台身份不匹配")
        caller.update(workbench_id=scope.workbench_id,
                      authorization_sha256=hashlib.sha256(scope.authorization.encode()).hexdigest())
        if scope.scope_version == 2:
            caller.update(scope_version=2, sources_revision=scope.sources_revision)
        else:
            caller.update(knowledge_base_id=scope.knowledge_base_id,
                          knowledge_base_tenant_id=scope.knowledge_base_tenant_id)
    encoded = base64.urlsafe_b64encode(json.dumps(caller,
        separators=(",", ":")).encode()).decode().rstrip("=")
    stamp = str(int(time.time()))
    message = "\n".join((stamp, method, path, encoded, hashlib.sha256(content).hexdigest()))
    signature = hmac.new(key, message.encode(), hashlib.sha256).hexdigest()
    headers = {"Content-Type": "application/json", "X-Research-Timestamp": stamp,
               "X-Research-Identity": encoded, "X-Research-Signature": signature}
    if scope is not None:
        headers['Authorization'] = scope.authorization
    response = httpx.request(method, base+path, content=content, headers=headers,
        follow_redirects=False, trust_env=False, timeout=timeout)
    if response.status_code != 200 and not return_error_response:
        raise ValueError(f"结构化服务读取失败（HTTP {response.status_code}）")
    return response
'''

def _reference(source, module, **overrides):
    namespace = {**vars(module), **overrides}
    exec(compile(source, __file__ + ':reference', 'exec'), namespace)
    return SimpleNamespace(**namespace)


def _seed(client, state):
    """125 definitions, predominantly independent single-metric cycles."""
    day = date(2030, 9, 14)
    identifiers = ['kb-' + f'{index + 1:048x}' for index in range(108)]
    state.fields = {sid: {'series_id': sid, 'metric': f'合成字段{index:03d}', 'unit': '件',
                          'source_version': 'e' * 64}
                    for index, sid in enumerate(identifiers)}
    state.observations = {}
    for index, sid in enumerate(identifiers):
        points = []
        for offset in range(2000):
            stamp = day - timedelta(days=1999 - offset)
            value = (100 + index + (stamp.year - 2025) * ((index % 3) - 1) * 3
                     + math.sin(stamp.timetuple().tm_yday / 13) * (1 + index % 5))
            if offset >= 1998:
                value += (40 if offset == 1999 else -30) * ((index % 2) * 2 - 1)
            points.append((stamp.isoformat(), value))
        state.observations[sid] = points
    # Some histories are window-limited; one is genuinely empty, never zeroed.
    state.observations[identifiers[0]].insert(0, ((day - timedelta(days=2000)).isoformat(), 7.0))
    state.observations[identifiers[-1]] = []
    base_time = {'mode': 'cycle', 'template': {'kind': 'repeat', 'name': '合成自然年',
                 'start': '01-01', 'period': 'year'}, 'cycles': 8, 'band_cycles': 5}
    definitions = []
    for index, sid in enumerate(identifiers):
        time = {**base_time, 'transform': ('raw', 'change', 'rebase')[index % 3]}
        definitions.append(definition(title=f'合成周期{index:03d}', curves=[{'series_id': sid, 'label': f'曲线{index}'}], time=time))
    for index in range(5):
        definitions.append(definition(title=f'共享指标{index}', curves=[{'series_id': identifiers[index], 'label': '共享曲线'}], time=base_time))
    for index in range(5):
        definitions.append(definition(title=f'多指标{index}', curves=[{'series_id': identifiers[index], 'label': '甲'},
            {'series_id': identifiers[index + 5], 'label': '乙'}], time=base_time))
    for index, op in enumerate(('sub', 'div', 'sub')):
        definitions.append(definition(title=f'计算序列{index}', curves=[{'derived': {'op': op, 'a': identifiers[index],
            'b': identifiers[index + 10]}, 'label': '明确计算'}], time=base_time))
    definitions.append(definition(title='缺失来源', curves=[{'series_id': 'kb-' + 'f' * 48, 'label': '已移除字段'}], time=base_time))
    definitions.append(definition(title='损坏定义', time=base_time))
    for index in range(2):
        definitions.append(definition(title=f'走势图{index}', curves=[{'series_id': identifiers[index], 'label': '走势'}]))
    assert len(definitions) == 125

    def seed():
        scope = charts.require_scope()
        with artifact_session(scope) as session:
            for value in definitions:
                row = charts.create_row(session, scope, ChartDefinition.model_validate(value))
                if value['title'] == '损坏定义':
                    row.body = '{"format":"invalid"}'
            session.commit()
        return {'created': len(definitions)}

    state.app.add_api_route('/synthetic-seed', seed, methods=['POST'])
    assert client.post('/synthetic-seed').json() == {'created': 125}
    state.requests.clear()
    return day.isoformat()


def _cold_projection_state():
    # Each scenario starts with empty TEMP tables and date memoization. Only
    # these test-owned engines are disposed; no external service is involved.
    with kb_facts._projection_lock:
        for engine in kb_facts._projection_engines.values():
            engine.dispose()
        kb_facts._projection_engines.clear()
    kb_facts._date_stamp.cache_clear()


def _measured(function, metrics, key):
    def invoke(*args, **kwargs):
        started = perf_counter()
        try:
            return function(*args, **kwargs)
        finally:
            metrics[key] += perf_counter() - started
    return invoke


def _run_scenario(client, state, monkeypatch, day, latency_ms, stage):
    """Incremental changes isolate HTTP, catalog, compute and projection costs."""
    metrics = {key: 0.0 for key in ('client_seconds', 'projection_seconds', 'cycles_seconds', 'upstream_seconds')}
    metrics['clients'] = 0
    _cold_projection_state()
    state.requests.clear()
    synthetic_service = bridge.structured_request
    # The fixture normally substitutes structured_request; this benchmark moves
    # interception down to the actual HTTP transport, preserving the bridge.
    assert synthetic_service is not CURRENT_REQUEST

    def handle(request):
        assert request.url.host == 'synthetic.invalid'
        assert request.headers['Authorization'] == 'Bearer fixture'
        caller = json.loads(base64.urlsafe_b64decode(request.headers['X-Research-Identity'] + '==='))
        assert caller['user_id'] == 'first' and caller['tenant_id'] == 17
        assert caller['workbench_id'] and caller['scope_version'] == 2
        body = json.loads(request.content)
        started = perf_counter()
        if latency_ms:
            sleep(latency_ms / 1000)
        response = synthetic_service(request.method, request.url.path, body, return_error_response=True)
        metrics['upstream_seconds'] += perf_counter() - started
        response.request = request
        return response

    def make_client(*args, **kwargs):
        # Initialize the real default HTTP transport first, including its TLS
        # roots. Replace it before sending anything, so even DNS is never used.
        started = perf_counter()
        result = ORIGINAL_CLIENT(*args, **kwargs)
        result._transport.close()
        result._transport = httpx.MockTransport(handle)
        metrics['client_seconds'] += perf_counter() - started
        metrics['clients'] += 1
        return result

    old_chart = _reference(CHARTS_REFERENCE, charts, compute_cycles=cycles_reference.compute_cycles,
                           compute_multi_cycles=cycles_reference.compute_multi_cycles)
    old_page = _reference(PAGE_REFERENCE, page_facts)
    old_projection = _reference(PROJECTION_REFERENCE, kb_facts)
    old_bridge = _reference(BRIDGE_REFERENCE, bridge)
    modern = stage in {'compute', 'projection', 'all'}
    pool = stage != 'baseline'
    with monkeypatch.context() as patch:
        patch.setattr(httpx, 'Client', make_client)
        # httpx.request imports Client into httpx._api, independently of the
        # public alias. Patching both exercises one real init per old request.
        patch.setattr(httpx._api, 'Client', make_client)
        patch.setattr(bridge, 'structured_request', CURRENT_REQUEST if pool else old_bridge.structured_request)
        if stage in {'baseline', 'http'}:
            patch.setattr(page_facts, 'catalog_page', old_page.catalog_page)
            patch.setattr(page_facts, '_raw_catalog_page', old_page._raw_catalog_page)
            patch.setattr(page_facts, 'bounded_facts', old_page.bounded_facts)
            patch.setattr(chart_data, 'catalog_page', old_page.catalog_page)
            patch.setattr(kb_facts, 'catalog_page', old_page.catalog_page)
        projection = CURRENT_PROJECTION if stage in {'projection', 'all'} else old_projection.install_projection
        patch.setattr(kb_facts, 'install_projection', _measured(projection, metrics, 'projection_seconds'))
        if modern:
            if stage != 'all':
                patch.setattr(chart_data, '_bounded_series', lambda *args: None)
            patch.setattr(charts, 'compute_cycles', _measured(CURRENT_CYCLES, metrics, 'cycles_seconds'))
            patch.setattr(charts, 'compute_multi_cycles', _measured(CURRENT_MULTI_CYCLES, metrics, 'cycles_seconds'))
            endpoint_path = '/api/sn/charts/anomalies'
        else:
            old_chart.chart_anomalies.__globals__['compute_cycles'] = _measured(cycles_reference.compute_cycles, metrics, 'cycles_seconds')
            old_chart.chart_anomalies.__globals__['compute_multi_cycles'] = _measured(cycles_reference.compute_multi_cycles, metrics, 'cycles_seconds')
            endpoint = old_chart.chart_anomalies
            if stage == 'http':
                endpoint = bridge.structured_read_scope()(endpoint)
            elif stage == 'catalog':
                endpoint = chart_data.chart_read_scope(use_bounded=False)(endpoint)
            # Register the frozen handler through real FastAPI routing and the
            # same identity/workbench middleware. A separate test-only path
            # avoids depending on FastAPI's cached include_router internals.
            endpoint_path = '/synthetic-anomalies/' + stage
            state.app.add_api_route(endpoint_path, endpoint, methods=['GET'])
        started = perf_counter()
        response = client.get(endpoint_path, params={'date': day})
        metrics['seconds'] = perf_counter() - started
        assert response.status_code == 200, response.text
    for label, catalog in (('catalog_requests', True), ('bounded_requests', False)):
        metrics[label] = sum(bool(body.get('catalog_only')) == catalog for _, body in state.requests)
    for _, body in state.requests:
        if not body.get('catalog_only'):
            assert len(body['series_ids']) <= 256
            assert len(body['series_ids']) * body['points_per_series'] <= 20000
    metrics['stage'] = stage
    metrics['latency_ms'] = latency_ms
    return response.json(), metrics


@pytest.mark.parametrize('latency_ms', [0, 5])
def test_125_chart_outputs_equal_frozen_reference_and_report_cold_timings(chart_client, monkeypatch, tmp_path, latency_ms):
    client, state = chart_client
    day = _seed(client, state)
    token = tmp_path / 'synthetic-service-token'
    token.write_bytes(b'synthetic-key-for-tests-only-000000000000000000')
    monkeypatch.setattr(settings, 'service_token_file', token)
    monkeypatch.setattr(settings, 'structured_data_url', 'http://synthetic.invalid')
    expected = None
    timings = []
    for stage in ('baseline', 'http', 'catalog', 'compute', 'projection', 'all'):
        actual, metrics = _run_scenario(client, state, monkeypatch, day, latency_ms, stage)
        print('\nANOMALIES_STAGE ' + json.dumps(metrics, sort_keys=True), flush=True)
        if expected is None:
            expected = actual
            assert actual['total'] > 90
            assert len(actual['failed']) == 2
            assert {item['title'] for item in actual['failed']} == {'缺失来源', '损坏定义'}
            assert any(item.get('curve_index') is not None for item in actual['items'])
            assert any(item.get('derived') is not None for item in actual['items'])
            assert any('rank_change' in item['reasons'] for item in actual['items'])
        # Includes ordering, revision, rank/n, share_below, five-cycle means,
        # reasons/warnings, every other returned field, and the failed list.
        assert actual == expected
        timings.append(metrics)
    assert timings[0]['clients'] == timings[0]['catalog_requests'] + timings[0]['bounded_requests']
    assert timings[-1]['clients'] == 1
    assert timings[-1]['catalog_requests'] < timings[0]['catalog_requests']
    assert timings[-1]['bounded_requests'] == timings[0]['bounded_requests']
    assert all(item['cycles_seconds'] > 0 for item in timings)
    print('\nANOMALIES_COLD_SYNTHETIC ' + json.dumps(timings, sort_keys=True))

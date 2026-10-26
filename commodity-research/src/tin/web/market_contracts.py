"""Explicit KB bindings for a same-day, specific-contract settlement curve.

Contract identities, delivery months and field meanings are owner configuration;
neither names nor legacy SHFE-shaped IDs are used to infer them.
"""
from dataclasses import replace
from datetime import date, datetime, time
import math
import re

import httpx
from fastapi import HTTPException
from sqlalchemy import func, select

from tin.compute.engine import day_end
from tin.config import SHANGHAI, settings
from tin.integration.workbench_scope import current_workbench, require_shared_write
from tin.models import Observation, SourceMapping
from tin.web.restricted_views import ORIGINS_KEY, _ids, _save_mapping, mapping_key, remember_origins

CONTRACTS_KEY = 'workbench-market-contracts'
MAX_CONTRACTS = 24
MAX_CONTRACT_IDS = 128
FIELDS = ('settle', 'close', 'volume', 'oi')
CONFIG_FIELDS = {'contract', 'delivery_month', 'settle', 'close', 'volume', 'oi',
                 'price_unit', 'volume_unit', 'oi_unit', 'main'}


def _unit(value, *, optional=False):
    if value is None and optional:
        return None
    if not isinstance(value, str) or not value.strip() or len(value) > 64 or value != value.strip():
        raise HTTPException(409, '合约字段需要明确且一致的原表单位')
    return value


def _validate(contracts):
    if not isinstance(contracts, list) or len(contracts) > MAX_CONTRACTS:
        raise HTTPException(409, '最多配置24个明确的具体合约')
    result, labels, months, settlements, identifiers, prices = [], set(), set(), set(), [], set()
    main_count = 0
    for item in contracts:
        if not isinstance(item, dict) or set(item) - CONFIG_FIELDS:
            raise HTTPException(409, '合约来源配置格式无效')
        label, month = item.get('contract'), item.get('delivery_month')
        if (not isinstance(label, str) or not label.strip() or label != label.strip()
                or len(label) > 64 or label in labels or any(ord(char) < 32 for char in label)):
            raise HTTPException(409, '具体合约名称不能为空或重复')
        if not isinstance(month, str) or not re.fullmatch(r'\d{4}-\d{2}', month):
            raise HTTPException(409, '交割年月需要明确为 YYYY-MM')
        try:
            date.fromisoformat(month + '-01')
        except ValueError:
            raise HTTPException(409, '交割年月无效') from None
        if month in months:
            raise HTTPException(409, '同一交割月不能重复配置不同合约')
        main = item.get('main', False)
        if type(main) is not bool:
            raise HTTPException(409, '主力标记必须明确为布尔值')
        main_count += main
        mapped = {field: item.get(field) for field in FIELDS}
        if mapped['settle'] is None:
            raise HTTPException(409, '每个合约需要绑定真实结算价指标')
        for identifier in mapped.values():
            if identifier is not None:
                _ids([identifier])
                identifiers.append(identifier)
        if mapped['settle'] in settlements:
            raise HTTPException(409, '不同合约不能复用同一个结算价指标')
        price_unit = _unit(item.get('price_unit'))
        prices.add(price_unit)
        volume_unit, oi_unit = _unit(item.get('volume_unit'), optional=True), _unit(item.get('oi_unit'), optional=True)
        if mapped['volume'] is None and volume_unit is not None or mapped['oi'] is None and oi_unit is not None:
            raise HTTPException(409, '未绑定数量指标时不能声明其单位')
        result.append({'contract': label, 'delivery_month': month, **mapped, 'price_unit': price_unit,
                       'volume_unit': volume_unit, 'oi_unit': oi_unit, 'main': main})
        labels.add(label)
        months.add(month)
        settlements.add(mapped['settle'])
    if len(prices) > 1:
        raise HTTPException(409, '合约结算价必须使用相同单位，不能混画不同价格量纲')
    if main_count > 1 or len(set(identifiers)) > MAX_CONTRACT_IDS:
        raise HTTPException(409, '最多指定一个主力合约，绑定指标总数不能超过128项')
    return sorted(result, key=lambda row: row['delivery_month'])


def contract_config(session):
    if not session.info.get('workbench_id'):
        raise HTTPException(409, '请从绑定知识库的研究工作台进入')
    row = session.get(SourceMapping, mapping_key(session, CONTRACTS_KEY))
    if row is None:
        return []
    if not isinstance(row.config, dict) or set(row.config) != {'contracts'}:
        raise HTTPException(409, '合约来源配置无效，请重新保存')
    return _validate(row.config['contracts'])


def _identifiers(contracts):
    return list(dict.fromkeys(row[field] for row in contracts for field in FIELDS if row[field] is not None))


def _verify_catalog(contracts, catalog, scope):
    if not isinstance(catalog, dict):
        raise HTTPException(409, '合约来源未经过当前知识库核验')
    for row in contracts:
        for field in FIELDS:
            identifier = row[field]
            if identifier is None:
                continue
            spec = catalog.get(identifier)
            allowed = {(source['knowledge_base_id'],source['tenant_id']) for source in scope.sources} if scope.scope_version == 2 else {(scope.knowledge_base_id,scope.knowledge_base_tenant_id)}
            if (not isinstance(spec, dict) or type(spec.get('knowledge_base_tenant_id')) is not int
                    or (spec.get('knowledge_base_id'),spec['knowledge_base_tenant_id']) not in allowed):
                raise HTTPException(404, '合约指标不在当前绑定知识库，不能借用其他来源')
            expected = row['price_unit'] if field in {'settle', 'close'} else row[field + '_unit']
            actual = spec.get('unit') or None
            if expected != actual:
                raise HTTPException(409, '合约配置单位与当前知识库指标单位不一致，请核对来源')


def _readable_scope(session):
    scope = current_workbench.get()
    if (scope is None or scope.restricted or not scope.source_available and scope.scope_version != 2
            or session.info.get('workbench_id') != scope.workbench_id
            or session.info.get('knowledge_facts') is not True):
        raise HTTPException(403, '合约行情只读取当前知识库的已授权事实')
    return scope


def save_contract_config(session, contracts):
    scope = _readable_scope(session)
    require_shared_write(scope)
    contracts = _validate(contracts)
    _verify_catalog(contracts, session.info.get('knowledge_facts_catalog'), scope)
    remember_origins(session, _identifiers(contracts))
    _save_mapping(session, CONTRACTS_KEY, {'contracts': contracts})
    session.commit()
    return {'contracts': contracts}


def _curve(contracts, day, values, *, notices=()):
    rows, notes = [], list(notices)
    stamp = day.isoformat() if day is not None else None
    for item in contracts:
        fields = {field: values.get(item[field], {}).get(stamp) if item[field] is not None else None
                  for field in FIELDS}
        if fields['settle'] is None:
            continue
        comparable = item['volume_unit'] == '手' and fields['volume'] is not None
        rows.append({'contract': item['contract'], 'month': item['delivery_month'],
            'delivery_month': item['delivery_month'], **fields, 'main': item['main'],
            'active': bool(comparable and fields['volume'] >= settings.liquidity_min_volume),
            'liquidity_comparable': comparable, 'unit': item['price_unit'],
            'volume_unit': item['volume_unit'], 'oi_unit': item['oi_unit'], 'date': stamp})
    prices = [row['settle'] for row in rows]
    low, high = (min(prices), max(prices)) if prices else (None, None)
    points = []
    if prices:
        scale = max(abs(low), abs(high), 1)
        spread = high / scale - low / scale
        for index, row in enumerate(rows):
            row['x'] = round(index * 400 / (len(rows) - 1), 2) if len(rows) > 1 else 200.0
            row['y'] = round(3 + (high / scale - row['settle'] / scale) * 114 / spread, 2) if spread else 60.0
            points.append(f"{row['x']},{row['y']}")
    active = [row for row in rows if row['active']]
    shape = None
    if len(active) >= 2:
        first, last = active[0]['settle'], active[-1]['settle']
        shape = 'Contango' if last > first else 'Backwardation' if last < first else '持平'
    if any(row['volume_unit'] not in {None, '手'} and row['volume'] is not None for row in rows):
        notes.append('部分成交量单位不适用现有以手计的活跃阈值，未推定活跃状态。')
    if rows and len(rows) < len(contracts):
        notes.append('部分配置合约在该交易日没有已确认结算价，未用其他日期补齐。')
    def shared_unit(field):
        units = {item[field] for item in contracts if item[field] is not None}
        return next(iter(units)) if len(units) == 1 else None
    return {'rows': rows, 'points': ' '.join(points), 'shape': shape, 'min': low, 'max': high,
            'mid': low / 2 + high / 2 if prices else None, 'date': stamp, 'through': stamp,
            'unit': contracts[0]['price_unit'] if contracts else None,
            'volume_unit': shared_unit('volume_unit'), 'oi_unit': shared_unit('oi_unit'),
            'source': 'knowledge-base', 'configured': bool(contracts), 'notices': notes}


def _default_day(session, contracts):
    """Find a true common original day in SQL; never fill stale values forward."""
    identifiers = [row['settle'] for row in contracts]
    if not identifiers:
        return None
    # Workbench KB projections are SQLite; times are stored UTC and shown in
    # the fixed Shanghai zone. Only the selected fields enter this query.
    ranked = select(Observation.series_id, Observation.as_of, Observation.status,
        func.row_number().over(partition_by=(Observation.series_id, Observation.as_of),
            order_by=(Observation.revision.desc(), Observation.id.desc())).label('rank')).where(
                Observation.series_id.in_(identifiers),
                Observation.as_of < day_end(datetime.now(SHANGHAI).date())).subquery()
    local_day = func.date(ranked.c.as_of, '+8 hours')
    dates = select(ranked.c.series_id, local_day.label('day')).where(
        ranked.c.rank == 1, ranked.c.status == '正常').group_by(ranked.c.series_id, local_day).subquery()
    common = session.scalar(select(dates.c.day).group_by(dates.c.day)
        .having(func.count() == len(identifiers)).order_by(dates.c.day.desc()).limit(1))
    chosen = common or session.scalar(select(func.max(dates.c.day)))
    return date.fromisoformat(chosen) if chosen else None


def configured_contract_curve(session, day=None):
    scope = _readable_scope(session)
    contracts = contract_config(session)
    if scope.scope_version == 2:
        unavailable = set(_identifiers(contracts)) - set(session.info.get('knowledge_facts_catalog',{}))
        if unavailable:
            return {**_curve(contracts,None,{},notices=('部分合约指标的来源已解除或不可用，请重新绑定。',)), 'unavailable':sorted(unavailable)}
    _verify_catalog(contracts, session.info.get('knowledge_facts_catalog'), scope)
    if day is not None and type(day) is not date:
        raise HTTPException(400, '合约行情日期应为明确的交易日')
    day = day if day is not None else _default_day(session, contracts)
    if day is None:
        return _curve(contracts, None, {})
    start = datetime.combine(day, time.min, SHANGHAI)
    values = {}
    for identifier in _identifiers(contracts):
        ranked = select(Observation.as_of, Observation.value, Observation.status,
            func.row_number().over(partition_by=Observation.as_of,
                order_by=(Observation.revision.desc(), Observation.id.desc())).label('rank')).where(
                    Observation.series_id == identifier, Observation.as_of >= start,
                    Observation.as_of < day_end(day)).subquery()
        rows = session.execute(select(ranked.c.as_of, ranked.c.value).where(
            ranked.c.rank == 1, ranked.c.status == '正常').order_by(ranked.c.as_of.desc()).limit(2)).all()
        if len(rows) > 1:
            raise HTTPException(409, '合约同日有多个原始时点，请先明确日行情口径')
        if rows:
            value = rows[0].value
            if type(value) not in {int, float} or not math.isfinite(value):
                raise HTTPException(503, '合约行情包含无效数值，未展示部分结果')
            values[identifier] = {day.isoformat(): value}
    return _curve(contracts, day, values)


def _same_day_contract_values(scope, identifiers, settlement_ids):
    """The source service selects the day; only one same-day value leaves it."""
    from tin.integration.kb_facts import current_sources
    from tin.provenance.bridge import structured_request
    marker = current_workbench.set(scope)
    try:
        body = {'sources': current_sources(scope), 'series_ids': identifiers, 'view_module': 'market',
                'as_of': datetime.now(SHANGHAI).date().isoformat(), 'points_per_series': 1,
                'same_day_series_ids': settlement_ids}
        response = structured_request('POST', '/api/research/facts', body, return_error_response=True)
        if response.status_code in {401, 403, 404, 409}:
            raise HTTPException(response.status_code, '合约行情来源授权或原始数据已改变，请重新打开')
        response.raise_for_status()
        doc = response.json()
        if (not isinstance(doc, dict) or doc.get('bounded') is not True
                or doc.get('knowledge_base_id') != scope.knowledge_base_id
                or type(doc.get('knowledge_base_tenant_id')) is not int
                or doc['knowledge_base_tenant_id'] != scope.knowledge_base_tenant_id
                or not isinstance(doc.get('version'), str) or not re.fullmatch('[0-9a-f]{64}', doc['version'])
                or type(doc.get('offset')) is not int or doc['offset'] != 0
                or doc.get('next_offset') is not None or doc.get('truncated') is not False
                or type(doc.get('total')) is not int or not isinstance(doc.get('rows'), list)
                or doc['total'] != len(doc['rows']) or len(doc['rows']) > len(identifiers)
                or not isinstance(doc.get('indicators'), list) or len(doc['indicators']) != len(identifiers)
                or 'selected_date' not in doc):
            raise ValueError('Invalid same-day snapshot')
        stamp = doc['selected_date']
        if stamp is not None and (not isinstance(stamp, str)
                or date.fromisoformat(stamp).isoformat() != stamp or stamp > body['as_of']):
            raise ValueError('Invalid selected day')
        if stamp is None and doc['rows']:
            raise ValueError('Unselected observations')
        metadata, values = {}, {}
        for spec in doc['indicators']:
            identifier = spec.get('series_id') if isinstance(spec, dict) else None
            if identifier not in identifiers or identifier in metadata:
                raise ValueError('Unexpected field')
            metadata[identifier] = {**spec, 'knowledge_base_id': scope.knowledge_base_id,
                                   'knowledge_base_tenant_id': scope.knowledge_base_tenant_id}
        if set(metadata) != set(identifiers):
            raise ValueError('Incomplete bound metadata')
        for row in doc['rows']:
            identifier = row.get('series_id') if isinstance(row, dict) else None
            value = row.get('value')
            if (identifier not in metadata or identifier in values or row.get('date') != stamp
                    or row.get('value_status') not in {'valid', 'ok', '正常'}
                    or type(value) not in {int, float} or not math.isfinite(value)):
                raise ValueError('Out of scope same-day value')
            values[identifier] = {stamp: value}
        return date.fromisoformat(stamp) if stamp else None, metadata, values
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError, TypeError, KeyError, AttributeError, OSError):
        raise HTTPException(503, '同日合约行情响应超出授权范围或暂不可用，未展示部分数据') from None
    finally:
        current_workbench.reset(marker)


def readonly_contract_curve(session, scope):
    """Only saved current-KB IDs, with the server-selected exact common day."""
    if (not scope.restricted or not scope.source_available or 'market' not in scope.view_modules
            or session.info.get('workbench_id') != scope.workbench_id):
        raise HTTPException(403, '当前成员没有市场合约行情展示授权')
    contracts = contract_config(session)
    identifiers = _identifiers(contracts)
    if not identifiers:
        return _curve(contracts, None, {})
    row = session.get(SourceMapping, mapping_key(session, ORIGINS_KEY))
    origins = (row.config or {}).get('origins') if row and isinstance(row.config, dict) else None
    if not isinstance(origins, dict):
        raise HTTPException(409, '合约行情缺少已核验来源，请由编辑成员重新保存')
    if scope.scope_version == 2:
        from tin.integration.page_facts import bounded_facts
        result = bounded_facts(scope,identifiers,'market',points=1,
            same_day=[row['settle'] for row in contracts],origins=origins)
        if result.get('unavailable_series_ids'):
            return {**_curve(contracts,None,{},notices=('部分合约指标的来源已解除或不可用，请重新绑定。',)), 'unavailable':result['unavailable_series_ids']}
        metadata = {field['series_id']:field for field in result['indicators']}
        _verify_catalog(contracts,metadata,scope)
        stamp = result.get('selected_date')
        if stamp is not None and (not isinstance(stamp,str) or date.fromisoformat(stamp).isoformat()!=stamp):
            raise HTTPException(503,'合约行情共同日期无效')
        values = {row['series_id']:{row['date']:row['value']} for row in result['rows']}
        if any(row['date'] != stamp for row in result['rows']):
            raise HTTPException(503,'合约行情响应包含不同日期')
        return _curve(contracts,date.fromisoformat(stamp) if stamp else None,values)
    for identifier in identifiers:
        origin = origins.get(identifier)
        if (not isinstance(origin, dict) or origin.get('knowledge_base_id') != scope.knowledge_base_id
                or type(origin.get('tenant_id')) is not int
                or origin['tenant_id'] != scope.knowledge_base_tenant_id):
            raise HTTPException(403, '合约行情绑定不在当前知识库来源授权内')
    source_scope = replace(scope, additional_knowledge_bases=())
    chosen, metadata, values = _same_day_contract_values(source_scope, identifiers, [row['settle'] for row in contracts])
    _verify_catalog(contracts, metadata, scope)
    return _curve(contracts, chosen, values)

"""Read-only distances for verified preview rules and their necessary observations."""
from dataclasses import replace
from datetime import date as Date, datetime
import math
import re
from types import SimpleNamespace

import httpx
from fastapi import APIRouter, HTTPException, Query, Request
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI
from tin.export.board import threshold_radar_values
from tin.integration.page_facts import page_read_scope, projected_page_session
from tin.integration.workbench_scope import current_workbench
from tin.judgments.analysis import AnalysisError
from tin.models import Indicator
from tin.web import judgment_generation as generation
from tin.web import judgment_review as review

router = APIRouter(prefix='/api/sn/judgment/review')


def legacy_series(scope, identifiers, day, catalog, points, *, include_invalid=False):
    """The old protocol still lists metadata, but observes only requested IDs."""
    from tin.integration.kb_facts import current_sources
    from tin.provenance.bridge import structured_request
    known = {field['id']: field for field in catalog}
    permitted = {scope.knowledge_base_id: scope.knowledge_base_tenant_id,
        **{source['knowledge_base_id']: source['tenant_id'] for source in scope.additional_knowledge_bases}}
    groups, values = {}, {}
    for identifier in identifiers:
        field = known[identifier]
        kb = field.get('knowledge_base_id') or scope.knowledge_base_id
        if kb not in permitted:
            raise HTTPException(403, '阈值指标不在当前来源授权中')
        groups.setdefault(kb, []).append(identifier)
        values[identifier] = {'name': field.get('name') or identifier, 'unit': field.get('unit'),
            'points': []}
    for kb, selected in groups.items():
        local = replace(scope, knowledge_base_id=kb, knowledge_base_tenant_id=permitted[kb],
            additional_knowledge_bases=())
        marker = current_workbench.set(local)
        try:
            body = {'sources': current_sources(local), 'series_ids': selected, 'view_module': 'judgments',
                'as_of': day.isoformat(), 'points_per_series': points}
            if include_invalid:
                body['include_invalid'] = True
            response = structured_request('POST', '/api/research/facts', body, return_error_response=True)
            if response.status_code in {401, 403, 404, 409}:
                raise HTTPException(response.status_code, '阈值来源或权限已改变，请重新核对')
            response.raise_for_status()
            result = response.json()
            if include_invalid and result.get('include_invalid') is not True:
                raise HTTPException(409, '来源服务未保留完整均线观测窗口，请更新来源服务')
            if (not isinstance(result, dict) or result.get('bounded') is not True
                    or result.get('knowledge_base_id') != kb
                    or type(result.get('knowledge_base_tenant_id')) is not int
                    or result['knowledge_base_tenant_id'] != permitted[kb]
                    or not isinstance(result.get('version'), str) or not re.fullmatch('[a-f0-9]{64}', result['version'])
                    or result.get('offset') != 0 or result.get('next_offset') is not None
                    or result.get('truncated') is not False or type(result.get('total')) is not int
                    or not isinstance(result.get('rows'), list) or result['total'] != len(result['rows'])
                    or len(result['rows']) > len(selected) * points or not isinstance(result.get('indicators'), list)
                    or len(result['indicators']) != len(selected)):
                raise ValueError('Invalid bounded radar response')
            fields = {field['series_id']: field for field in result['indicators']}
            if set(fields) != set(selected):
                raise ValueError('Unexpected radar field')
            for identifier, field in fields.items():
                if field.get('unit') != known[identifier].get('unit'):
                    raise HTTPException(409, '阈值指标单位已改变，请重新分析')
            seen = set()
            for point in result['rows']:
                identifier, stamp, value = point.get('series_id'), point.get('date'), point.get('value')
                if (identifier not in fields or (identifier, stamp) in seen or not isinstance(stamp, str)
                        or Date.fromisoformat(stamp).isoformat() != stamp or stamp > day.isoformat()
                        or not (include_invalid and point.get('value_status') == 'missing' and value is None)
                        and (type(value) not in {int, float} or not math.isfinite(value)
                        or point.get('value_status') not in {'valid', 'ok', '正常'})
                        or len(values[identifier]['points']) >= points):
                    raise ValueError('Unexpected radar observation')
                seen.add((identifier, stamp))
                values[identifier]['points'].append({'date': stamp, 'value': value})
        except HTTPException:
            raise
        except (httpx.HTTPError, ValueError, TypeError, KeyError, AttributeError, OSError):
            raise HTTPException(503, '阈值取值超出必要范围或暂不可用，未使用全量或缓存事实') from None
        finally:
            current_workbench.reset(marker)
    return values


def read_series(scope, identifiers, catalog, day, points, *, include_invalid=False):
    if not identifiers:
        return []
    include_invalid = include_invalid or scope.scope_version == 2 and points > 2
    with page_read_scope('judgments', series_ids=identifiers, day=day, points_per_series=points, include_invalid=include_invalid):
        if scope.scope_version != 2:
            values = legacy_series(scope, identifiers, day, catalog, points, include_invalid=include_invalid)
        else:
            # Avoid the ordinary bootstrap's persistent isolated_scope INSERT.
            with generation.result_session(scope) as results:
                engine = results.get_bind()
            values = {}
            with projected_page_session(engine, scope) as facts:
                known = {field['id']: field for field in catalog}
                for identifier in identifiers:
                    indicator = facts.get(Indicator, identifier)
                    if indicator is not None and indicator.unit != (known[identifier].get('unit') or '未注明'):
                        raise HTTPException(409, '指标单位已改变，请重新分析')
                    from tin.compute.signals import recent_points
                    rows = recent_points(facts, identifier, day, points)
                    values[identifier] = {'name': indicator.name if indicator else known[identifier].get('name'),
                        'unit': known[identifier].get('unit'), 'points': [{'date': row.as_of.astimezone(SHANGHAI).date().isoformat(),
                            'value': row.value if row.status == '正常' else None} for row in rows]}
    result = []
    for identifier in identifiers:
        field = values[identifier]
        observations = sorted(field['points'], key=lambda point: point['date'])
        latest = observations[-1] if observations else None
        result.append({'series_id': identifier, 'name': field['name'], 'unit': field['unit'],
            'points': observations, 'latest_value': latest['value'] if latest else None,
            'as_of': latest['date'] if latest else None})
    return result


def read_radar(scope, rules, catalog, day):
    from tin.compute.moving_average import moving_average_target, moving_average_points
    windows = {}
    for rule in rules:
        windows[rule['series_id']] = max(windows.get(rule['series_id'], 1),
            moving_average_points(rule['value'], rule['operator']))
    if len(windows) > 256 or sum(windows.values()) > 20000:
        raise HTTPException(413, '阈值试算超过必要观测读取范围')
    series = []
    for points in sorted(set(windows.values())):
        identifiers = [identifier for identifier, required in windows.items() if required == points]
        moving = any(moving_average_target(rule['value']) is not None and rule['series_id'] in identifiers for rule in rules)
        if moving:
            series.extend(read_series(scope, identifiers, catalog, day, points, include_invalid=True))
        else:
            series.extend(read_series(scope, identifiers, catalog, day, points))
    values = {field['series_id']: {'name': field['name'], 'unit': field['unit'],
        'value': field['latest_value'], 'date': field['as_of'], 'points': field['points']} for field in series}
    return threshold_radar_values(SimpleNamespace(thresholds=rules), values)


def checked(preview, catalog, scope):
    try:
        return review.checked(preview, preview['document'], catalog, scope)
    except AnalysisError as error:
        raise HTTPException(422, generation.validation_detail(error)) from None


async def unchanged(scope, draft_id, revision):
    current, _ = await review.current_preview(scope, draft_id)
    if current['review']['revision'] != revision:
        raise HTTPException(409, '预览在试算期间已更新，请重新试算')


def cutoff(date):
    try:
        day = Date.fromisoformat(date) if date is not None else datetime.now(SHANGHAI).date()
        if date is not None and day.isoformat() != date or day > datetime.now(SHANGHAI).date():
            raise ValueError
        return day
    except ValueError:
        raise HTTPException(422, '试算日期须为今天或历史日期YYYY-MM-DD') from None


@router.get('/radar')
async def radar(request: Request, draft_id: str = Query(min_length=16, max_length=128),
                date: str | None = Query(default=None)):
    if set(request.query_params) - {'draft_id', 'date'}:
        raise HTTPException(422, '阈值试算只接受当前预览与日期，不接受任意指标')
    day = cutoff(date)
    scope = generation.scope_for_analysis(write=True)
    preview, state = await review.current_preview(scope, draft_id)
    validated = checked(preview, state['catalog'], scope)
    rules = [rule.model_dump(mode='json') for rule in validated.payload.thresholds]
    rule_items = {item['value']['id']: item['id'] for item in preview['review']['items']
        if item['kind'] == 'thresholds'}
    if {rule['id'] for rule in rules} - set(rule_items):
        raise HTTPException(409, '阈值条目与预览不一致，请重新分析')
    rows = await run_in_threadpool(read_radar, scope, rules, state['catalog'], day)
    for row in rows:
        row['item_id'] = rule_items[row['id']]
    await unchanged(scope, draft_id, preview['review']['revision'])
    return {'draft_id': draft_id, 'revision': preview['review']['revision'],
        'requested_date': date, 'effective_date': day.isoformat(), 'rows': rows,
        'notice': '仅按当前预览已核验阈值和截至指定日期的可读观测试算；不保存信号，不自动生效。'}


@router.get('/indicator-values')
async def indicator_values(request: Request, draft_id: str = Query(min_length=16, max_length=128),
                           item_ids: str = Query(max_length=2000), date: str | None = Query(default=None)):
    if set(request.query_params) - {'draft_id', 'item_ids', 'date'}:
        raise HTTPException(422, '指标试算只接受当前预览条目，不接受任意指标')
    day = cutoff(date)
    selected = item_ids.split(',') if item_ids else []
    if len(selected) > 24 or len(set(selected)) != len(selected) or any(not item for item in selected):
        raise HTTPException(422, '一次最多读取24个不同的当前预览条目')
    scope = generation.scope_for_analysis(write=True)
    preview, state = await review.current_preview(scope, draft_id)
    checked(preview, state['catalog'], scope)
    items = {item['id']: item for item in preview['review']['items']}
    if set(selected) - set(items):
        raise HTTPException(404, '指标条目不属于当前预览')
    identifiers = list(dict.fromkeys(items[identifier]['series_id'] for identifier in selected
        if items[identifier]['status'] not in {'kept_text', 'excluded'} and items[identifier]['series_id'] is not None))
    if set(identifiers) - {field['id'] for field in state['catalog']}:
        raise HTTPException(422, '预览绑定不属于当前授权目录')
    series = await run_in_threadpool(read_series, scope, identifiers, state['catalog'], day,
        24 if scope.scope_version == 2 else 2)
    await unchanged(scope, draft_id, preview['review']['revision'])
    return {'draft_id': draft_id, 'revision': preview['review']['revision'], 'series': series,
        'requested_date': date, 'effective_date': day.isoformat(),
        'notice': '仅展示当前可见条目所选指标的近期原表数值；不补零，不执行未核验规则，不代表发布时间或实时行情。'}

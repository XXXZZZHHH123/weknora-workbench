"""Bounded research reads. The caller and workspace are never model arguments."""
from datetime import date as Date, timezone
from enum import Enum

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select, func, and_

from tin.compute.engine import latest_trade_date, day_end, latest_obs
from tin.config import SHANGHAI, settings
from tin.db import SessionLocal
from tin.export import board, indicators
from tin.integration.identity import current_identity, research_url
from tin.judgments.service import current, to_payload
from tin.jobs.seed import seed_researcher
from tin.models import DailyBrief, Indicator, Observation, Signal, Judgment, TradingDay
from tin.ownership import scope_query
from tin.strategy.service import list_strategies, serialize_strategy

router = APIRouter()


class Operation(str, Enum):
    summary = "summary"
    indicators = "indicators"
    series = "series"
    judgment = "judgment"
    signals = "signals"
    brief = "brief"
    strategies = "strategies"


class ReadRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    operation: Operation
    date: Date | None = None
    series_id: str | None = Field(default=None, max_length=80)
    search: str = Field(default="", max_length=200)
    limit: int = Field(default=30, ge=1, le=200)
    offset: int = Field(default=0, ge=0, le=100000)


def read_research(session, body: ReadRequest):
    """Return actual coverage, provenance and explicit gaps, without generating data."""
    latest = latest_trade_date(session)
    day = body.date or latest
    brief_row = None
    if body.operation == Operation.brief:
        me = seed_researcher(session)
        query = select(DailyBrief).where(DailyBrief.researcher_id == me.id, DailyBrief.variety == "SN")
        if body.date:
            query = query.where(DailyBrief.trade_date == body.date.isoformat())
            day = body.date
        brief_row = session.scalar(query.order_by(DailyBrief.trade_date.desc(), DailyBrief.generated_at.desc()).limit(1))
        if brief_row is not None:
            day = Date.fromisoformat(brief_row.trade_date)
    elif body.operation == Operation.series:
        # Imported/manual series are not limited by the exchange trading calendar.
        if not body.series_id:
            raise HTTPException(400, '读取序列必须提供已登记的 series_id，请先检索指标')
        if session.get(Indicator, body.series_id) is None:
            raise HTTPException(404, '指标未登记')
        query = select(Observation.as_of).where(Observation.series_id == body.series_id,
                                                Observation.status == '正常')
        if body.date:
            query = query.where(Observation.as_of < day_end(body.date))
        timestamp = session.scalar(query.order_by(Observation.as_of.desc()).limit(1))
        day = timestamp.astimezone(SHANGHAI).date() if timestamp else None
    elif body.date:
        matched = session.scalar(select(TradingDay.trade_date).where(
            TradingDay.trade_date <= body.date.isoformat()).order_by(TradingDay.trade_date.desc()).limit(1))
        day = Date.fromisoformat(matched) if matched else None
    result = {"operation": body.operation.value, "requested_date": body.date,
              "latest_trade_date": latest, "effective_date": day,
              "notice": "研究库数据；日期和草稿状态按实际返回，不代表实时行情或研究员已确认。",
              "judgment_basis": "判断是当前账号的最新生效版本或最新草稿；历史日期限定事实、信号和简报，不代表历史当时可知的判断回放。"}
    if body.operation == Operation.indicators:
        query = select(Indicator).order_by(Indicator.category, Indicator.series_id)
        if body.search:
            # Python matching treats %/_ literally and searches all source/name/code fields.
            rows = session.scalars(query).all()
            rows = [r for r in rows if body.search.casefold() in
                    " ".join((r.name, r.series_id, r.source, r.category)).casefold()]
        else:
            rows = session.scalars(query).all()
        result.update(total=len(rows), offset=body.offset,
                      truncated=len(rows) > body.offset + body.limit,
                      indicators=[{"series_id": r.series_id, "name": r.name, "unit": r.unit,
                                   "source": r.source, "source_url": r.source_url,
                                   "frequency": r.frequency, "caliber": r.caliber,
                                   "url": research_url("/sn/indicators/" + r.series_id)}
                                  for r in rows[body.offset:body.offset + body.limit]])
        return result
    if day is None and body.operation in {Operation.summary, Operation.series, Operation.signals, Operation.brief}:
        gap = '该指标在指定范围尚无有效观测' if body.operation == Operation.series else '当前空间尚无行情交易日，未补造研究结果。'
        return result | {"data": None, "gap":gap}
    if body.operation == Operation.series:
        if not body.series_id:
            raise HTTPException(400, "读取序列必须提供已登记的 series_id，请先检索指标")
        indicator = session.get(Indicator, body.series_id)
        if indicator is None:
            raise HTTPException(404, "指标未登记")
        # Full concrete-contract support and exact observation provenance, unlike
        # the chart endpoint which intentionally omits per-contract indicators.
        versions = select(Observation.as_of, func.max(Observation.revision).label("revision")).where(
            Observation.series_id == body.series_id, Observation.as_of < day_end(day),
            Observation.status == "正常").group_by(Observation.as_of).subquery()
        query = select(Observation).join(versions, and_(Observation.as_of == versions.c.as_of,
            Observation.revision == versions.c.revision)).where(Observation.series_id == body.series_id)
        total = session.scalar(select(func.count()).select_from(versions))
        rows = list(reversed(session.scalars(query.order_by(Observation.as_of.desc()).offset(body.offset).limit(body.limit)).all()))
        from tin.provenance.service import observation_sources
        data = {"series_id": indicator.series_id, "name": indicator.name, "unit": indicator.unit,
                "total": total, "truncated": total > body.offset + len(rows), "limit": body.limit, "offset": body.offset,
                "observations": [{"observation_id": r.id, "value": r.value, "as_of": r.as_of,
                    "fetched_at": r.fetched_at, "source": r.source, "source_url": r.source_url,
                    "revision": r.revision, "caliber": r.caliber_snapshot,
                    "sources": observation_sources(session, r.id)} for r in rows]}
        return result | {"data": data, "caliber": indicator.caliber,
                         "source_url": indicator.source_url,
                         "url": research_url("/sn/indicators/" + body.series_id)}
    if body.operation == Operation.judgment:
        row = current(session, "SN")
        return result | {"data": None if row is None else {
            "id": row.id, "version": row.version, "status": row.status,
            "review_due": row.review_due, "payload": to_payload(row).model_dump(mode="json")},
            "gap": "当前账号尚无研究判断" if row is None else None,
            "url": research_url("/sn/judgment")}
    if body.operation == Operation.signals:
        own = scope_query(session, select(Judgment.id), Judgment).subquery()
        query = select(Signal).where(Signal.judgment_id.in_(select(own.c.id)),
                                      Signal.trade_date == day.isoformat()).order_by(Signal.id)
        rows = session.scalars(query).all()
        return result | {"signals": [{"id": r.id, "rule_id": r.rule_id, "state": r.state,
                                       "current_value": r.current_value, "evidence": r.evidence,
                                       "gap_note": r.gap_note} for r in rows[body.offset:body.offset + body.limit]],
                         "total": len(rows), "offset": body.offset, "truncated": len(rows) > body.offset + body.limit,
                         "url": research_url("/")}
    if body.operation == Operation.brief:
        row = brief_row
        return result | {"data": None if row is None else {
            "id": row.id, "status": row.status, "tone": row.tone, "tone_note": row.tone_note,
            "range_low": row.range_low, "range_high": row.range_high,
            "range_series_id": row.range_series_id, "stance": row.stance,
            "summary": row.summary, "claims": row.claims, "unverified": row.unverified,
            "generated_at": row.generated_at, "reviewed_at": row.reviewed_at},
            "gap": "当前账号在该日尚无简报" if row is None else None,
            "url": research_url("/")}
    if body.operation == Operation.strategies:
        rows = list_strategies(session)
        return result | {"strategies": [serialize_strategy(session, r) for r in
                                         rows[body.offset:body.offset + body.limit]],
                         "total": len(rows), "truncated": len(rows) > body.offset + body.limit,
                         "url": research_url("/sn/strategies")}
    # summary is composed exclusively from current stored facts and owned judgment/signals.
    data = board.snapshot(session, 'SN', day)
    from tin.provenance.service import observation_sources
    # Summary answers need the same exact file/cell evidence as series reads.
    for point in data.get('observations', []):
        row = latest_obs(session, point['series_id'], day_end(day))
        if row is not None:
            point['observation_id'] = row.id
            point['sources'] = observation_sources(session, row.id)
    for derived in data.get('derived', []):
        for point in derived.get('inputs', []):
            identifier = point.get('observation_id')
            if type(identifier) is int and session.get(Observation, identifier) is not None:
                point['sources'] = observation_sources(session, identifier)
    return result | {'data':data, 'url':research_url('/')}


@router.post("/api/tools/read")
def research_read(body: ReadRequest):
    identity = current_identity.get()
    if identity is None:
        raise HTTPException(401, "缺少已核验研究身份")
    if identity.workbench_id:
        from tin.integration.workbench_scope import current_workbench, workbench_session
        scope = current_workbench.get()
        if scope is None or scope.workbench_id != identity.workbench_id:
            raise HTTPException(403, '缺少当前已核验工作台范围')
        if scope.scope_version == 2:
            from tin.integration.page_facts import catalog_page,page_read_scope
            if body.operation == Operation.indicators:
                page = catalog_page(scope,offset=body.offset,limit=body.limit,search=body.search)
                return {'operation':body.operation.value,'workbench_id':scope.workbench_id,
                    'knowledge_base_sources':[{'id':source['id'],'source_type':source['source_type'],'source_id':source['source_id']} for source in scope.sources if not source['delegated']],
                    'indicators':[{**field,'name':field.get('metric') or field.get('name') or ''} for field in page['series']],
                    'total':page['total'],'offset':page['offset'],'next_offset':page['next_offset'],
                    'truncated':page['truncated'],'version':page['version'],
                    'notice':'当前直接授权来源的真实指标元数据；目录检索不代表业务口径已确认。'}
            if body.operation == Operation.series:
                if not body.series_id: raise HTTPException(400,'请先选择真实指标ID')
                if body.offset+body.limit > 2000: raise HTTPException(413,'单次研究序列窗口最多2000点，请缩小范围')
                module,identifiers,points='indicators',[body.series_id],body.offset+body.limit
            else:
                module={Operation.summary:'home',Operation.judgment:'judgments',Operation.signals:'judgments',
                    Operation.brief:'reports',Operation.strategies:'strategies'}[body.operation]
                identifiers,points=None,None
            with page_read_scope(module,series_ids=identifiers,day=body.date,points_per_series=points),workbench_session(scope) as session:
                result = read_isolated_research(session,body,identity.workbench_id)
                if body.operation == Operation.summary:
                    result['coverage'] = 'saved_workbench_bindings_only'
                    result['notice'] = '仅当前工作台已保存绑定所需的必要观测，不是来源全部历史或全库统计。'
                return result
        with workbench_session(scope) as session:
            return read_isolated_research(session, body, identity.workbench_id)
    if settings.weknora_api_url:
        raise HTTPException(409, '请先选择绑定知识库的研究工作台')
    with SessionLocal() as session:
        return read_research(session, body)


def original_result_query(session, module):
    """Use the same personally owned models as the original research pages."""
    from tin.models import Strategy
    model, stamp = {'judgments': (Judgment, Judgment.created_at),
                    'briefs': (DailyBrief, DailyBrief.generated_at),
                    'strategies': (Strategy, Strategy.created_at)}[module]
    return model, stamp, scope_query(session, select(model), model)


def research_result_counts(session, scope):
    from tin.models import DiagramTemplate
    from tin.web.workbench_artifacts import artifact_counts
    counts = artifact_counts(session, scope)
    for module in ('judgments', 'briefs', 'strategies'):
        _, _, query = original_result_query(session, module)
        counts[module] += session.scalar(select(func.count()).select_from(query.subquery()))
    # Viewing a new workspace creates an empty default canvas, not a research result.
    counts['structure'] += sum(bool(layout.get('nodes') or layout.get('edges')) for layout in
        session.scalars(select(DiagramTemplate.layout)) if isinstance(layout, dict))
    return counts


def research_result_records(session, scope, module, limit, offset):
    """Merge actual page results and generic notes, keeping source and draft status."""
    from tin.web.workbench_artifacts import artifact_query, serialize_artifact
    from tin.models.workbench import WorkbenchArtifact
    model, stamp, query = original_result_query(session, module)
    generic = artifact_query(scope, module)
    total = (session.scalar(select(func.count()).select_from(query.subquery()))
             + session.scalar(select(func.count()).select_from(generic.subquery())))
    end = offset + limit
    records = [{**serialize_artifact(row), 'origin': 'workbench_record'} for row in
        session.scalars(generic.order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id.desc()).limit(end))]
    for row in session.scalars(query.order_by(stamp.desc(), model.id.desc()).limit(end)):
        if module == 'judgments':
            payload = to_payload(row).model_dump(mode='json')
        elif module == 'briefs':
            payload = {key: getattr(row, key) for key in ('variety', 'trade_date', 'tone', 'tone_note',
                'range_low', 'range_high', 'range_series_id', 'stance', 'summary', 'claims', 'unverified',
                'reviewed_at', 'reviewed_by')}
        else:
            payload = serialize_strategy(session, row)
        created = row.generated_at if module == 'briefs' else row.created_at
        records.append({'id': row.id, 'module': module, 'origin': 'research_ui',
                        'status': row.status, 'created_at': created, 'updated_at': created,
                        'payload': payload})
    def order(item):
        timestamp = item['updated_at']
        return (timestamp.replace(tzinfo=timezone.utc).timestamp() if timestamp.tzinfo is None else timestamp.timestamp(),
                item['origin'], item['id'])
    records.sort(key=order, reverse=True)
    return {'items': records[offset:end], 'total': total, 'offset': offset, 'truncated': end < total}


def read_isolated_research(session, body: ReadRequest, workbench_id: str):
    """Actual scoped facts for model tools; never initializes legacy research."""
    from tin.web.workbenches import fact_counts, indicator_payload, observation_payload, observation_rows
    base = {"operation": body.operation.value, "workbench_id": workbench_id,
            "knowledge_base_id": session.info["knowledge_base_id"], "requested_date": body.date,
            "knowledge_base_sources": session.info.get('knowledge_base_sources', []),
            "notice": "直接读取主绑定知识库及当前授权的默认宏观知识库已确认结构化事实与本台研究成果；原表日期不声明发布时间，不代表实时行情。"}
    if body.operation == Operation.indicators:
        rows = session.scalars(select(Indicator).order_by(Indicator.series_id)).all()
        if body.search:
            rows = [row for row in rows if body.search.casefold() in
                    " ".join((row.name, row.series_id, row.source)).casefold()]
        catalog = session.info.get('knowledge_facts_catalog', {})
        return {**base, "indicators": [{**indicator_payload(r), **{key: catalog.get(r.series_id, {}).get(key)
                    for key in ('knowledge_base_id', 'knowledge_base_tenant_id', 'source_kind', 'source_version')}}
                    for r in rows[body.offset:body.offset + body.limit]],
                "total": len(rows), "offset": body.offset, "truncated": len(rows) > body.offset + body.limit}
    if body.operation == Operation.series:
        if not body.series_id:
            raise HTTPException(400, "读取序列必须先提供本工作台真实指标 ID")
        data = observation_rows(session, body.series_id, workbench_id,
                    offset=body.offset, limit=body.limit, through=body.date)
        totals = session.info.get('knowledge_facts_series_totals',{})
        if body.series_id in totals:
            data['total'] = totals[body.series_id]
            data['truncated'] = data['total'] > body.offset+len(data.get('observations',[]))
            data['next_offset'] = body.offset+len(data.get('observations',[])) if data['truncated'] else None
        return {**base,'data':data}
    if body.operation == Operation.summary:
        versions = select(Observation.series_id, Observation.as_of,
                          func.max(Observation.revision).label('revision')).where(Observation.status == '正常')
        all_revisions = select(func.count()).select_from(Observation)
        if body.date:
            versions = versions.where(Observation.as_of < day_end(body.date))
            all_revisions = all_revisions.where(Observation.as_of < day_end(body.date))
        versions = versions.group_by(Observation.series_id, Observation.as_of).subquery()
        total = session.scalar(select(func.count()).select_from(versions))
        query = select(Observation).join(versions, and_(Observation.series_id == versions.c.series_id,
            Observation.as_of == versions.c.as_of, Observation.revision == versions.c.revision)).where(
                Observation.status == '正常').order_by(Observation.as_of.desc(), Observation.series_id)
        rows = session.scalars(query.offset(body.offset).limit(body.limit)).all()
        counts = {**fact_counts(session), 'observations': total, 'current_observations': total,
                  'all_observation_revisions': session.scalar(all_revisions)}
        return {**base, "counts": counts, "observations": [
            {"series_id": row.series_id, **observation_payload(session, row, workbench_id)} for row in rows],
            'total': total, "offset": body.offset, "limit": body.limit,
            'truncated': body.offset + len(rows) < total,
            'next_offset': body.offset + len(rows) if body.offset + len(rows) < total else None}
    if body.operation in {Operation.judgment, Operation.brief, Operation.strategies}:
        from tin.integration.workbench_scope import current_workbench
        module = {Operation.judgment: "judgments", Operation.brief: "briefs", Operation.strategies: "strategies"}[body.operation]
        data = research_result_records(session, current_workbench.get(), module, body.limit, body.offset)
        return {**base, "data": data, "temporal_scope": "current_manual_records",
                "requested_date_applied": False,
                "notice": "当前本人研究记录含草稿及原页面真实状态，按保存时间列出；请求日期未用于筛选，"
                          "不代表该日历史版本、当时可知信息或交易判断回放。记录内历史引用不充当当前知识库事实。"}
    return {**base, "signals": [], "total": 0,
            "gap": "本工作台尚无已配置并计算的条件监测结果"}

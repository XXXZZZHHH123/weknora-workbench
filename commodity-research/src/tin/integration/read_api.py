"""Bounded research reads. The caller and workspace are never model arguments."""
from datetime import date as Date
from enum import Enum

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select, func, and_

from tin.compute.engine import latest_trade_date, day_end
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
    if body.date:
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
        return result | {"data": None, "gap": "当前空间尚无行情交易日，未补造研究结果。"}
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
        data = {"series_id": indicator.series_id, "name": indicator.name, "unit": indicator.unit,
                "total": total, "truncated": total > body.offset + len(rows), "limit": body.limit, "offset": body.offset,
                "observations": [{"observation_id": r.id, "value": r.value, "as_of": r.as_of,
                    "fetched_at": r.fetched_at, "source": r.source, "source_url": r.source_url,
                    "revision": r.revision, "caliber": r.caliber_snapshot} for r in rows]}
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
        me = seed_researcher(session)
        row = session.scalar(select(DailyBrief).where(DailyBrief.researcher_id == me.id,
            DailyBrief.variety == "SN", DailyBrief.trade_date == day.isoformat()))
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
    return result | {"data": board.snapshot(session, "SN", day), "url": research_url("/")}


@router.post("/api/tools/read")
def research_read(body: ReadRequest):
    if current_identity.get() is None:
        raise HTTPException(401, "缺少已核验研究身份")
    with SessionLocal() as session:
        return read_research(session, body)

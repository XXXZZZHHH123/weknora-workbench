"""采集调度（FR-3.1）：每个数据源失败重试 1 次；仍失败记入 fetch_runs 并在页面显示缺口，不中断其余采数。"""

import logging
from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from contextvars import ContextVar
from urllib.parse import urlsplit
from datetime import date, datetime, timedelta, timezone

import httpx
from sqlalchemy import select
from sqlalchemy.orm import Session

from tin.config import settings
from tin.ingest import NotPublished, cboe, cfets, fred, shfe
from tin.ingest.record import ensure_indicator, latest_index, record
from tin.ingest.shfe import Batch
from tin.models import FetchRun, TradingDay, Indicator, ObservationSource

log = logging.getLogger(__name__)
UA = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0 Safari/537.36"}
_CAPTURES = ContextVar("source_responses", default=None)


def _capture(response, captures=None, url=None):
    captures = _CAPTURES.get() if captures is None else captures
    if captures is not None:
        captures.append({"data": response.content, "source_url": url or str(response.request.url),
                         "response_url": str(response.request.url),
                         "filename": urlsplit(str(response.request.url)).path.rsplit("/",1)[-1] or "response.bin",
                         "http_status": response.status_code})


def _get(client: httpx.Client, url: str, method: str = "GET") -> httpx.Response:
    r = client.request(method, url)
    _capture(r, url=url)
    if r.status_code == 404:
        raise NotPublished(f"{url} 返回 404")
    r.raise_for_status()
    r.encoding = "utf-8"
    return r


@dataclass(frozen=True)
class Fetcher:
    name: str
    fetch: Callable[[httpx.Client, date], Batch]


def _quotes(c, d):
    return shfe.parse_quotes(_get(c, shfe.QUOTE_URL.format(d=d.strftime("%Y%m%d"))).json())


def _warrant(c, d):
    return shfe.parse_daily_warrant(_get(c, shfe.WARRANT_URL.format(d=d.strftime("%Y%m%d"))).text, d)


def _weekly(c, d):
    return shfe.parse_weekly_stock(_get(c, shfe.WEEKLY_URL.format(d=d.strftime("%Y%m%d"))).text, d)


def _fx(c, d):
    batch = cfets.parse_ccpr(_get(c, cfets.CCPR_URL, "POST").json())
    got = batch.observations[0].as_of.date()
    if got != d:
        raise NotPublished(f"中间价接口只提供当日数据，当前为 {got}，无法取 {d}")
    return batch


def _vix(c, d):
    batch = cboe.parse_vix(_get(c, cboe.VIX_URL).text, since=d - timedelta(days=10))
    if not batch.observations:
        raise NotPublished(f"VIX 截至 {d} 前 10 日无数据")
    return batch


def _fred(c, d):
    """并发下载独立序列；FRED 多 ID 下载会忽略日期窗口并返回过大的完整历史。"""
    merged = Batch()
    captures = _CAPTURES.get()

    def fetch_one(spec):
        url = fred.url_for(spec, d)
        # 独立连接避免代理环境下共享连接池在线程间阻塞。
        # 并且不要带上 UA：FRED 对带浏览器 UA 的并发请求会挂住不响应，直到读超时。
        response = httpx.get(url, timeout=15, follow_redirects=True)
        _capture(response, captures, url=url)
        response.raise_for_status()
        return fred.parse_series(response.text, spec, through=d, source_url=url)

    with ThreadPoolExecutor(max_workers=6) as pool:
        futures = {pool.submit(fetch_one, spec): spec for spec in fred.FETCHABLE_SERIES}
        for future in as_completed(futures):
            spec = futures[future]
            try:
                batch = future.result()
                merged.indicators.extend(batch.indicators)
                merged.observations.extend(batch.observations)
            except Exception as exc:  # noqa: BLE001 - 单序列失败不影响同批其他数据
                merged.errors.append(f"{spec.fred_id}: {type(exc).__name__}: {exc}")
    if not merged.observations:
        raise RuntimeError("FRED 全部序列失败：" + "；".join(merged.errors[:3]))
    return merged


FETCHERS = (
    Fetcher("shfe_quotes", _quotes),
    Fetcher("shfe_warrant", _warrant),
    Fetcher("shfe_weekly_stock", _weekly),
    Fetcher("cfets_fx", _fx),
    Fetcher("cboe_vix", _vix),
    Fetcher("fred_macro", _fred),
)


def store(session: Session, batch: Batch, *, documents=None, batch_label=None) -> int:
    for spec in batch.indicators:
        ensure_indicator(session, spec)
    if batch.trading_day:
        d, seq = batch.trading_day
        if session.get(TradingDay, d.isoformat()) is None:
            session.add(TradingDay(trade_date=d.isoformat(), year=d.year, year_seq=seq))
    fetched_at = datetime.now(timezone.utc)
    if not documents:
        return sum(record(session, o, fetched_at=fetched_at) is not None for o in batch.observations)
    from tin.provenance.service import preserve_blob, start_batch, record_with_evidence
    from tin.provenance.automatic import locate_document
    artifacts = []
    for document in documents:
        artifact = preserve_blob(session, document["data"], document["filename"], kind="automatic",
                                 source_url=document["source_url"], metadata={"http_status": document.get("http_status")})
        imported = start_batch(session, artifact, kind="automatic", key={"artifact": artifact.id,
            "url": document["source_url"], "target": batch_label})
        artifacts.append((document, artifact, imported, {}))
    documents_by_url = {}
    for item in artifacts:
        documents_by_url.setdefault(item[0]["source_url"], []).append(item)
    series_ids = list({obs.series_id for obs in batch.observations})
    cache = latest_index(session, series_ids)
    # Keep strong references for record()'s session.get checks: SQLAlchemy's
    # identity map otherwise evicts unreferenced indicators between rows.
    _indicators = session.scalars(select(Indicator).where(Indicator.series_id.in_(series_ids))).all()
    evidence_ids = set(session.scalars(select(ObservationSource.id).where(
        ObservationSource.artifact_id.in_([artifact.id for _, artifact, _, _ in artifacts]))))
    written = 0
    for obs in batch.observations:
        candidates = documents_by_url.get(obs.source_url, [])
        if len(candidates) != 1:
            raise ValueError(f"{obs.series_id} 原件不能唯一匹配，拒绝入库")
        doc, artifact, imported, prepared = candidates[0]
        _, added = record_with_evidence(session, obs, artifact=artifact,
            locator=locate_document(doc["data"], obs, prepared=prepared), batch_id=imported.id,
            mapping_version="official-parser-v1", transformation={"parser": batch_label, "response_url": doc.get("response_url")},
            cache=cache, evidence_ids=evidence_ids)
        written += added
    for _, _, imported, _ in artifacts:
        imported.status, imported.committed_at = "committed", fetched_at
    return written


def run_one(session: Session, client: httpx.Client, f: Fetcher, d: date) -> FetchRun:
    run = FetchRun(fetcher=f.name, target_date=d.isoformat(), started_at=datetime.now(timezone.utc),
                   status="running", attempts=0)
    for attempt in (1, 2):
        run.attempts = attempt
        captures = []
        marker = _CAPTURES.set(captures)
        try:
            batch = f.fetch(client, d)
            with session.begin_nested():
                run.written = store(session, batch, documents=captures, batch_label=f"{f.name}:{d}")
            run.status = "部分失败" if batch.errors else "ok"
            run.error = "；".join(batch.errors) if batch.errors else None
            break
        except NotPublished as e:
            run.status, run.error = "未发布", str(e)
            break
        except Exception as e:  # noqa: BLE001 —— 任一数据源故障都不能中断其余采数
            run.status, run.error = "失败", f"{type(e).__name__}: {e}"
            log.warning("%s %s 第 %d 次失败：%s", f.name, d, attempt, run.error)
        finally:
            _CAPTURES.reset(marker)
            # Even unpublished or malformed real responses remain auditable.
            from tin.provenance.service import preserve_blob
            for document in captures:
                try:
                    with session.begin_nested():
                        preserve_blob(session, document["data"], document["filename"], kind="automatic",
                                      source_url=document["source_url"], metadata={"http_status": document.get("http_status"),
                                      "fetcher": f.name, "target_date": d.isoformat(), "status": run.status})
                except Exception as exc:
                    run.status, run.error = "失败", f"原件留存失败 {type(exc).__name__}: {exc}"
    run.finished_at = datetime.now(timezone.utc)
    session.add(run)
    session.commit()
    return run


def fetch_all(session: Session, d: date, only: set[str] | None = None) -> list[FetchRun]:
    with httpx.Client(headers=UA, timeout=settings.http_timeout, follow_redirects=True) as client:
        return [run_one(session, client, f, d) for f in FETCHERS if only is None or f.name in only]

"""JSON cold-start flow bound to a verified workbench and its actual sources."""
import json
import hashlib
from itertools import islice
from datetime import date, datetime, time, timezone
from typing import Literal
from urllib.parse import quote, urlsplit

import httpx
from fastapi import APIRouter, Depends, HTTPException, Query
from fastapi.responses import FileResponse
from pydantic import BaseModel, ConfigDict, Field, StrictBool
from sqlalchemy import and_, func, select
from starlette.concurrency import run_in_threadpool

from tin.config import SHANGHAI, settings
from tin.integration.workbench_scope import WorkbenchScope, go_request, verified_workbench, workbench_session
from tin.models import Indicator, Observation, ObservationSource, SourceArtifact, SourceBatch, SourceMapping
from tin.provenance import bridge
from tin.provenance.service import artifact_path, stable_hash, verified_artifact_bytes
from tin.schemas.caliber import Caliber

router = APIRouter(prefix="/api/workbenches/{workbench_id}")


def _write(scope):
    if not scope.identity.can_write:
        raise HTTPException(403, "当前空间角色只有读取权限")


def _structured_catalog(operation, arguments):
    try:
        return bridge.structured_request("POST", "/api/tools/read",
                                         {"operation": operation, "arguments": arguments}).json()
    except (ValueError, httpx.HTTPError):
        raise HTTPException(503, "结构化来源暂时不可读，请稍后重试") from None


async def _native_source_page(scope, page, page_size):
    result = await go_request(scope, "/api/v1/knowledge-bases/" + quote(scope.knowledge_base_id, safe="")
                              + "/knowledge", params={"page": page, "page_size": page_size})
    sources = result.get("data")
    if (not isinstance(sources, list) or type(result.get("total")) is not int or result['total'] < 0
            or any(not isinstance(s, dict) or s.get("knowledge_base_id") != scope.knowledge_base_id
                   or not isinstance(s.get("id"), str) or not s['id'] for s in sources)):
        raise HTTPException(503, "知识库来源信息不完整，未使用其他范围替代")
    return result


async def _all_native_sources(scope):
    """A request-local inventory, never a cross-user or cross-request cache."""
    sources, page = [], 1
    while True:
        result = await _native_source_page(scope, page, 200)
        sources.extend(result['data'])
        if len(sources) >= result['total']:
            return sources
        if not result['data'] or page >= 500:
            raise HTTPException(503, "知识库来源目录未完整读取，请稍后分批核对")
        page += 1


def _current_native_import(source, records, scope):
    if source.get('parse_status') in {'deleting', 'deleted'}:
        return None
    matching = []
    for record in records:
        if not isinstance(record, dict) or record.get('use_reviewed_mappings') is not False:
            continue
        bindings = record.get('native_source_bindings') or []
        if any(isinstance(binding, dict) and binding.get('source_knowledge_id') == source['id']
               and binding.get('file_hash') == source.get('file_hash') and source.get('file_hash')
               and binding.get('knowledge_base_id') == scope.knowledge_base_id
               and binding.get('tenant_id') == scope.knowledge_base_tenant_id for binding in bindings):
            matching.append(record)
    return max(matching, key=lambda row: str(row.get('updated_at', ''))) if matching else None


def _current_datasets(sources, records, catalog, scope):
    imports = {}
    for source in sources:
        record = _current_native_import(source, records, scope)
        if record and record.get('import_id') and record.get('dataset_id'):
            imports[record['import_id']] = record
    current, history = [], []
    for dataset in catalog:
        if not isinstance(dataset, dict) or dataset.get('use_reviewed_mappings', False) is not False:
            continue
        record = next((row for row in imports.values() if row['dataset_id'] == dataset.get('dataset_id')
                       and dataset.get('import_id', row['import_id']) == row['import_id']), None)
        if record is not None:
            current.append({**dataset, 'import_id': record['import_id'], 'use_reviewed_mappings': False})
        elif dataset.get('import_id') in imports and dataset.get('use_reviewed_mappings', False) is False:
            history.append({**dataset, 'use_reviewed_mappings': False})
    return current, history


async def source_inventory(scope: WorkbenchScope, page=1, page_size=100):
    result = await _native_source_page(scope, page, page_size)
    sources = result['data']
    allowed = {"id", "title", "file_name", "file_type", "file_size", "file_hash", "folder_path",
               "parse_status", "enable_status", "error_message", "updated_at", "created_at", "type"}
    structured_error = None
    try:
        datasets = await run_in_threadpool(_structured_catalog, "list_datasets", {})
        if not isinstance(datasets, dict) or not isinstance(datasets.get("datasets"), list):
            raise HTTPException(503, "结构化目录信息不完整")
    except HTTPException as exc:
        datasets, structured_error = {"datasets": []}, str(exc.detail)
    source_imports, review_error = [], None
    if structured_error is None:
        try:
            records = await _structured_user(scope, "GET", "/api/imports")
            if not isinstance(records, dict) or not isinstance(records.get('imports'), list):
                raise HTTPException(503, "原件核对目录信息不完整")
            source_imports = records['imports']
        except HTTPException as exc:
            review_error = str(exc.detail)
    with workbench_session(scope) as session:
        roles = {row.config["knowledge_id"]: row.config["source_role"] for row in
                 session.scalars(select(SourceMapping).where(SourceMapping.id.startswith("role-"))).all() if row.config.get("knowledge_id")
                 and row.config.get("source_role")}
    current, history = _current_datasets(sources, source_imports, datasets['datasets'], scope)
    return {"knowledge_base_id": scope.knowledge_base_id,
            "sources": [{**{k: v for k, v in row.items() if k in allowed},
                         "source_role": roles.get(row["id"], "unassigned")} for row in sources],
            "datasets": current, "dataset_versions": history, "total": result["total"], "page": page,
            "page_size": page_size, "has_more": result["total"] > page * page_size,
            "structured_error": structured_error, "source_imports": source_imports,
            "review_error": review_error}


def indicator_payload(row):
    return {"series_id": row.series_id, "name": row.name, "unit": row.unit, "frequency": row.frequency,
            "caliber": row.caliber, "source": row.source, "status": row.status,
            "default_entry_time": row.default_entry_time.isoformat() if row.default_entry_time else None,
            "note": row.note}


def fact_counts(session):
    return {"indicators": session.scalar(select(func.count()).select_from(Indicator)),
            "observations": session.scalar(select(func.count()).select_from(Observation))}


def source_review_progress(inventory, scope):
    excel = [source for source in inventory['sources'] if str(source.get('file_type', '')).lower().lstrip('.')
             in {'xlsx', 'xls', 'xlsm', 'csv', 'tsv'}]
    eligible = [source for source in excel if source.get('source_role') in {'observations', 'unassigned'}]
    pending_sources = pending_sheets = unknown_sources = 0
    for source in eligible:
        record = _current_native_import(source, inventory['source_imports'], scope)
        if record is None:
            pending_sources += 1; unknown_sources += 1
            continue
        summary = record.get('summary') or {}
        if not isinstance(summary, dict) or not summary or any(type(value) is not int or value < 0 for value in summary.values()) or not sum(summary.values()):
            pending_sources += 1; unknown_sources += 1
            continue
        pending = sum(value for key, value in summary.items()
                      if key not in {'confirmed', 'template_ready', 'auto_ready', 'empty'}
                      and type(value) is int and value > 0)
        pending_sheets += pending
        if pending or source.get('source_role') == 'unassigned':
            pending_sources += 1
    return {'excel_pending': pending_sources, 'pending_sheet_count': pending_sheets,
            'unknown_source_count': unknown_sources, 'observation_source_count': len(eligible),
            'review_counts_partial': bool(unknown_sources or inventory['has_more']
                                         or inventory['structured_error'] or inventory['review_error'])}


@router.get("/overview")
async def overview(scope: WorkbenchScope = Depends(verified_workbench)):
    inventory = await source_inventory(scope, page_size=200)
    with workbench_session(scope) as session:
        counts = fact_counts(session)
        from tin.web.workbench_artifacts import artifact_counts
        research_counts = artifact_counts(session, scope)
    sources = inventory["sources"]
    excel = [s for s in sources if str(s.get("file_type", "")).lower().lstrip(".") in {"xlsx", "xls", "xlsm", "csv", "tsv"}]
    ready_docs = sum(s.get("parse_status") == "completed" for s in sources if s not in excel)
    review = source_review_progress(inventory, scope)
    counts.update(sources=inventory["total"], documents_ready=ready_docs,
                  datasets=len(inventory["datasets"]), counts_partial=inventory["has_more"])
    counts.update(review)
    prepared = counts["indicators"] > 0 or ready_docs > 0 or any(research_counts.values())
    research_ready = any(research_counts.values())
    review_status = ('ready' if counts['indicators'] and not counts['excel_pending'] and not counts['review_counts_partial']
                     else 'partially_ready' if counts['indicators'] else 'pending')
    tasks = [{"id": "sources", "status": "ready" if counts["sources"] else "pending", "count": counts["sources"]},
             {"id": "review", "status": review_status, "count": counts["indicators"],
              "pending_sources": counts['excel_pending'], "pending_sheets": counts['pending_sheet_count'],
              "optional": not counts['observation_source_count']},
             {"id": "research", "status": "ready" if research_ready else "pending", "optional": True}]
    status = "partially_ready" if prepared else ("preparing" if counts["sources"] else "empty")
    return {"workbench": scope.metadata, "counts": counts, "readiness": {"status": status, "tasks": tasks},
            "modules": [{"id": "data", "status": "ready" if counts["indicators"] else "pending",
                         "reason": None if counts["indicators"] else "先确认并绑定本工作台的真实来源字段"},
                        {"id": "documents", "status": "ready" if ready_docs else "pending",
                         "reason": None if ready_docs else "等待本知识库文档解析"}],
            "research_counts": research_counts, "structured_error": inventory["structured_error"],
            "review_error": inventory['review_error'],
            "model": {"calls_enabled": False, "free_quota_confirmed": False}}


@router.get("/sources")
async def sources(page: int = Query(1, ge=1), page_size: int = Query(100, ge=1, le=200),
                  scope: WorkbenchScope = Depends(verified_workbench)):
    return await source_inventory(scope, page, page_size)


@router.get("/datasets/{dataset_id}/series")
async def source_series(dataset_id: str, offset: int = Query(0, ge=0), limit: int = Query(100, ge=1, le=200),
                  scope: WorkbenchScope = Depends(verified_workbench)):
    if not dataset_id or len(dataset_id) > 128:
        raise HTTPException(400, "数据集编号无效")
    catalog = await run_in_threadpool(_structured_catalog, "list_datasets", {})
    records = await _structured_user(scope, 'GET', '/api/imports')
    native_sources = await _all_native_sources(scope)
    if (not isinstance(catalog, dict) or not isinstance(catalog.get('datasets'), list)
            or not isinstance(records, dict) or not isinstance(records.get('imports'), list)):
        raise HTTPException(503, "当前来源核对信息不完整")
    current, history = _current_datasets(native_sources, records['imports'], catalog['datasets'], scope)
    if (not any(row.get('dataset_id') == dataset_id for row in current + history)
            or not any(row.get('dataset_id') == dataset_id and row.get('use_reviewed_mappings') is False
                       for row in catalog['datasets'])):
        raise HTTPException(409, "该版本缺少本知识库当前原件的独立核对证据，请重新扫描核对")
    result = await run_in_threadpool(_structured_catalog, "list_series",
                                    {"dataset_id": dataset_id, "offset": offset, "limit": limit})
    if not isinstance(result, dict) or result.get("dataset_id") != dataset_id or not isinstance(result.get("series"), list):
        raise HTTPException(409, "结构化字段与请求版本不一致")
    return result


class PreviewInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    dataset_id: str = Field(min_length=1, max_length=128)
    series_id: str = Field(min_length=1, max_length=128)
    source_role: Literal["observations", "acquisition-plan", "method", "specification"] = "observations"


class ConfirmInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    batch_id: str = Field(min_length=1, max_length=64)
    source_unit: str = Field(min_length=1, max_length=16)
    target_unit: str = Field(min_length=1, max_length=16)
    factor: float = Field(default=1, gt=0, allow_inf_nan=False, strict=True)
    caliber: Caliber
    default_entry_time: time | None = None
    note: str = Field(min_length=1, max_length=2000)
    name: str | None = Field(default=None, min_length=1, max_length=120)
    confirmed: StrictBool = False
    future_dates_confirmed: StrictBool = False
    source_role: Literal["observations", "acquisition-plan", "method", "specification"] = "observations"


def _future_count(exported, today=None):
    today = today or datetime.now(SHANGHAI).date()
    return sum(1 for row in exported["rows"] if row.get("value_status") in {"valid", "ok", "正常"}
               and row.get("value") is not None and isinstance(row.get("date"), str)
               and _after_day(row["date"], today))


def _after_day(value, day):
    try:
        return date.fromisoformat(value[:10]) > day
    except ValueError:
        return False


def _preview(scope, body, native_sources):
    _write(scope)
    if body.source_role != "observations":
        raise HTTPException(409, "当前来源用途不是数值观测，不能写入研究事实")
    try:
        exported = bridge.full_export(body.dataset_id, body.series_id)
        if (exported["manifest"].get("dataset_id") != body.dataset_id
                or exported["series"].get("series_id") != body.series_id):
            raise ValueError("真实快照与请求的数据集或字段不一致")
        if exported['manifest'].get('use_reviewed_mappings') is not False:
            raise ValueError("本工作台只接受从原件独立核对的快照，请先重新扫描并确认字段")
        with workbench_session(scope) as session:
            preview = bridge.preview_bridge(session, body.dataset_id, body.series_id,
                                            exported=exported, native_sources=native_sources)
            _require_observation_role(session, session.get(SourceArtifact, preview["artifact_id"]))
            today = datetime.now(SHANGHAI).date()
            future = _future_count(exported, today)
            batch = session.get(SourceBatch, preview["batch_id"])
            batch.mapping = {**batch.mapping, "knowledge_base_id": scope.knowledge_base_id,
                             "future_numeric_count": future, "source_role": body.source_role}
            session.commit()
            future_rows = list(islice(({key: row.get(key) for key in ("date", "period", "value", "source_cell", "date_cell", "value_status")}
                           for row in exported['rows'] if row.get('value_status') in {'valid', 'ok', '正常'}
                           and row.get('value') is not None and isinstance(row.get('date'), str)
                           and _after_day(row['date'], today)), 20))
            return {**preview, "future_numeric_count": future, "future_rows": future_rows,
                    "future_rows_truncated": future > len(future_rows)}
    except (ValueError, httpx.HTTPError, KeyError, TypeError, OSError) as exc:
        raise HTTPException(409, str(exc) if isinstance(exc, ValueError) else "来源读取不完整，未创建候选") from None


@router.post("/imports/preview")
async def preview(body: PreviewInput, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    native_sources = await _all_native_sources(scope)
    return await run_in_threadpool(_preview, scope, body, native_sources)


def _batch_snapshot(session, scope, batch_id):
    batch = session.get(SourceBatch, batch_id)
    if batch is None or batch.kind != "bridge":
        raise HTTPException(404, "本工作台没有此导入候选")
    if batch.actor != scope.identity.user_id:
        raise HTTPException(403, "导入候选不属于当前账号")
    if batch.mapping.get("knowledge_base_id") != scope.knowledge_base_id:
        raise HTTPException(403, "候选来源与当前知识库不一致")
    artifact = session.get(SourceArtifact, batch.mapping.get("external_snapshot_id"))
    if artifact is None:
        raise HTTPException(409, "候选缺少冻结的来源快照")
    try:
        exported = json.loads(verified_artifact_bytes(session, artifact))
    except (ValueError, OSError):
        raise HTTPException(409, "来源快照缺失或哈希发生变化") from None
    if exported.get('manifest', {}).get('use_reviewed_mappings') is not False:
        raise HTTPException(409, "冻结候选缺少从原件独立核对的证据，请重新扫描并预览")
    return batch, exported


def _confirm(scope, body, native_sources):
    _write(scope)
    if not body.confirmed:
        raise HTTPException(400, "请先确认真实字段、单位与口径")
    if not body.note.strip() or (body.name is not None and not body.name.strip()):
        raise HTTPException(400, "请填写明确的来源说明与指标名称")
    if body.default_entry_time is not None and body.default_entry_time.tzinfo is not None:
        raise HTTPException(400, "登记时刻应为当前工作台本地时间，不含额外时区偏移")
    with workbench_session(scope) as session:
        batch, exported = _batch_snapshot(session, scope, body.batch_id)
        artifact = session.get(SourceArtifact, batch.artifact_id)
        _require_observation_role(session, artifact)
        original = verified_artifact_bytes(session, artifact)
        current_bindings = bridge.native_source_bindings(scope, original, native_sources)
        if not any(binding in artifact.details.get('native_source_bindings', []) for binding in current_bindings):
            raise HTTPException(409, "冻结原件与当前知识库的原生来源授权证据不一致，请重新核对")
        if body.source_role != "observations" or batch.mapping.get("source_role") != "observations":
            raise HTTPException(409, "当前来源用途不是已确认的数值观测")
        spec = exported["series"]
        if not spec.get("unit") or body.source_unit != spec["unit"]:
            raise HTTPException(409, "来源单位必须与冻结字段元信息一致")
        if body.target_unit != body.source_unit or body.factor != 1:
            raise HTTPException(409, "当前新指标导入保留原单位，不能未经单位换算核验修改数值")
        if _future_count(exported) and not body.future_dates_confirmed:
            raise HTTPException(409, "含晚于当前日期的有效数值，请先核对期间与时间口径")
        if spec.get("date_mode") in {"none", "period"}:
            raise HTTPException(409, "字段尚无明确日期映射，请先核对结构化期间")
        if body.default_entry_time is None and any(isinstance(r.get("date"), str) and len(r["date"]) <= 10
                                                   for r in exported["rows"] if r.get("value") is not None):
            raise HTTPException(409, "原件只有日期，请明确选择登记时刻，不能自动推定发布时点")
        identifier = "field-" + stable_hash({"dataset_id": batch.mapping["dataset_id"],
            "series_id": batch.mapping["external_series_id"], "unit": body.target_unit,
            "caliber": body.caliber.dump()})[:48]
        if batch.status == "committed" and batch.mapping.get("research_series_id") != identifier:
            raise HTTPException(409, "已导入候选的登记口径不能在重试时改变，请重新核对新候选")
        indicator = session.get(Indicator, identifier)
        if indicator is not None and (indicator.default_entry_time != body.default_entry_time
                                      or indicator.frequency != str(spec.get('frequency') or '未注明')
                                      or indicator.unit != body.target_unit or indicator.caliber != body.caliber.dump()):
            raise HTTPException(409, "指标已有不同的登记时刻、频率或口径，请重新核对，未改写原观测")
        if indicator is None:
            source_name = str(spec.get("metric") or spec.get("name") or "").strip()
            if not source_name:
                raise HTTPException(409, "真实字段缺少名称，请先核对源字段")
            indicator = Indicator(series_id=identifier, name=body.name or source_name[:120],
                variety="", category="来源字段", caliber=body.caliber.dump(), unit=body.target_unit,
                source="结构化文件", source_url=None, frequency=str(spec.get("frequency") or "未注明"),
                fetch_mode="manual", owner=scope.identity.user_id, note=body.note,
                default_entry_time=body.default_entry_time)
            session.add(indicator); session.flush()
        mapping = {"research_series_id": identifier, "source_unit": body.source_unit,
                   "target_unit": body.target_unit, "factor": body.factor, "note": body.note}
        try:
            result = bridge.apply_bridge(session, batch.id, mapping, allow_conflicts=False, prefetch=True)
            if result.get("status") == "needs_confirmation":
                session.rollback()
                raise HTTPException(409, {"message": "来源冲突尚未处理，未覆盖已有值", "conflicts": result.get("conflicts", [])})
            batch.mapping = {**batch.mapping, "research_series_id": identifier}
            session.commit()
            return {"indicator": indicator_payload(indicator), "result": result}
        except (ValueError, OSError) as exc:
            session.rollback()
            raise HTTPException(409, str(exc)) from None


@router.post("/imports/confirm")
@router.post("/indicators")
async def confirm(body: ConfirmInput, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    native_sources = await _all_native_sources(scope)
    return await run_in_threadpool(_confirm, scope, body, native_sources)


class PreviewBatch(BaseModel):
    model_config = ConfigDict(extra="forbid")
    items: list[PreviewInput] = Field(min_length=1, max_length=100)


class ConfirmBatch(BaseModel):
    model_config = ConfigDict(extra="forbid")
    items: list[ConfirmInput] = Field(min_length=1, max_length=100)


@router.post("/imports/preview-batch")
async def preview_batch(body: PreviewBatch, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    native_sources = await _all_native_sources(scope)
    rows = []
    for item in body.items:
        try:
            rows.append({"status": "ready", "preview": await run_in_threadpool(_preview, scope, item, native_sources)})
        except HTTPException as exc:
            rows.append({"status": "failed", "dataset_id": item.dataset_id,
                         "series_id": item.series_id, "detail": exc.detail})
    return {"items": rows}


@router.post("/imports/confirm-batch")
async def confirm_batch(body: ConfirmBatch, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    native_sources = await _all_native_sources(scope)
    rows = []
    for item in body.items:
        try:
            rows.append({"status": "committed", **await run_in_threadpool(_confirm, scope, item, native_sources)})
        except HTTPException as exc:
            rows.append({"status": "failed", "batch_id": item.batch_id, "detail": exc.detail})
    return {"items": rows}


@router.get("/indicators")
def indicators(offset: int = Query(0, ge=0), limit: int = Query(100, ge=1, le=200),
               scope: WorkbenchScope = Depends(verified_workbench)):
    with workbench_session(scope) as session:
        rows = session.scalars(select(Indicator).order_by(Indicator.series_id).offset(offset).limit(limit)).all()
        total = session.scalar(select(func.count()).select_from(Indicator))
        return {"indicators": [indicator_payload(row) for row in rows], "total": total,
                "offset": offset, "next_offset": offset + len(rows) if total > offset + len(rows) else None}


def observation_payload(session, row, workbench_id):
    evidence = session.scalars(select(ObservationSource).where(ObservationSource.observation_id == row.id)).all()
    return {"observation_id": row.id, "value": row.value, "as_of": row.as_of,
            "revision": row.revision, "caliber": row.caliber_snapshot,
            "sources": [{"artifact_id": e.artifact_id, "sha256": e.artifact_id,
                         "locator": e.locator, "mapping_version": e.mapping_version,
                         "transformation": e.transformation, "batch_id": e.batch_id,
                         "download_url": f"/research/api/workbenches/{workbench_id}/sources/{e.artifact_id}/download"}
                        for e in evidence]}


def observation_rows(session, series_id, workbench_id, *, offset=0, limit=100, through=None):
    indicator = session.get(Indicator, series_id)
    if indicator is None:
        raise HTTPException(404, "本工作台没有此指标")
    versions = select(Observation.as_of, func.max(Observation.revision).label("revision")).where(
        Observation.series_id == series_id, Observation.status == "正常")
    if through is not None:
        from tin.compute.engine import day_end
        versions = versions.where(Observation.as_of < day_end(through))
    versions = versions.group_by(Observation.as_of).subquery()
    query = select(Observation).join(versions, and_(Observation.as_of == versions.c.as_of,
        Observation.revision == versions.c.revision)).where(Observation.series_id == series_id)
    total = session.scalar(select(func.count()).select_from(versions))
    rows = session.scalars(query.order_by(Observation.as_of.desc()).offset(offset).limit(limit)).all()
    return {"indicator": indicator_payload(indicator), "observations": [observation_payload(session, r, workbench_id) for r in reversed(rows)],
            "total": total, "offset": offset, "truncated": total > offset + len(rows),
            "next_offset": offset + len(rows) if total > offset + len(rows) else None}


@router.get("/indicators/{series_id}/observations")
def observations(series_id: str, offset: int = Query(0, ge=0), limit: int = Query(100, ge=1, le=200),
                 scope: WorkbenchScope = Depends(verified_workbench)):
    with workbench_session(scope) as session:
        return observation_rows(session, series_id, scope.workbench_id, offset=offset, limit=limit)


@router.get("/sources/{artifact_id}/download")
async def source_download(artifact_id: str, scope: WorkbenchScope = Depends(verified_workbench)):
    with workbench_session(scope) as session:
        artifact = session.get(SourceArtifact, artifact_id)
        if artifact is None:
            raise HTTPException(404, "本工作台没有此来源")
        try:
            verified_artifact_bytes(session, artifact)
        except (ValueError, OSError):
            raise HTTPException(409, "原件缺失或哈希改变，未提供替代文件") from None
        from tin.web.workbench_artifact_evidence import authorize_frozen_download
        await authorize_frozen_download(session, scope, artifact)
        return FileResponse(artifact_path(session, artifact), filename=artifact.filename,
                            media_type=artifact.media_type, headers={"X-Source-Sha256": artifact.id})


async def _structured_user(scope, method, path, *, body=None, params=None):
    parsed = urlsplit(settings.structured_data_url)
    if (parsed.scheme not in {"http", "https"} or not parsed.netloc or parsed.path not in {"", "/"}
            or parsed.username or parsed.query or parsed.fragment):
        raise HTTPException(503, "结构化服务地址配置无效")
    try:
        async with httpx.AsyncClient(timeout=120, follow_redirects=False, trust_env=False) as client:
            response = await client.request(method, settings.structured_data_url.rstrip("/") + path,
                json=body, params=params, headers={"Authorization": scope.authorization,
                    "X-Tenant-ID": str(scope.identity.tenant_id), "X-Knowledge-Base-ID": scope.knowledge_base_id,
                    "Origin": f"{parsed.scheme}://{parsed.netloc}"})
        if response.status_code != 200:
            detail = response.json().get("detail", "结构化处理失败")
            raise HTTPException(response.status_code if response.status_code in {400, 401, 403, 404, 409, 413, 422} else 503, detail)
        return response.json()
    except HTTPException:
        raise
    except (httpx.HTTPError, ValueError):
        raise HTTPException(503, "结构化处理暂时不可用，请稍后重试") from None


@router.post("/sources/{knowledge_id}/scan")
async def scan_source(knowledge_id: str, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    return await _structured_user(scope, "POST", "/api/import-knowledge",
        body={"source_knowledge_id": knowledge_id, "restore_reviewed_state": False})


class SourceRoleInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    source_role: Literal["observations", "acquisition-plan", "method", "specification"]


def _require_observation_role(session, artifact):
    try:
        fingerprints = {artifact.id, hashlib.md5(verified_artifact_bytes(session, artifact)).hexdigest()}
    except (ValueError, OSError):
        raise HTTPException(409, "来源原件缺失或哈希发生变化，未读取替代文件") from None
    if any(row.config.get("source_file_hash") in fingerprints
           and row.config.get("source_role") not in {None, "observations"}
           for row in session.scalars(select(SourceMapping).where(SourceMapping.id.startswith("role-"))).all()):
        raise HTTPException(409, "该原件已确认用于计划或文档，不能当作观测导入")


@router.put("/sources/{knowledge_id}/role")
async def source_role(knowledge_id: str, body: SourceRoleInput, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    document = await go_request(scope, "/api/v1/knowledge/" + quote(knowledge_id, safe=""))
    source = document.get("data")
    if not isinstance(source, dict) or source.get("id") != knowledge_id or source.get("knowledge_base_id") != scope.knowledge_base_id:
        raise HTTPException(403, "来源不属于当前工作台知识库")
    with workbench_session(scope) as session:
        if body.source_role != "observations":
            committed = select(SourceBatch.artifact_id).where(SourceBatch.status == "committed", SourceBatch.kind == "bridge")
            for artifact in session.scalars(select(SourceArtifact).where(SourceArtifact.id.in_(committed))):
                try:
                    fingerprints = {artifact.id, hashlib.md5(verified_artifact_bytes(session, artifact)).hexdigest()}
                except (ValueError, OSError):
                    raise HTTPException(409, "已绑定原件缺失或哈希变化，请先核对来源") from None
                if source.get("file_hash") in fingerprints:
                    raise HTTPException(409, "该原件已有已确认观测绑定，请先处理绑定后再改变来源用途")
        identifier = "role-" + stable_hash({"knowledge_id": knowledge_id, "kb": scope.knowledge_base_id})[:48]
        row = session.get(SourceMapping, identifier)
        config = {"knowledge_id": knowledge_id, "source_file_hash": source.get("file_hash"),
                  "source_role": body.source_role}
        if row is None:
            row = SourceMapping(id=identifier, config=config, confirmed_by=scope.identity.user_id,
                                confirmed_at=datetime.now(timezone.utc), version=1)
            session.add(row)
        else:
            row.config, row.version = config, row.version + 1
            row.confirmed_by, row.confirmed_at = scope.identity.user_id, datetime.now(timezone.utc)
        session.commit()
    return {"knowledge_id": knowledge_id, "source_role": body.source_role}


@router.get("/scan-jobs/{job_id}")
async def scan_job(job_id: str, scope: WorkbenchScope = Depends(verified_workbench)):
    return await _structured_user(scope, "GET", "/api/jobs/" + quote(job_id, safe=""))


@router.get("/source-imports")
async def source_imports(scope: WorkbenchScope = Depends(verified_workbench)):
    return await _structured_user(scope, "GET", "/api/imports")


@router.get("/source-imports/{import_id}")
async def source_import(import_id: str, scope: WorkbenchScope = Depends(verified_workbench)):
    return await _structured_user(scope, "GET", "/api/imports/" + quote(import_id, safe=""))


@router.get("/source-imports/{import_id}/sheet")
async def source_sheet(import_id: str, name: str = Query(min_length=1, max_length=254),
                       scope: WorkbenchScope = Depends(verified_workbench)):
    return await _structured_user(scope, "GET", "/api/imports/" + quote(import_id, safe="") + "/sheet", params={"name": name})


class SheetMapping(BaseModel):
    model_config = ConfigDict(extra="forbid")
    mapping: dict
    save_template: bool = False
    source_role: Literal["observations", "acquisition-plan", "method", "specification"] = "observations"


def _sheet_mapping_body(body):
    if body.source_role != "observations":
        raise HTTPException(409, "当前来源用途不是数值观测，不进入观测映射确认")
    return body.model_dump(exclude={"source_role"})


@router.post("/source-imports/{import_id}/preview")
async def preview_sheet(import_id: str, body: SheetMapping, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    return await _structured_user(scope, "POST", "/api/imports/" + quote(import_id, safe="") + "/preview", body=_sheet_mapping_body(body))


@router.post("/source-imports/{import_id}/sample-preview")
async def sample_preview_sheet(import_id: str, body: SheetMapping, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    return await _structured_user(scope, "POST", "/api/imports/" + quote(import_id, safe="") + "/sample-preview", body=_sheet_mapping_body(body))


@router.post("/source-imports/{import_id}/confirm")
async def confirm_sheet(import_id: str, body: SheetMapping, scope: WorkbenchScope = Depends(verified_workbench)):
    _write(scope)
    return await _structured_user(scope, "POST", "/api/imports/" + quote(import_id, safe="") + "/confirm", body=_sheet_mapping_body(body))

"""Personal research cards, kept separate from executable trade plans and facts."""
from datetime import date
import json
from typing import Literal

from fastapi import APIRouter, HTTPException, Query, Request
from fastapi.encoders import jsonable_encoder
from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator
from sqlalchemy import func, select

from tin.integration.page_facts import page_read_scope
from tin.integration.workbench_scope import artifact_session, current_workbench, require_shared_write
from tin.models.workbench import WorkbenchArtifact
from tin.web.workbench_artifacts import (
    ArtifactInput, ArtifactModule, artifact_query, create_artifact, serialize_artifact, update_artifact,
)

FORMAT = "research-strategy-card.v1"
MAX_REQUEST_BYTES = 128 * 1024
DIRECTION_LABELS = {"bull": "偏多观察", "bear": "偏空观察", "range": "区间观察", "observe": "等待验证"}
router = APIRouter(prefix="/api/sn/research-strategies")


class ResearchStrategyCard(BaseModel):
    model_config = ConfigDict(extra="forbid")
    format: Literal["research-strategy-card.v1"] = FORMAT
    title: str = Field(min_length=1, max_length=120)
    direction: Literal["bull", "bear", "range", "observe"] = "observe"
    thesis: str = Field(min_length=1, max_length=8000)
    watch_for: str = Field(min_length=1, max_length=8000)
    trigger: str = Field(min_length=1, max_length=4000)
    invalidation: str = Field(min_length=1, max_length=4000)
    review_date: date
    original_text: str = Field(default="", max_length=24000)
    reference: "StrategyReference | None" = None
    judgment_reference: "JudgmentReference | None" = None
    response_action: str | None = Field(default=None, max_length=4000)

    @field_validator("review_date", mode="before")
    @classmethod
    def explicit_review_date(cls, value):
        if type(value) is date:
            return value
        if not isinstance(value, str) or date.fromisoformat(value).isoformat() != value:
            raise ValueError("Review date must be YYYY-MM-DD")
        return value


class CardInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    card: ResearchStrategyCard
    source_ids: list[str] = Field(default_factory=list, max_length=100)
    revision: int | None = Field(default=None, ge=1, strict=True)
    acknowledge_observation: bool = Field(default=False, strict=True)


class StrategyReference(BaseModel):
    model_config = ConfigDict(extra="forbid")
    kind: Literal["strategy"] = "strategy"
    id: str = Field(min_length=1, max_length=128)
    revision: int = Field(ge=1, strict=True)


class JudgmentReference(BaseModel):
    model_config = ConfigDict(extra="forbid")
    kind: Literal["judgment"] = "judgment"
    id: str = Field(min_length=1, max_length=128)
    revision: int = Field(ge=1, strict=True)


ResearchStrategyCard.model_rebuild()
CardInput.model_rebuild()


def require_card_scope():
    scope = current_workbench.get()
    if scope is None:
        raise HTTPException(409, "请先进入已授权的研究工作台")
    if scope.restricted or "strategies" not in scope.view_modules:
        raise HTTPException(403, "此来源授权不开放个人研究策略卡")
    return scope


def _reject_constant(value):
    raise ValueError("Non-finite JSON is not accepted")


def _unique_pairs(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("Duplicate JSON property")
        value[key] = item
    return value


async def read_input(request):
    raw = bytearray()
    async for chunk in request.stream():
        raw.extend(chunk)
        if len(raw) > MAX_REQUEST_BYTES:
            raise HTTPException(413, "研究策略卡超过128 KiB，请缩短原文后重试")
    try:
        data = json.loads(raw.decode("utf-8-sig"), object_pairs_hook=_unique_pairs,
                          parse_constant=_reject_constant)
        return CardInput.model_validate(data)
    except (UnicodeDecodeError, ValueError, TypeError, ValidationError):
        raise HTTPException(422, "请完整填写研究假设、观察依据、触发、失效与复核日期；导入文件须符合研究策略卡格式") from None


def serialize_card(row):
    result = {key: row[key] for key in ("id", "title", "status", "source_ids", "source_snapshots",
                                       "revision", "created_at", "updated_at")}
    try:
        card = ResearchStrategyCard.model_validate_json(row["body"])
        result.update(card=card.model_dump(mode="json", exclude_none=True), direction_label=DIRECTION_LABELS[card.direction], legacy_body=None)
    except (ValidationError, ValueError, TypeError):
        result.update(card=None, direction_label="待整理", legacy_body=row["body"])
    result["evidence_notice"] = "人工研究草稿；关联原件用于追溯，正文与数字尚未由系统逐项核验。"
    # The Jinja page embeds this same object with ``tojson`` before it ever
    # passes through FastAPI's response encoder. Preserve actual timestamps in
    # ISO format here so both the page and API use the same JSON-safe view.
    return jsonable_encoder(result)


def _visible_card_ids(session, scope):
    """Scope filtering precedes counts and paging; bodies are not a directory payload."""
    from tin.web.research_references import record_scope_status
    columns = [WorkbenchArtifact.id]
    if scope.metadata.get("workspace_mode") == "legacy":
        columns.append(WorkbenchArtifact.source_snapshots)
    rows = list(session.execute(artifact_query(scope, ArtifactModule.strategies.value).with_only_columns(*columns)
        .order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id).limit(1001)).mappings())
    if len(rows) > 1000:
        raise HTTPException(413, "策略方案目录超过1000项，请先整理历史记录")
    result = []
    for row in rows:
        state = record_scope_status(session, scope, "artifact", row["id"], record=row)
        if state != "foreign":
            result.append((row["id"], state))
    return result


def build_context(session, scope, *, limit=50, offset=0):
    if scope is None or scope.restricted or "strategies" not in scope.view_modules:
        return {"items": [], "total": 0, "truncated": False, "enabled": False}
    identifiers = _visible_card_ids(session, scope)
    states = dict(identifiers[offset:offset + limit])
    rows = session.scalars(artifact_query(scope, ArtifactModule.strategies.value)
        .where(WorkbenchArtifact.id.in_(states)).order_by(WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id)) if states else []
    visible = []
    for row in rows:
        state = states[row.id]
        value = serialize_card(serialize_artifact(row))
        value.update(scope_status=state, can_edit=scope.can_edit and state == "current",
                     can_apply=state == "current" and value["card"] is not None)
        visible.append(value)
    return {"items": visible, "total": len(identifiers), "offset": offset,
            "truncated": offset + len(visible) < len(identifiers), "enabled": True, "can_write": scope.can_edit}


def workspace_context(session, scope, *, selected_id=None, select_first=True):
    """The initial page carries one full record; other bodies load on selection."""
    data = build_context(session, scope, limit=100)
    selected = next((item for item in data["items"] if item["id"] == selected_id), None)
    if selected_id and selected is None:
        selected = _read_card(session, scope, selected_id)
    if selected is None and select_first and data["items"]:
        selected = data["items"][0]
    rows = [{key: item[key] for key in ("id", "title", "status", "revision", "updated_at",
            "direction_label", "scope_status", "can_edit", "can_apply")} | {
            "direction": (item["card"] or {}).get("direction"),
            "has_action": bool(((item["card"] or {}).get("response_action") or "").strip()),
            "review_date": (item["card"] or {}).get("review_date"), "has_card": item["card"] is not None}
            for item in data["items"]]
    return {**data, "items": rows, "selected": selected, "metadata_only": True}


def _read_card(session, scope, card_id):
    from tin.web.research_references import record_scope_status
    row = session.scalar(artifact_query(scope, ArtifactModule.strategies.value)
                         .where(WorkbenchArtifact.id == card_id))
    if row is None:
        raise HTTPException(404, "研究方案不存在或无权访问")
    state = record_scope_status(session, scope, "artifact", card_id, record=row)
    if state == "foreign":
        raise HTTPException(404, "研究方案不存在或无权访问")
    value = serialize_card(serialize_artifact(row))
    value.update(scope_status=state, can_edit=scope.can_edit and state == "current",
                 can_apply=state == "current" and value["card"] is not None)
    return value


def _validate_reference(session, scope, reference):
    if reference is not None:
        from tin.web.research_references import resolve_reference
        resolve_reference(session, scope, reference.kind, reference.id, revision=reference.revision)


def report_projection(session, scope, *, limit=40):
    """Read only authored card fields; source snapshots and original files stay out of reports."""
    if scope is None or scope.restricted or "strategies" not in scope.view_modules:
        return {"items": [], "total": 0, "truncated": False}
    if type(limit) is not int or not 1 <= limit <= 100:
        raise ValueError("Invalid report card limit")
    identifiers = [identifier for identifier, state in _visible_card_ids(session, scope) if state == "current"]
    query = artifact_query(scope, ArtifactModule.strategies.value).where(WorkbenchArtifact.id.in_(identifiers))
    total = session.scalar(select(func.count()).select_from(query.with_only_columns(WorkbenchArtifact.id).subquery()))
    records = session.execute(query.with_only_columns(WorkbenchArtifact.id, WorkbenchArtifact.title,
        WorkbenchArtifact.body, WorkbenchArtifact.status, WorkbenchArtifact.revision,
        WorkbenchArtifact.created_at, WorkbenchArtifact.updated_at).order_by(
            WorkbenchArtifact.updated_at.desc(), WorkbenchArtifact.id).limit(limit)).mappings()
    items = []
    for record in records:
        try:
            card = ResearchStrategyCard.model_validate_json(record["body"])
        except (ValidationError, ValueError, TypeError):
            continue
        items.append({**{key: record[key] for key in ("id", "title", "status", "revision", "created_at", "updated_at")},
            **card.model_dump(mode="json", exclude_none=True, include={"direction", "thesis", "watch_for", "trigger", "invalidation", "review_date", "response_action", "judgment_reference"}),
            "direction_label": DIRECTION_LABELS[card.direction],
            "status_label": "个人草稿" if record["status"] == "draft" else "人工确认记录",
            "evidence_notice": "当前保存的人工研究方案，不代表历史当时已知观点或实际成交；正文数字未逐项核验。"})
    return {"items": items, "total": total, "truncated": total > limit,
            "legacy_unformatted_count_in_page": min(total, limit) - len(items)}


def artifact_input(body):
    for field in ("title", "thesis", "watch_for", "trigger", "invalidation"):
        if not getattr(body.card, field).strip():
            raise HTTPException(422, "研究策略卡的必填字段不能只含空白")
    if body.card.response_action is not None and not body.card.response_action.strip() and not body.acknowledge_observation:
        raise HTTPException(422, "请填写应对措施，或明确选择保存为待补应对的观察方案")
    text = body.card.model_dump_json(exclude_none=True)
    if len(text) > 60000:
        raise HTTPException(413, "研究策略卡内容超过保存预算，请缩短原文")
    return ArtifactInput(title=body.card.title, body=text, status="draft",
                         source_ids=body.source_ids, confirmed=False, revision=body.revision)


@router.get("")
def list_cards(limit: int = Query(50, ge=1, le=100), offset: int = Query(0, ge=0, le=100000)):
    scope = require_card_scope()
    with artifact_session(scope) as session:
        return build_context(session, scope, limit=limit, offset=offset)


@router.get("/sources")
async def source_choices(page: int = Query(1, ge=1, le=500)):
    scope = require_card_scope()
    from tin.web.workbenches import _native_source_page
    document = await _native_source_page(scope, page, 100)
    return {"items": [{key: item.get(key) for key in ("id", "title", "file_name", "file_type")}
                      for item in document["data"] if item.get("parse_status") not in {"deleted", "deleting"}],
            "page": page, "has_more": page * 100 < document["total"]}


@router.get("/{card_id}")
def card_detail(card_id: str):
    scope = require_card_scope()
    with artifact_session(scope) as session:
        return _read_card(session, scope, card_id)


@router.post("", status_code=201)
async def save_card(request: Request):
    scope = require_card_scope()
    require_shared_write(scope)
    body = await read_input(request)
    with artifact_session(scope) as session:
        _validate_reference(session, scope, body.card.reference)
        _validate_reference(session, scope, body.card.judgment_reference)
    # This module stores the author's hypothesis only; saving does not read or
    # import the complete knowledge-base fact history.
    with page_read_scope("strategies", series_ids=[]):
        row = await create_artifact(ArtifactModule.strategies, artifact_input(body), scope)
    return serialize_card(row)


@router.patch("/{card_id}")
async def revise_card(card_id: str, request: Request):
    scope = require_card_scope()
    require_shared_write(scope)
    body = await read_input(request)
    with artifact_session(scope) as session:
        previous = _read_card(session, scope, card_id)
        if not previous["can_edit"]:
            raise HTTPException(403, "请先核对历史记录归属，再修改研究方案")
        _validate_reference(session, scope, body.card.reference)
        _validate_reference(session, scope, body.card.judgment_reference)
    with page_read_scope("strategies", series_ids=[]):
        row = await update_artifact(ArtifactModule.strategies, card_id, artifact_input(body), scope)
    return serialize_card(row)

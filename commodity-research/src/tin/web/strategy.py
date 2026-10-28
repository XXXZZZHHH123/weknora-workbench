"""Strategy HTTP adapter. Decisions and accounting live in tin.strategy.service."""

from datetime import date, datetime

from fastapi import APIRouter, Body, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from tin.compute.engine import latest_trade_date
from tin.config import SHANGHAI, settings
from tin.integration.research_profile import research_variety
from tin.jobs.seed import seed_researcher
from tin.integration.identity import verified_actor
from tin.schemas.strategy import ExecutionIn, StrategyPlan
from tin.strategy import service
from tin.integration.workbench_scope import current_workbench, go_request, require_shared_write


class StrategyGenerationInput(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    date: str | None = Field(default=None, max_length=10)
    model_id: str | None = Field(default=None, min_length=1, max_length=128)
    research_question: str = Field(default='', max_length=4000)


class StrategyDraftInput(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    base_version: int = Field(ge=1)
    plan: StrategyPlan

STATUS_LABELS = {"draft": "草稿", "watching": "等待入场", "open": "跟踪中",
                 "closed": "已退出", "cancelled": "已作废"}
MODE_LABELS = {"simulation": "模拟跟踪", "actual": "实际成交记录"}
KIND_LABELS = {"outright": "方向策略", "calendar_spread": "跨期价差"}


def _validation_message(exc: ValidationError) -> str:
    return "；".join(f"{'.'.join(map(str, e['loc'])) or '策略'}：{e['msg']}"
                    for e in exc.errors(include_url=False))


def strategy_summary(session) -> dict:
    rows = service.list_strategies(session)
    scope = current_workbench.get()
    if scope is not None:
        from tin.web.research_references import record_scope_status
        rows = [row for row in rows if record_scope_status(session, scope, "plan", str(row.id), row) != "foreign"]
    active = [row for row in rows if row.status in ("watching", "open")]
    return {"active": len(active), "drafts": sum(row.status == "draft" for row in rows),
            "items": [{"id": row.id, "title": row.title, "status": STATUS_LABELS[row.status],
                       "mode": MODE_LABELS[row.mode]} for row in active[:4]]}


def build_router(session_factory, templates, shell) -> APIRouter:
    """Use a lazy session factory so the application's session override stays effective."""
    router = APIRouter()

    def context(session, **extra):
        return shell(session, "strategies", None, status_labels=STATUS_LABELS,
                     mode_labels=MODE_LABELS, kind_labels=KIND_LABELS,
                     now_local=datetime.now(SHANGHAI).strftime("%Y-%m-%dT%H:%M"),
                     platform_models=current_workbench.get() is not None, **extra)

    def find(session, strategy_id):
        try:
            row = service.get_strategy(session, strategy_id)
        except PermissionError:
            raise HTTPException(404, "策略不存在或无权访问") from None
        if row is None:
            raise HTTPException(404, "策略不存在")
        scope = current_workbench.get()
        if scope is not None:
            from tin.web.research_references import record_scope_status
            if record_scope_status(session, scope, "plan", str(row.id), record=row) == "foreign":
                raise HTTPException(404, "策略不存在或无权访问")
        return row

    def mutate(strategy_id, body, operation):
        with session_factory() as session:
            row = find(session, strategy_id)
            scope = current_workbench.get()
            if scope is not None:
                require_shared_write(scope)
                from tin.web.research_references import record_scope_status
                if record_scope_status(session, scope, "plan", str(row.id), row) != "current":
                    raise HTTPException(403, "请先核对历史记录归属，再编辑计划")
            actor = verified_actor(seed_researcher(session).display_name)
            try:
                if operation in ("open", "close"):
                    execution = ExecutionIn.model_validate(body)
                    getattr(service, f"{operation}_strategy")(session, strategy_id, execution, actor=actor)
                else:
                    getattr(service, f"{operation}_strategy")(
                        session, strategy_id, actor=actor, reason=str(body.get("reason") or "").strip())
                session.commit()
                return service.serialize_strategy(session, find(session, strategy_id))
            except ValidationError as exc:
                session.rollback()
                raise HTTPException(400, _validation_message(exc)) from None
            except ValueError as exc:
                session.rollback()
                raise HTTPException(400, str(exc)) from None

    @router.get("/sn/strategies")
    def strategies_page(request: Request, status: str = "all", selected: str | None = None):
        if status != "all" and status not in STATUS_LABELS:
            raise HTTPException(400, "未知策略状态")
        with session_factory() as session:
            rows = service.list_strategies(session)
            scope = current_workbench.get()
            from tin.web.research_strategy import workspace_context
            cards = workspace_context(session, scope,
                selected_id=selected.removeprefix("research:") if selected and selected.startswith("research:") else None,
                select_first=not (selected and selected.startswith("plan:")))
            from tin.web.research_references import record_scope_status
            visible = []
            for row in rows:
                if status != "all" and row.status != status:
                    continue
                state = record_scope_status(session, scope, "plan", str(row.id), record=row) if scope else "current"
                if state != "foreign":
                    visible.append((row, state))
            entries = [{"id": row.id, "key": "plan:" + str(row.id), "title": row.title,
                "status": row.status, "status_label": STATUS_LABELS[row.status], "version": row.version,
                "kind_label": KIND_LABELS[row.kind], "scope_status": state,
                "review_date": str((row.plan or {}).get("review_at") or "")[:10]}
                for row, state in visible[:100]]
            wanted = selected or ("research:" + cards["selected"]["id"] if cards.get("selected")
                                  else entries[0]["key"] if entries else None)
            chosen = next((row for row, _ in visible if wanted == "plan:" + str(row.id)), None)
            if wanted and not chosen and not (wanted.startswith("research:") and cards.get("selected")
                                             and wanted == "research:" + cards["selected"]["id"]):
                raise HTTPException(404, "策略不存在或无权访问")
            initial = service.serialize_strategy(session, chosen) if chosen else cards.get("selected")
            if chosen:
                initial["scope_status"] = next(state for row, state in visible if row.id == chosen.id)
            return templates.TemplateResponse(request, "strategies.html", context(
                session, strategies=[], strategy_entries=entries,
                research_cards=cards, workspace_initial=initial, workspace_selected=wanted,
                workspace_can_write=scope.can_edit if scope is not None else True,
                selected_status=status, summary=strategy_summary(session),
                latest_date=latest_trade_date(session)))

    @router.get("/sn/strategies/{strategy_id}")
    def strategy_page(request: Request, strategy_id: int):
        with session_factory() as session:
            row = find(session, strategy_id)
            scope = current_workbench.get()
            state = "current"
            if scope is not None:
                from tin.web.research_references import record_scope_status
                state = record_scope_status(session, scope, "plan", str(row.id), row)
            return templates.TemplateResponse(request, "strategy_detail.html", context(
                session, strategy=service.serialize_strategy(session, row), strategy_scope_status=state,
                strategy_can_write=(scope is None or scope.can_edit) and state == "current"))

    @router.get("/api/sn/strategies")
    def strategies_api():
        with session_factory() as session:
            scope = current_workbench.get()
            from tin.web.research_references import record_scope_status
            return {"strategies": [service.serialize_strategy(session, row)
                for row in service.list_strategies(session) if scope is None or
                record_scope_status(session, scope, "plan", str(row.id), row) != "foreign"]}

    @router.post("/api/sn/strategies", status_code=201)
    def create_strategy(body: dict = Body(...)):
        scope = current_workbench.get()
        if scope is not None:
            require_shared_write(scope)
        with session_factory() as session:
            try:
                plan = StrategyPlan.model_validate(body)
                row = service.create_strategy(session, plan, actor=verified_actor(seed_researcher(session).display_name),
                                              reason="研究员建立策略草稿")
                session.commit()
                return service.serialize_strategy(session, row)
            except ValidationError as exc:
                session.rollback()
                raise HTTPException(400, _validation_message(exc)) from None
            except ValueError as exc:
                session.rollback()
                raise HTTPException(400, str(exc)) from None

    @router.post("/api/sn/strategies/generate")
    def generate_strategy(body: StrategyGenerationInput):
        from tin.strategy.generate import generate_candidates
        from tin.integration.kb_facts import page_read_scope
        try:
            requested_day = date.fromisoformat(body.date) if body.date else None
        except (TypeError, ValueError):
            raise HTTPException(400, '研究日期应为 YYYY-MM-DD') from None
        if requested_day is not None and requested_day > datetime.now(SHANGHAI).date():
            raise HTTPException(400, '研究日期不能晚于今天')
        scope = current_workbench.get()
        provider = None
        if scope is not None:
            require_shared_write(scope)
            if not body.model_id:
                raise HTTPException(400, '请选择已配置的通用模型')
            from tin.llm.platform_provider import PlatformStrategyProvider
            from tin.llm.base import ProviderUnavailable
            try:
                provider = PlatformStrategyProvider(scope, body.model_id)
            except ProviderUnavailable:
                raise HTTPException(400, '请选择本工作台可用的已配置通用模型') from None
        with page_read_scope('strategies', day=requested_day), session_factory() as session:
            day = requested_day or latest_trade_date(session)
            if day is None:
                return {"status": "no_candidate", "strategies": [],
                        "gaps": ["尚无行情数据。先录入明确合约的行情，再生成策略草稿。"]}
            result = generate_candidates(session, research_variety(session), day,
                                         researcher_id=seed_researcher(session).id,
                                         provider=provider,
                                         research_question=body.research_question.strip())
            session.commit()
            return {"status": result.status, "gaps": result.gaps, "call_id": result.call_id,
                    "strategies": [service.serialize_strategy(session, row) for row in result.strategies]}

    @router.get('/api/sn/strategies/models')
    async def strategy_models():
        scope = current_workbench.get()
        if scope is None:
            raise HTTPException(409, '请从研究工作台选择平台模型')
        require_shared_write(scope)
        document = await go_request(scope, '/api/v1/models')
        rows = document.get('data')
        if not isinstance(rows, list):
            raise HTTPException(503, '模型目录暂时不可用')
        return {'models': [{'id': row['id'], 'name': row.get('display_name') or row.get('name') or row['id']}
            for row in rows if isinstance(row, dict) and row.get('id')
            and row.get('type') == 'KnowledgeQA' and row.get('status') == 'active']}

    @router.get("/api/sn/strategies/{strategy_id}")
    def strategy_api(strategy_id: int):
        with session_factory() as session:
            row = find(session, strategy_id)
            value = service.serialize_strategy(session, row)
            scope = current_workbench.get()
            if scope is not None:
                from tin.web.research_references import record_scope_status
                value["scope_status"] = record_scope_status(session, scope, "plan", str(row.id), row)
            return value

    @router.post("/api/sn/strategies/{strategy_id}/draft", status_code=201)
    def strategy_draft(strategy_id: int, body: StrategyDraftInput):
        scope = current_workbench.get()
        if scope is not None:
            require_shared_write(scope)
        with session_factory() as session:
            source = find(session, strategy_id)
            if scope is not None:
                from tin.web.research_references import record_scope_status, resolve_reference
                if record_scope_status(session, scope, "plan", str(source.id), record=source) != "current":
                    raise HTTPException(403, "请先核对历史记录归属，再应用为新草稿")
                resolve_reference(session, scope, "strategy", "plan:" + str(source.id), revision=body.base_version)
            if source.version != body.base_version:
                raise HTTPException(409, "原策略已经更新，请重新预览后再应用")
            try:
                actor = verified_actor(seed_researcher(session).display_name)
                row = service.create_strategy(session, body.plan, actor=actor,
                    reason="根据已有策略另建待审阅草稿", revision_of=source.id)
                from tin.models import StrategyEvent
                from sqlalchemy import select
                event = session.scalar(select(StrategyEvent).where(StrategyEvent.strategy_id == row.id,
                    StrategyEvent.event_type == "created"))
                event.payload = {**event.payload, "reference": {"kind": "strategy", "id": "plan:" + str(source.id),
                    "revision": body.base_version, "title": source.title}, "source_plan": source.plan}
                session.commit()
                return service.serialize_strategy(session, row)
            except ValueError as exc:
                session.rollback()
                raise HTTPException(400, str(exc)) from None

    @router.post("/api/sn/strategies/{strategy_id}/publish")
    def publish_strategy(strategy_id: int, body: dict = Body(...)):
        return mutate(strategy_id, body, "publish")

    @router.post("/api/sn/strategies/{strategy_id}/open")
    def open_strategy(strategy_id: int, body: dict = Body(...)):
        return mutate(strategy_id, body, "open")

    @router.post("/api/sn/strategies/{strategy_id}/close")
    def close_strategy(strategy_id: int, body: dict = Body(...)):
        return mutate(strategy_id, body, "close")

    @router.post("/api/sn/strategies/{strategy_id}/cancel")
    def cancel_strategy(strategy_id: int, body: dict = Body(...)):
        return mutate(strategy_id, body, "cancel")

    return router

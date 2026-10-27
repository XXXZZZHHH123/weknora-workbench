import csv
import io
import json
import logging
from contextlib import contextmanager
from datetime import date, datetime, time
from datetime import date as CalendarDate
from pathlib import Path
from urllib.parse import quote, urlencode

from fastapi import BackgroundTasks, Body, FastAPI, File, Form, HTTPException, Query, Request, UploadFile
from fastapi.responses import FileResponse, JSONResponse, RedirectResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from pydantic import ValidationError
from sqlalchemy import select, text

from tin.brief import store as brief_store
from tin.caliber.dictionary import DIMENSIONS
from tin.compute.engine import compute_day, latest_trade_date, observation_boundary
from tin.config import SHANGHAI, settings
from tin.db import SessionLocal
from tin import schema_check
from tin.export.board import (
    archive_days,
    audit_entries,
    contract_curve,
    core_cards,
    gaps,
    judgment_block,
    judgment_record_block,
    macro_featured,
    snapshot,
    source_catalog,
    threshold_radar,
    ticker,
)
from tin.export import templates as export_templates
from tin.export.excel import Column, build_export, build_import_template, field_catalog
from tin.export import diagram as diagram_api
from tin.export import diagram_md
from tin.export import indicators as indicator_data
from tin.ingest import excel_importer, import_jobs, vendor_terminal
from tin.ingest.record import RecordError, record
from tin.jobs.seed import seed_researcher
from tin.judgments.service import JudgmentError, current, save_version, to_payload
from tin.models import AuditLog, DailyBrief, Indicator, Judgment, Observation, TradingDay
from tin.schemas.brief import STANCES
from tin.schemas.caliber import Caliber
from tin.schemas.judgment import TONES
from tin.schemas.observation import ObservationIn
from tin.integration.identity import current_identity, research_prefix, research_url, research_form_url, verified_actor
from tin.integration.auth import ResearchAuthMiddleware
from tin.ownership import scope_query
from tin.web import home
from tin.web.strategy import build_router as build_strategy_router

HERE = Path(__file__).parent
log = logging.getLogger("uvicorn.error")   # 借 uvicorn 的 logger：自建的默认没有 handler，写了也看不见
app = FastAPI(title="大宗商品研究工作台")
app.mount("/static", StaticFiles(directory=HERE / "static"), name="static")
# A standalone document needs its own font registrations as well as the iframe.
FONT_ASSETS = HERE.resolve().parents[3] / "WeKnora/frontend/public/fonts"
if FONT_ASSETS.is_dir():
    app.mount("/fonts", StaticFiles(directory=FONT_ASSETS), name="fonts")
templates = Jinja2Templates(directory=HERE / "templates")

V = settings.variety
from tin.integration.research_profile import research_variety
# 指标未登记默认发布时刻时的兜底值（收盘截点）
FALLBACK_ENTRY_TIME = time(15, 0)


@contextmanager
def _page_session(module, *, day=None, series_ids=None, template_id=None, points_per_series=None, judgment_id=None):
    scope = current_workbench.get()
    if scope is None or scope.scope_version != 2:
        with SessionLocal() as session:
            yield session
        return
    from tin.integration.kb_facts import page_read_scope
    try:
        cutoff = CalendarDate.fromisoformat(day) if isinstance(day, str) else day
    except ValueError:
        raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
    with page_read_scope(module, series_ids=series_ids, day=cutoff,
                         template_id=template_id, points_per_series=points_per_series, judgment_id=judgment_id):
        with SessionLocal() as session:
            yield session


def _layout_series_ids(layout):
    if not isinstance(layout, dict) or not isinstance(layout.get('nodes', []), list):
        raise HTTPException(400, '产业图结构无效')
    identifiers = []
    for node in layout.get('nodes', []):
        if not isinstance(node, dict):
            raise HTTPException(400, '产业图节点无效')
        binding = node.get('binding') or {}
        if not isinstance(binding, dict):
            raise HTTPException(400, '产业图绑定无效')
        if binding.get('kind') == 'series':
            identifier = binding.get('series_id')
            if not isinstance(identifier, str) or not identifier or len(identifier) > 128:
                raise HTTPException(400, '产业图指标编号无效')
            scope = current_workbench.get()
            if scope is not None and scope.scope_version == 2:
                from tin.integration.page_facts import SERIES, LEGACY_ID
                if not SERIES.fullmatch(identifier):
                    if identifier.startswith('kb-') or not LEGACY_ID.fullmatch(identifier):
                        raise HTTPException(422, '产业图指标编号格式无效')
                    # Retained legacy bindings remain visible as unavailable;
                    # they cannot enter the current knowledge-facts query.
                    continue
            identifiers.append(identifier)
    return list(dict.fromkeys(identifiers))


def _num(value, unit: str = "", signed: bool = False) -> str:
    if value is None:
        return "—"
    if unit == "元":
        digits = 4
    elif unit == "%":
        digits = 2
    elif unit in ("元/吨", "吨", "手", "实物吨") or float(value).is_integer():
        digits = 0
    else:
        digits = 2
    s = f"{value:+,.{digits}f}" if signed else f"{value:,.{digits}f}"
    return s.replace("-", "−")


def static_version() -> str:
    """静态资源指纹。页面每次渲染都带上它，避免浏览器拿着旧的 app.js 配新的 HTML——
    那样按钮在、函数不在，点了没反应且毫无提示。"""
    newest = max((f.stat().st_mtime for f in (HERE / "static").glob("*")), default=0)
    return str(int(newest))


_migrate_error: str | None = None


@app.on_event("startup")
def _migrate_on_startup() -> None:
    """启动时自动把库升到最新。

    部署脚本和容器启动都没有迁移步骤，不自动跑就意味着每次带迁移的发布都得有人
    手动上服务器执行——漏跑的代价是整站 503，而漏跑几乎必然发生（本地已经连着
    绊了三次）。

    **前提是迁移前自动备份**，否则"自动改生产库"不该开（见 schema_check.backup）。
    **失败也不让进程退出**：起不来的服务只会留下一段终端里的堆栈；起得来、由 503
    页面把错误和手动命令一起显示出来，人才看得见。
    """
    global _migrate_error
    if settings.weknora_api_url:
        return
    engine = schema_check.engine_of(SessionLocal)
    if engine is None or not settings.auto_migrate:
        return
    result = schema_check.auto_upgrade(engine, keep_backups=settings.migrate_backups)
    if not result.get("ran"):
        return
    if result["ok"]:
        # alembic 自己会逐条打出跑了哪些迁移，这里再给一句汇总，
        # 重点是**备份在哪**——真要回退的时候，得能一眼找到那个文件
        log.warning("数据库已自动升级到 %s%s", result.get("to"),
                    f"，迁移前备份：{result['backup']}" if result.get("backup")
                    else "（非 SQLite，未自动备份，请确认运维侧有备份策略）")
    else:
        _migrate_error = result["error"]
        log.error("数据库自动升级失败：%s", _migrate_error)


@app.middleware("http")
async def _guard_schema(request: Request, call_next):
    """库仍然落后时，在页面炸开之前先把话说清楚。

    正常情况下启动时已经自动升级过，这里不会触发。留着它是因为自动升级可能被关掉
    （`TIN_AUTO_MIGRATE=false`）、可能失败、也可能库是在进程起来之后才被换掉的。

    同一个坑绊了三次（`diagram_templates`、`researchers`…）：按表逐个 try/except
    补不完，每加一张表就多一个坑。这里只检查一次迁移版本，对不上就**所有页面**
    统一给出该敲的命令。
    """
    public_font = request.method in {"GET", "HEAD"} and request.url.path.startswith("/fonts/")
    if not settings.weknora_api_url and not request.url.path.startswith("/static") and not public_font:
        gap = schema_check.migration_gap(schema_check.engine_of(SessionLocal))
        if gap:
            if _migrate_error:
                gap = f"自动升级失败了：{_migrate_error}\n\n{gap}"
            if request.url.path.startswith("/api/"):
                return JSONResponse({"detail": gap}, status_code=503)
            return templates.TemplateResponse(
                request, "needs_migration.html",
                {"setup_error": gap, "project_root": str(schema_check.ROOT),
                 "auto_failed": bool(_migrate_error)},
                status_code=503)
    return await call_next(request)


app.add_middleware(ResearchAuthMiddleware)

from tin.integration.identity import research_static_prefix
from tin.integration.workbench_scope import current_workbench
templates.env.globals["research_static_prefix"] = research_static_prefix
templates.env.globals["research_workbench"] = current_workbench.get
templates.env.globals["research_prefix"] = research_prefix
templates.env.globals["research_url"] = research_url
templates.env.globals["research_form_url"] = research_form_url
templates.env.globals["research_identity"] = lambda: current_identity.get()
templates.env.globals["static_version"] = static_version
templates.env.filters["num"] = _num
templates.env.filters["local"] = lambda dt: dt.astimezone(SHANGHAI).strftime("%Y-%m-%d %H:%M") if dt else ""

# Resolve SessionLocal at request time, preserving the app's test/deployment overrides.
from tin.integration.read_api import router as research_read_router
from tin.web.materials import router as materials_router
from tin.web.refresh import router as refresh_router
from tin.web.library import router as library_router
from tin.provenance.router import router as sources_router
from tin.web.workbenches import router as workbench_router
app.include_router(research_read_router)
app.include_router(materials_router)
app.include_router(refresh_router)
app.include_router(library_router)
app.include_router(sources_router)
app.include_router(workbench_router)
from tin.web.workbench_artifacts import router as workbench_artifacts_router
app.include_router(workbench_artifacts_router)
from tin.web.workbench_sources import router as workbench_sources_router
app.include_router(workbench_sources_router)
from tin.web.brief_export import router as brief_export_router
app.include_router(brief_export_router)
from tin.web.diagram_generation import router as diagram_generation_router
app.include_router(diagram_generation_router)
from tin.web.judgment_generation import router as judgment_generation_router
app.include_router(judgment_generation_router)
from tin.web.judgment_structured import router as judgment_structured_router
app.include_router(judgment_structured_router)
from tin.web.research_strategy import router as research_strategy_router
app.include_router(research_strategy_router)
from tin.web.research_references import router as research_references_router
app.include_router(research_references_router)
from tin.web.judgment_panels import router as judgment_panels_router
app.include_router(judgment_panels_router)
from tin.web.report_drafts import router as report_drafts_router
app.include_router(report_drafts_router)
from tin.web.report_figures import router as report_figures_router
app.include_router(report_figures_router)
from tin.web.affairs import router as affairs_router
app.include_router(affairs_router)
from tin.web.market import build_market_router, market_catalog as market_catalog_data, saved_core_cards
app.include_router(build_market_router(lambda: _page_session('market'), templates,
                                       lambda session, day: _home_date(session, day)))


@app.on_event('startup')
def _start_workspace_refresh():
    from tin.refresh.scheduler import start
    start()


@app.on_event('shutdown')
def _stop_workspace_refresh():
    from tin.refresh.scheduler import stop
    stop()

app.include_router(build_strategy_router(lambda: _page_session('strategies'), templates,
                                         lambda *args, **kwargs: _shell(*args, **kwargs)))


def _redirect(path: str, **params) -> RedirectResponse:
    q = urlencode({k: str(v) for k, v in params.items() if v is not None})
    if path.startswith("//") or not path.startswith("/"):
        path = "/"
    path = research_url(path)
    return RedirectResponse(f"{path}?{q}" if q else path, status_code=303)


def _board_date(s, q: str | None) -> date:
    if s.info.get("workbench_id"):
        try:
            requested = date.fromisoformat(q) if q else latest_trade_date(s)
        except (ValueError, TypeError):
            raise HTTPException(400, "日期格式应为 YYYY-MM-DD") from None
        if requested is None:
            raise HTTPException(404, "当前工作台尚无已确认观测，请在「资料与来源」准备并确认本知识库资料")
        from tin.compute.engine import day_end
        if s.scalar(select(Observation.id).where(Observation.status == "正常", Observation.as_of < day_end(requested)).limit(1)) is None:
            raise HTTPException(404, "当前工作台在所选日期之前尚无已确认观测，请在「资料与来源」准备并确认本知识库资料")
        return requested
    if q:
        try:
            requested = date.fromisoformat(q)
        except ValueError:
            raise HTTPException(400, "日期格式应为 YYYY-MM-DD") from None
        matched = s.scalar(
            select(TradingDay.trade_date)
            .where(TradingDay.trade_date <= requested.isoformat())
            .order_by(TradingDay.trade_date.desc())
            .limit(1)
        )
        if matched is None:
            raise HTTPException(404, "所选日期之前没有可用交易日")
        return date.fromisoformat(matched)
    d = latest_trade_date(s)
    if d is None:
        raise HTTPException(503, "尚无行情数据：请在工作台的「数据更新」中立即更新，或导入行情来源文件。")
    return d


def _neighbour_dates(s, d: date) -> tuple[str | None, str | None]:
    if s.info.get("workbench_id"):
        calendar = s.info.get('knowledge_facts_calendar')
        if isinstance(calendar, dict):
            if d.isoformat() == calendar.get('max_date'):
                return calendar.get('latest_previous_date'), None
            if d.isoformat() == calendar.get('as_of'):
                return calendar.get('previous_date'), calendar.get('next_date')
        from tin.compute.engine import day_end
        previous = observation_boundary(s, before=datetime.combine(d, time.min, SHANGHAI))
        following = observation_boundary(s, at_or_after=day_end(d), latest=False)
        return tuple(value.astimezone(SHANGHAI).date().isoformat() if value else None
                     for value in (previous, following))
    prev = s.scalar(select(TradingDay.trade_date).where(TradingDay.trade_date < d.isoformat())
                    .order_by(TradingDay.trade_date.desc()).limit(1))
    nxt = s.scalar(select(TradingDay.trade_date).where(TradingDay.trade_date > d.isoformat())
                   .order_by(TradingDay.trade_date).limit(1))
    return prev, nxt


def _manual_indicators(s) -> list[dict]:
    rows = s.scalars(select(Indicator).where(Indicator.fetch_mode == "manual", Indicator.status == "可用")
                     .order_by(Indicator.category, Indicator.series_id)).all()
    today = datetime.now(SHANGHAI).date()
    return [{"series_id": i.series_id, "name": i.name, "unit": i.unit, "frequency": i.frequency,
             "default_as_of": datetime.combine(today, i.default_entry_time or FALLBACK_ENTRY_TIME)
             .strftime("%Y-%m-%dT%H:%M")} for i in rows]


def _shell(s, nav: str, d: date | None = None, **extra) -> dict:
    """所有页面共用的外壳数据：行情条、日期切换、快速录入抽屉。

    **行情条与日期选择器是两件事。** 行情条是"此刻的市场状态"，每一页都该有；
    日期选择器是页面自己的翻页，只有按日期取数的页面才需要。早先两者都挂在 `d` 上，
    于是不按日期取数的页面（策略页传 d=None）连行情条一起没了——那一条是研究员
    扫一眼就知道盘面的地方，缺了很显眼。
    """
    ctx = {"nav": nav, "d": d.isoformat() if d else None, "manual_indicators": _manual_indicators(s)}
    shown = d or latest_trade_date(s)
    if shown is not None:
        ctx["ticker"] = ticker(s, research_variety(s), shown)
    if d is not None:
        ctx["prev_date"], ctx["next_date"] = _neighbour_dates(s, d)
        calendar = s.info.get('knowledge_facts_calendar')
        if isinstance(calendar, dict):
            ctx['min_date'], ctx['max_date'] = calendar.get('min_date'), calendar.get('max_date')
        else:
            ctx["min_date"] = s.scalar(select(TradingDay.trade_date)
                                       .order_by(TradingDay.trade_date).limit(1))
            ctx["max_date"] = s.scalar(select(TradingDay.trade_date)
                                       .order_by(TradingDay.trade_date.desc()).limit(1))
    return {**ctx, **extra}


def _home_date(s, q: str | None) -> date | None:
    """页面默认日期允许为空；显式历史日期仍按交易日历验证。

    首屏和总览在尚未采集行情时也应保留导航、研究判断与数据更新入口。
    """
    if q:
        return _board_date(s, q)
    return latest_trade_date(s)


@app.get("/")
def home_page(request: Request, date: str | None = None, err: str | None = None):
    """Verified workbenches open a metadata inbox; standalone keeps its brief review."""
    scope = current_workbench.get()
    if scope is not None and not scope.restricted:
        from tin.integration.workbench_scope import artifact_session
        from tin.web.affairs import build_affairs
        with artifact_session(scope) as session:
            return templates.TemplateResponse(request, 'home.html', {
                'nav': 'home', 'd': None, 'manual_indicators': [], 'ticker': [],
                'affairs': build_affairs(session, scope, today=datetime.now(SHANGHAI).date()), 'err': err})
    with _page_session('home', day=date) as s:
        d = _home_date(s, date)
        researcher = seed_researcher(s)
        brief = brief_store.current(s, researcher.id, research_variety(s), d) if d is not None else None
        return templates.TemplateResponse(request, "home.html", _shell(
            s, "home", d,
            researcher=researcher,
            todos=home.todos(s, research_variety(s), d),
            brief=home.brief_view(s, brief, d) if brief is not None else None,
            brief_gap=None if brief is not None else home.brief_gap(s, researcher, d),
            changes=home.overnight(s, research_variety(s), d) if d is not None else {"core": [], "macro": []},
            cards=saved_core_cards(s, d, core_cards(s, research_variety(s), d)) if d is not None else [],
            radar=threshold_radar(s, research_variety(s), d) if d is not None else [],
            gaps=gaps(s, research_variety(s), d) if d is not None else [],
            tones=TONES, stances=STANCES, editable=brief_store.EDITABLE_FIELDS, err=err))


@app.post("/api/sn/brief/{brief_id}/review")
def brief_review(brief_id: int, body: dict = Body(...)):
    """研究员的「采纳 / 改 / 否」。规则全在 `brief/store.review` 里，这里只负责转述。

    `store.BriefError` 一律转成 400 + 原文案：那些规则（改/否必须写原因、系统留痕不可
    改写）是写给研究员看的，包成「操作失败」或者漏成 500 就白写了。
    """
    action = str(body.get("action") or "").strip()
    reason = body.get("reason")
    reason = str(reason).strip() if reason is not None else None
    changes = body.get("changes") or None
    if changes is not None and not isinstance(changes, dict):
        raise HTTPException(400, "changes 必须是「字段名 → 新值」的对象")
    elapsed = body.get("elapsed_seconds")
    if elapsed is not None:
        try:
            elapsed = max(0, int(elapsed))
        except (TypeError, ValueError):
            raise HTTPException(400, "elapsed_seconds 必须是秒数（整数）") from None

    with SessionLocal() as s:
        actor = verified_actor(str(body.get("actor") or "").strip() or seed_researcher(s).display_name)
        try:
            revisions = brief_store.review(s, brief_id, actor=actor, action=action, changes=changes,
                                           reason=reason, elapsed_seconds=elapsed)
        except brief_store.BriefError as e:
            raise HTTPException(400, str(e)) from None
        brief = s.get(DailyBrief, brief_id)
        return JSONResponse({"brief_id": brief_id, "status": brief.status, "actor": actor,
                             "revisions": len(revisions), "elapsed_seconds": elapsed,
                             "fields": [r.field for r in revisions if r.field]})


@app.post("/api/sn/brief/{brief_id}/reopen")
def brief_reopen(brief_id: int, body: dict = Body(...)):
    """把已处理的简报退回草稿，让重跑能再次生成。

    误点一次「否」不该让研究员当天再也拿不到简报——`save()` 拒绝覆盖已处理简报是对的，
    但没有这个出口就是死胡同。
    """
    reason = str(body.get("reason") or "").strip()
    with SessionLocal() as s:
        actor = verified_actor(str(body.get("actor") or "").strip() or seed_researcher(s).display_name)
        try:
            brief_store.reopen(s, brief_id, actor=actor, reason=reason)
        except brief_store.BriefError as e:
            raise HTTPException(400, str(e)) from None
        return JSONResponse({"brief_id": brief_id, "status": "草稿", "actor": actor})


@app.post("/api/sn/review-task/{task_id}/resolve")
def review_task_resolve(task_id: int, body: dict = Body(...)):
    """关闭一条复盘待办。

    待办只增不减的话，首屏那块「少而紧急」几天就变成第二面数据墙——
    而它短，恰恰是首屏全部价值所在。
    """
    from tin.compute.signals import resolve_review_task

    note = str(body.get("note") or "").strip()
    with SessionLocal() as s:
        actor = verified_actor(str(body.get("actor") or "").strip() or seed_researcher(s).display_name)
        try:
            task = resolve_review_task(s, task_id, actor=actor, note=note)
        except ValueError as e:
            raise HTTPException(400, str(e)) from None
        return JSONResponse({"task_id": task.id, "state": task.state, "actor": actor,
                             "resolved_at": task.resolved_at.isoformat()})


def _schema_state() -> tuple[str | None, str | None]:
    """（库里的 alembic 版本, 代码期望的 head）。取不到就返回 None，不让体检本身把进程弄挂。"""
    from alembic.config import Config
    from alembic.script import ScriptDirectory

    from tin.config import ROOT

    try:
        head = ScriptDirectory.from_config(Config(str(ROOT / "alembic.ini"))).get_current_head()
    except Exception:
        head = None
    try:
        with SessionLocal() as s:
            current = s.execute(text("SELECT version_num FROM alembic_version")).scalar()
    except Exception:
        current = None
    return current, head


@app.get("/healthz", include_in_schema=False)
def healthz():
    """部署闸门。进程活着、数据库连得上、**且表结构是代码期望的那一版**。

    原来这里只做 `SELECT 1`：库落后两个迁移照样返回 ok，部署脚本据此判定成功，
    而应用其实一点开页面就 500。体检报平安、病人躺地上，是最坏的一种失败。
    所以版本不一致必须是 503——宁可让部署红，也不要让它假绿。
    """
    with SessionLocal() as s:
        s.execute(text("SELECT 1"))
    current, head = _schema_state()
    if head is not None and current != head:
        raise HTTPException(503, f"数据库表结构落后于代码：库内 {current or '无版本记录'}，"
                                 f"代码期望 {head}。请在该机器上执行 alembic upgrade head。")
    return {"status": "ok", "schema": current}


@app.get("/sn")
def variety_page(request: Request, date: str | None = None, err: str | None = None):
    with _page_session('market', day=date) as s:
        d = _home_date(s, date)
        today = datetime.now(SHANGHAI).date()
        cards = saved_core_cards(s, d, core_cards(s, research_variety(s), d) if d is not None else [])
        from tin.web.market_history import market_history
        from tin.web.market_contracts import configured_contract_curve
        curve = (configured_contract_curve(s, d if date else None) if s.info.get('workbench_id')
                 else contract_curve(s, research_variety(s), d) if d is not None else None)
        return templates.TemplateResponse(request, "variety.html", _shell(
            s, "variety", d, cards=cards,
            market_catalog=market_catalog_data(s, cards, d) if s.info.get('workbench_id') else None,
            judgment=judgment_block(s, research_variety(s), today),
            radar=threshold_radar(s, research_variety(s), d) if d is not None else [],
            gaps=gaps(s, research_variety(s), d) if d is not None else [],
            curve=curve,
            market_history=market_history(s, d) if s.info.get('workbench_id') and d else None,
            macro=macro_featured(s, d) if d is not None else [],
            liquidity_min=settings.liquidity_min_volume, err=err))


@app.get("/sn/macro")
def macro_page(request: Request, date: str | None = None):
    target = research_url("/sn/indicators")
    if date:
        target += "?" + urlencode({"date": date})
    return RedirectResponse(target, status_code=307)


@app.get("/sn/judgment")
def judgment_page(request: Request, err: str | None = None):
    return _judgment_response(request, err=err)


def _judgment_response(request: Request, *, err=None, logic_input=""):
    from tin.web.judgment_panels import judgment_directory, selected_judgment
    selected_id = request.query_params.get('judgment_id')
    if selected_id is not None:
        if not selected_id.isascii() or not selected_id.isdecimal() or not 1 <= len(selected_id) <= 19:
            raise HTTPException(422, '研究判断编号无效')
        selected_id = int(selected_id)
        if not 1 <= selected_id <= 2**63 - 1:
            raise HTTPException(422, '研究判断编号无效')
    with _page_session('judgments', day=request.query_params.get('date'), judgment_id=selected_id) as s:
        d = _home_date(s, request.query_params.get('date'))
        today = datetime.now(SHANGHAI).date()
        scope = current_workbench.get()
        j = selected_judgment(s, selected_id, scope=scope) if selected_id is not None else current(s, research_variety(s))
        allowed_ids = [row.id for row in judgment_directory(s, scope)] if scope else None
        if j is not None and allowed_ids is not None and j.id not in allowed_ids:
            j = None
        variety = j.variety if j is not None else research_variety(s)
        version_query = select(Judgment.version, Judgment.status, Judgment.author, Judgment.written_at,
                               Judgment.review_due, Judgment.created_at).where(Judgment.variety == variety)
        if allowed_ids is not None:
            version_query = version_query.where(Judgment.id.in_(allowed_ids))
        versions = s.execute(scope_query(s, version_query.order_by(Judgment.version.desc()).limit(50), Judgment)).all()
        audit_filter = [AuditLog.action == '判断导入']
        if current_identity.get():
            audit_filter.append(AuditLog.actor == verified_actor())
        if scope:
            audit_filter.append(AuditLog.detail['workbench_id'].as_string() == scope.workbench_id)
        last_import = s.scalars(select(AuditLog).where(*audit_filter)
                                .order_by(AuditLog.id.desc()).limit(1)).first()
        return templates.TemplateResponse(request, "judgment.html", _shell(
            s, "judgment", d, j=j, block=judgment_record_block(j, today), versions=versions,
            selected_judgment_id=selected_id,
            names={i.series_id: i.name for i in s.scalars(select(Indicator))},
            radar=threshold_radar(s, variety, d, judgment=j) if d and j is not None else [],
            report=last_import.detail if last_import else None, err=err, logic_input=logic_input))


@app.post("/sn/judgment/import")
async def judgment_import(request: Request, file: UploadFile | None = None, text: str = Form(""), author: str = Form("")):
    """Compatibility entry: preserve input in a safe modal, never silently save/parse it."""
    from tin.judgments.analysis import SOURCE_BYTES
    if current_identity.get() and not current_identity.get().can_write:
        raise HTTPException(403, "当前角色不能导入研究逻辑")
    try:
        raw = (await file.read(SOURCE_BYTES + 1)).decode("utf-8") if file is not None and file.filename else text
    except UnicodeDecodeError:
        raise HTTPException(422, "请上传UTF-8编码的研究逻辑") from None
    if len(raw.encode("utf-8")) > SOURCE_BYTES:
        raise HTTPException(413, "研究逻辑原文超过48 KiB")
    return _judgment_response(request, err="请在管理研究逻辑中选择模型分析，预览后确认保存。", logic_input=raw)


@app.get("/sn/entry")
def entry_form(request: Request, series_id: str | None = None, err: str | None = None):
    with SessionLocal() as s:
        if s.info.get('knowledge_facts'):
            return templates.TemplateResponse(request, 'knowledge_entry.html', _shell(
                s, 'entry', latest_trade_date(s)))
        return templates.TemplateResponse(request, "entry.html", _shell(
            s, "entry", latest_trade_date(s), indicators=_manual_indicators(s), selected=series_id, err=err,
            audit=audit_entries(s), now=datetime.now(SHANGHAI).strftime("%Y-%m-%dT%H:%M")))


@app.post("/sn/entry")
def entry_submit(series_id: str = Form(...), value: float = Form(...), as_of: str = Form(...),
                 entered_by: str = Form(...), note: str = Form(...), return_to: str = Form("/sn/entry")):
    entered_by = verified_actor(entered_by)
    with SessionLocal() as s:
        ind = s.get(Indicator, series_id)
        if ind is None or ind.fetch_mode != "manual":
            raise HTTPException(400, "只能为人工录入指标录入数值")
        when = datetime.fromisoformat(as_of).replace(tzinfo=SHANGHAI)
        try:
            row = record(s, ObservationIn(series_id=series_id, value=value, as_of=when,
                                          caliber=Caliber.model_validate(ind.caliber), source="人工",
                                          entered_by=entered_by.strip(), note=note.strip()))
        except (RecordError, ValidationError) as e:
            return _redirect(return_to.split("?")[0], err=e)
        s.add(AuditLog(at=datetime.now(SHANGHAI), actor=entered_by.strip(), action="人工录入",
                       target_type="observation", target_id=series_id,
                       detail={"value": value, "as_of": when.isoformat(), "note": note}))
        s.commit()
        d = latest_trade_date(s)
        if d is not None:
            if not s.info.get("workbench_id"):
                compute_day(s, d)
        completion = "，已保存本工作台观测" if s.info.get("workbench_id") else "，派生指标已重算"
        text = f"已录入 {ind.name} = {value:g}{completion}" if row else "与已有值相同，未重复写入"
        base, _, query = return_to.partition("?")
        params = dict(p.split("=", 1) for p in query.split("&") if "=" in p)
        return _redirect(base, msg=text, **params)


@app.get("/sn/indicators")
def indicators_page(request: Request, date: str | None = None):
    with _page_session('indicators', day=date) as s:
        scope = current_workbench.get()
        remote = scope is not None and scope.scope_version == 2
        directory = None
        if remote:
            from tin.web.catalog import indicator_page
            try:
                offset = int(request.query_params.get('offset', '0'))
            except ValueError:
                raise HTTPException(400, '目录页码无效') from None
            kind = request.query_params.get('scope', 'all')
            kind = kind if kind in {'all', 'macro', 'industry'} else 'all'
            directory = indicator_page(scope, offset=offset, search=request.query_params.get('q', ''), kind=kind)
            rows = directory['indicators']
        else:
            rows = indicator_data.catalog(s)
        day = _home_date(s, date)
        groups = [{"name": name, "rows": [r for r in rows if r["category"] == name]}
                  for name in dict.fromkeys(r["category"] for r in rows)]
        checks = diagram_api.crosscheck(s, research_variety(s), day) if day else []
        return templates.TemplateResponse(request, "indicators.html", _shell(
            s, "indicators", day, rows=rows, groups=groups, dimensions=DIMENSIONS, checks=checks,
            catalog_remote=remote, catalog_offset=directory['offset'] if directory else 0,
            catalog_total=directory['total'] if directory else len(rows),
            catalog_next=directory['next_offset'] if directory else None,
            sources={"rows": [], "counts": {}} if s.info.get("workbench_id") else source_catalog()))


@app.get("/sn/indicators/{series_id}")
def indicator_page(request: Request, series_id: str, date: str | None = None):
    """单指标页。

    指标列表回答不了研究员真正的问题——444 行里每一行看起来都一样重要。这一页要回答
    三件事：这个数现在多少且怎么来的（口径与凭证）、被谁在用（判断与产业图）、
    历史上有没有被改过（修订）。**「被谁在用」是分辨要不要维护它的唯一依据。**
    """
    with _page_session('indicators', day=date, series_ids=[series_id], points_per_series=120) as s:
        day = _home_date(s, date)
        try:
            d = diagram_api.detail(s, series_id, day, limit=120)
        except diagram_api.DiagramError as e:
            raise HTTPException(404, str(e)) from e
        return templates.TemplateResponse(request, "indicator.html", _shell(
            s, "indicators", day, detail=d, usage=diagram_api.usage(s, series_id, research_variety(s))))


@app.get("/api/sn/indicators/{series_id}/usage")
def indicator_usage(series_id: str):
    with _page_session('indicators', series_ids=[series_id]) as s:
        return diagram_api.usage(s, series_id, research_variety(s))


@app.get("/api/sn/indicators/catalog")
def indicator_catalog(scope: str = "all", search: str = Query('', max_length=200),
                      offset: int = Query(0, ge=0), limit: int = Query(200, ge=1, le=200),
                      series_id: list[str] | None = Query(None, max_length=256), date: str | None = None,
                      format: str = 'html'):
    if format not in {'html', 'metadata'}:
        raise HTTPException(400, '无效指标目录显示格式')
    if scope not in {"all", "macro", "industry"}:
        raise HTTPException(400, "无效指标目录范围")
    if date is not None:
        try:
            date = CalendarDate.fromisoformat(date).isoformat()
        except ValueError:
            raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
    workbench = current_workbench.get()
    if workbench is not None and workbench.scope_version == 2:
        from tin.web.catalog import indicator_page
        page = indicator_page(workbench, offset=offset, limit=limit, search=search, kind=scope, identifiers=series_id)
        metadata = {key: value for key, value in page.items() if key not in {'series', 'groups', 'datasets'}}
        if format == 'metadata':
            return metadata
        render = {'groups': page['groups'], 'rows': page['indicators'], 'restricted_view': False, 'd': date, 'catalog_offset': offset}
        return {**metadata,
                'html': templates.get_template('_indicator_catalog_groups.html').render(render),
                'nav_html': templates.get_template('_indicator_catalog_links.html').render(render)}
    with SessionLocal() as session:
        rows = indicator_data.catalog(session)
        return {"indicators": [row for row in rows if scope == "all" or row["macro"] == (scope == 'macro')]}


@app.get("/sn/diagram")
def diagram_page(request: Request, template: int | None = None):
    with _page_session('diagram', template_id=template) as s:
        # 这里原来无条件返回默认布局，`?template=` 被整个忽略——下拉框选哪张都回到同一张，
        # 新建的布局也永远打不开。切换布局是这个页面最基本的操作之一。
        default = diagram_api.ensure_default(s, V)
        rows = diagram_api.list_templates(s, V)
        tpl = next((t for t in rows if t["id"] == template), None) if template else None
        if template is not None and tpl is None:
            raise HTTPException(404, f"没有这张布局：{template}")
        return templates.TemplateResponse(request, "diagram.html", _shell(
            s, "diagram", latest_trade_date(s), template=tpl or default,
            templates_all=rows))


def _diagram_query_date(session, requested):
    if not session.info.get("workbench_id"):
        day = _board_date(session, requested)
        return day, day.isoformat()
    try:
        day = date.fromisoformat(requested) if requested else latest_trade_date(session)
    except (ValueError, TypeError):
        raise HTTPException(400, "日期格式应为 YYYY-MM-DD") from None
    # The lookup cutoff keeps missing nodes usable; it is never returned as a
    # source date when this workbench has no confirmed observations by that day.
    day = day or datetime.now(SHANGHAI).date()
    from tin.compute.engine import day_end
    observed = session.scalar(select(Observation.id).where(
        Observation.status == "正常", Observation.as_of < day_end(day)).limit(1))
    return day, day.isoformat() if observed is not None else None


@app.get("/api/sn/diagram/values")
def diagram_values(template_id: int | None = None, date: str | None = None):
    with _page_session('diagram', day=date, template_id=template_id) as s:
        rows = diagram_api.list_templates(s, V)
        tpl = next((t for t in rows if t["id"] == template_id), None) or (rows[0] if rows else None)
        if tpl is None:
            raise HTTPException(404, "还没有任何布局")
        day, as_of = _diagram_query_date(s, date)
        return {"template_id": tpl["id"], "as_of": as_of,
                "judgment": diagram_api.judgment_roles(s, V)[1],
                "nodes": diagram_api.resolve(s, tpl["layout"], day)}


@app.post("/api/sn/diagram/values")
def diagram_values_live(body: dict = Body(...)):
    """按**前端当前正在编辑的布局**取值，不经过库里存的那一份。

    新增节点、刚换的绑定在保存之前库里并不存在；GET 版按 template_id 从库里读布局，
    于是新节点永远查不到值，看上去就是「绑了但不显示」。编辑态必须走这个入口。
    """
    layout = body.get("layout") or {}
    with _page_session('diagram', day=body.get('date'), series_ids=_layout_series_ids(layout), points_per_series=24) as s:
        day, as_of = _diagram_query_date(s, body.get("date"))
        roles_meta = diagram_api.judgment_roles(s, V)[1]
        return {"as_of": as_of, "judgment": roles_meta,
                "nodes": diagram_api.resolve(s, layout, day)}


@app.post("/api/sn/diagram/templates")
def diagram_save(body: dict = Body(...)):
    with _page_session('diagram', series_ids=_layout_series_ids(body.get('layout') or {})) as s:
        try:
            saved = diagram_api.save_template(s, V, body)
        except diagram_api.DiagramError as e:
            raise HTTPException(400, str(e)) from None
        return {"saved": saved, "templates": diagram_api.list_templates(s, V)}


@app.get("/api/sn/diagram/markdown")
def diagram_to_markdown(template_id: int | None = None):
    """导出为 Markdown 大纲。绑定写成注释，改完再导回无损。"""
    with _page_session('diagram', template_id=template_id) as s:
        rows = diagram_api.list_templates(s, V)
        tpl = next((t for t in rows if t["id"] == template_id), None) or (rows[0] if rows else None)
        if tpl is None:
            raise HTTPException(404, "还没有任何布局")
        try:
            markdown = diagram_md.dump(tpl["layout"], tpl["name"])
        except diagram_md.MarkdownError as e:
            raise HTTPException(400, str(e)) from None
        return {"name": tpl["name"], "markdown": markdown}


@app.post("/api/sn/diagram/markdown/dump")
def diagram_dump_markdown(body: dict = Body(...)):
    """把**前端此刻的布局**导成 Markdown。

    按 template_id 从库里导，拿到的是已保存版本——画布上刚改的结构不在里面，
    于是"画布改 / 文本改"这两条通道对不上：在图上加了个节点，打开 Markdown 看不到它。
    两边要能互相接着改，导出就得按内存里的布局来。
    """
    layout = body.get("layout") or {}
    name = str(body.get("name") or "产业结构").strip()
    try:
        markdown = diagram_md.dump(layout, name)
    except diagram_md.MarkdownError as e:
        raise HTTPException(400, str(e)) from None
    return {"name": name, "markdown": markdown}


@app.post("/api/sn/diagram/markdown")
def diagram_from_markdown(body: dict = Body(...)):
    """从 Markdown 大纲建一份新布局。不覆盖现有布局——导错了还能切回去。"""
    try:
        layout = diagram_md.parse(str(body.get("markdown", "")))
    except diagram_md.MarkdownError as e:
        raise HTTPException(400, str(e)) from None
    with _page_session('diagram', series_ids=_layout_series_ids(layout)) as s:
        if 'fact_version' in body:
            scope = current_workbench.get()
            if scope is not None and scope.scope_version == 2:
                from tin.web.diagram_generation import validate_generated_configuration
                validate_generated_configuration(body.get('markdown'), body['fact_version'])
            else:
                if body['fact_version'] != s.info.get('knowledge_facts_version'):
                    raise HTTPException(409,'知识库来源已改变，请重新生成或核对产业图配置')
                from tin.web.diagram_generation import validate_draft
                validate_draft(body.get('markdown'),[{'id':spec['series_id'],
                    'name':spec.get('metric') or spec.get('name'),'unit':spec.get('unit')}
                    for spec in s.info.get('knowledge_facts_catalog',{}).values()])
        # `into` 表示改写这一张，而不是新建。研究员在文本里改结构比拖方框快，
        # 但改完只能「导入为新布局」的话，每改一次就多一张图，改的还不是手里这张。
        into = body.get("into")
        name = str(body.get("name") or layout["name"]).strip()
        payload = {"name": name, "layout": layout}
        if into:
            row = next((t for t in diagram_api.list_templates(s, V) if t["id"] == into), None)
            if row is None:
                raise HTTPException(404, f"没有这张布局：{into}")
            payload["id"] = into
            payload["name"] = str(body.get("name") or row["name"]).strip()
        try:
            saved = diagram_api.save_template(s, V, payload)
        except diagram_api.DiagramError as e:
            raise HTTPException(400, str(e)) from None
        unbound = sum(1 for n in layout["nodes"]
                      if n.get("kind") != "group" and n["binding"]["kind"] == "none")
        return {"saved": saved, "unbound": unbound, "replaced": bool(into),
                "templates": diagram_api.list_templates(s, V)}


@app.get("/api/sn/diagram/crosscheck")
def diagram_crosscheck(date: str | None = None):
    with _page_session('diagram', day=date) as s:
        return {"checks": diagram_api.crosscheck(s, V, _board_date(s, date))}


@app.get("/api/sn/diagram/detail")
def diagram_detail(series_id: str, date: str | None = None):
    with _page_session('diagram', day=date, series_ids=[series_id]) as s:
        try:
            return diagram_api.detail(s, series_id, _board_date(s, date))
        except diagram_api.DiagramError as e:
            raise HTTPException(404, str(e)) from None


@app.get("/api/sn/diagram/suggest")
def diagram_suggest(label: str, exclude: str | None = None):
    """给某个节点推荐候选指标。给不出就明说，不拿"有数据的热门指标"凑数。"""
    scope = current_workbench.get()
    if scope is not None and scope.scope_version == 2:
        from tin.integration.kb_facts import catalog_page
        page = catalog_page(scope, limit=12, search=label)
        bound = set((exclude or '').split(',')) - {''}
        return {'suggestions': [{'series_id': spec['series_id'], 'name': spec.get('metric') or spec.get('name'),
            'unit': spec.get('unit'), 'frequency': spec.get('frequency') or '未注明', 'points': None,
            'why': '目录字面匹配，绑定前请核对口径', 'already_bound': spec['series_id'] in bound}
            for spec in page['series']]}
    with SessionLocal() as s:
        return {"suggestions": diagram_api.suggest(s, V, label,
                                                   bound=set((exclude or "").split(",")) - {""})}


@app.delete("/api/sn/diagram/templates/{template_id}")
def diagram_delete(template_id: int):
    with _page_session('diagram', template_id=template_id) as s:
        try:
            return diagram_api.delete_template(s, V, template_id)
        except diagram_api.DiagramError as e:
            raise HTTPException(400, str(e)) from None


@app.get("/sn/reports")
def reports_page(request: Request, date: str | None = None, period: str = 'daily'):
    from tin.integration.research_profile import research_variety
    from tin.web.report_period import build_period, period_start
    from tin.web.report_view import present_snapshot
    if period not in {'daily', 'weekly'}:
        raise HTTPException(400, '报告周期应为 daily 或 weekly')
    if date is not None:
        try:
            if CalendarDate.fromisoformat(date).isoformat() != date:
                raise ValueError
        except ValueError:
            raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
    with _page_session('reports', day=date, points_per_series=8 if period == 'weekly' else 2) as s:
        variety = research_variety(s)
        available_days = ([value.isoformat() for value in sorted({value.astimezone(SHANGHAI).date()
                          for value in s.scalars(select(Observation.as_of).where(Observation.status == "正常"))}, reverse=True)[:40]]
                          if s.info.get("workbench_id") else s.scalars(select(TradingDay.trade_date).order_by(TradingDay.trade_date.desc()).limit(40)))
        files = ([{"date": day, "weekday": "一二三四五六日"[CalendarDate.fromisoformat(day).weekday()],
                   "tone": None, "main": None, "gaps": None, "generated_at": None}
                  for day in available_days]
                 if settings.weknora_api_url else archive_days(s, variety))
        selected = next((f for f in files if f["date"] == date),
            {"date": date} if date else files[0] if files else {"date": None})
        preview, report, names = "", None, {}
        chosen = CalendarDate.fromisoformat(selected['date']) if selected.get('date') else (
            datetime.now(SHANGHAI).date() if settings.weknora_api_url else None)
        if chosen is not None:
            path = settings.exports_dir / variety / f"{chosen.isoformat()}.json"
            if settings.weknora_api_url:
                report = build_period(s, variety, chosen, period)
                names = dict(s.execute(select(Indicator.series_id, Indicator.name)).all())
            elif period == 'weekly':
                # Standalone weekly reports use explicit current-source replay,
                # never claim seven regenerated days are frozen archives.
                report = build_period(s, variety, chosen, period)
            elif path.exists():
                report = json.loads(path.read_text(encoding="utf-8"))
            if report is not None:
                if not settings.weknora_api_url and not files and not date and not any(report.get(key) for key in
                        ('judgment', 'briefs', 'strategies', 'research_cards')):
                    report = None
                else:
                    selected = {**selected, 'date': chosen.isoformat()}
            if report is not None:
                preview = json.dumps(report, ensure_ascii=False, indent=2)
        return templates.TemplateResponse(request, "reports.html", _shell(
            s, "reports", None, files=files, selected=selected, preview=preview,
            report=present_snapshot(report, names) if report is not None else None,
            report_generated_at=(report or {}).get('generated_at'), period_kind=period,
            report_start=period_start(chosen, period).isoformat() if chosen else None,
            live_report=bool(settings.weknora_api_url) or period == 'weekly'))


@app.get("/sn/reports/{day}.json")
def report_file(day: str):
    from tin.integration.research_profile import research_variety
    try:
        CalendarDate.fromisoformat(day)
    except ValueError:
        raise HTTPException(400, "日期格式应为 YYYY-MM-DD") from None
    if settings.weknora_api_url:
        return _period_download(day, 'daily')
    path = settings.exports_dir / V / f"{day}.json"
    if not path.exists():
        raise HTTPException(404, "该日没有归档快照")
    return FileResponse(path, media_type="application/json", filename=f"{V}_{day}.json")


def _period_download(day, period, markdown=False):
    from tin.integration.research_profile import research_variety
    from tin.web.report_period import build_period
    from tin.web.report_view import readable_markdown
    try:
        cutoff = CalendarDate.fromisoformat(day)
        if cutoff.isoformat() != day:
            raise ValueError
    except ValueError:
        raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
    with _page_session('reports', day=cutoff, points_per_series=8 if period == 'weekly' else 2) as s:
        variety = research_variety(s)
        payload = build_period(s, variety, cutoff, period)
        if markdown:
            scope = current_workbench.get()
            title = (scope.metadata.get('name') if scope else None) or '研究工作台'
            body = readable_markdown(payload, title=title + (' 周报' if period == 'weekly' else ' 日报'))
            return StreamingResponse(iter([body.encode('utf-8')]), media_type='text/markdown', headers={
                'Content-Disposition': f'attachment; filename="research_{period}_{day}.md"'})
        return JSONResponse(payload, headers={
            'Content-Disposition': f'attachment; filename="research_{period}_{day}.json"'})


@app.get('/sn/reports/{day}/weekly.json')
def weekly_report_json(day: str):
    return _period_download(day, 'weekly')


@app.get('/sn/reports/{day}/weekly.md')
def weekly_report_markdown(day: str):
    return _period_download(day, 'weekly', markdown=True)


@app.get('/sn/reports/{day}/daily.md')
def daily_report_markdown(day: str):
    return _period_download(day, 'daily', markdown=True)


@app.get("/sn/contracts.csv")
def contracts_csv(date: str | None = None):
    with _page_session('market', day=date) as s:
        from tin.web.market_contracts import configured_contract_curve
        if s.info.get('workbench_id'):
            curve = configured_contract_curve(s, _board_date(s, date) if date else None)
            if not curve['date']:
                raise HTTPException(404, '当前知识库尚无已绑定的合约日行情')
            d = CalendarDate.fromisoformat(curve['date'])
        else:
            d = _board_date(s, date)
            curve = contract_curve(s, V, d)
        rows = curve['rows']
        scoped = curve.get('source') == 'knowledge-base'
    buf = io.StringIO()
    writer = csv.writer(buf)
    writer.writerow(["交易日", "合约", "结算价", "收盘价", "成交量", "成交量单位", "持仓量", "持仓量单位", "价格单位", "主力", "流动性"])
    for r in rows:
        cells = [d.isoformat(), r["contract"], r["settle"], r["close"], r["volume"],
                         r.get('volume_unit') if scoped else '手', r["oi"],
                         r.get('oi_unit') if scoped else '手', curve.get('unit') if scoped else '元/吨',
                         "是" if r["main"] else "", "活跃" if r["active"] else
                         "未判定" if scoped and not r['liquidity_comparable'] else "低流动性"]
        writer.writerow(["'" + cell if isinstance(cell, str) and cell.startswith(('=', '+', '-', '@', '\t', '\r'))
                         else cell for cell in cells])
    buf.seek(0)
    return StreamingResponse(iter([buf.getvalue().encode("utf-8-sig")]), media_type="text/csv",
                             headers={"Content-Disposition": f'attachment; filename="{V}_contracts_{d}.csv"'})


# ---------- Excel 批量导入 ----------

XLSX_MEDIA = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"


@app.get("/api/sn/import/template")
def import_template():
    with SessionLocal() as s:
        data = build_import_template(s, V)
    name = f"{V}_批量导入模板_{datetime.now(SHANGHAI):%Y%m%d}.xlsx"
    return StreamingResponse(iter([data]), media_type=XLSX_MEDIA,
                             headers={"Content-Disposition": f'attachment; filename="{quote(name)}"'})


@app.post("/api/sn/import/preview")
async def import_preview(file: UploadFile = File(...), actor: str = Form(...)):
    actor = verified_actor(actor)
    if not actor.strip():
        raise HTTPException(400, "请填写导入操作人")
    data = await file.read()
    name = file.filename or "未命名文件"

    # 数据商终端原样导出的工作簿动辄几十万条，逐行预览既装不下也没人看得完。
    # 它按供应商编码精确对齐，不存在"这列对应哪个指标"的不确定性，该确认的是批次级信息。
    if vendor_terminal.detect(data) is not None:
        with SessionLocal() as s:
            try:
                summary = vendor_terminal.summarize(s, io.BytesIO(data), V)
            except vendor_terminal.VendorFormatError as e:
                raise HTTPException(400, str(e)) from None
        return JSONResponse({**summary, "token": import_jobs.stage(data), "filename": name})

    with SessionLocal() as s:
        try:
            return JSONResponse(excel_importer.build_preview(s, data, name, actor.strip(), V))
        except excel_importer.ImportError_ as e:
            raise HTTPException(400, str(e)) from None


@app.post("/api/sn/import/terminal/commit")
def import_terminal_commit(tasks: BackgroundTasks, body: dict = Body(...)):
    """确认整体导入。立刻返回任务号，几分钟的入库放后台跑。"""
    actor = verified_actor(str(body.get("actor", "")).strip())
    if not actor:
        raise HTTPException(400, "请填写导入操作人")
    try:
        job_id = import_jobs.create_terminal_job(
            str(body.get("token", "")), actor, str(body.get("filename", "未命名文件")), V)
    except ValueError as e:
        raise HTTPException(400, str(e)) from None
    if not import_jobs.staging_path(str(body["token"])).exists():
        raise HTTPException(400, "暂存文件已过期，请重新上传")
    tasks.add_task(import_jobs.execute, job_id)
    return {"job_id": job_id}


@app.get("/api/sn/import/jobs/{job_id}")
def import_job_status(job_id: str):
    state = import_jobs.snapshot(job_id)
    if state is None:
        raise HTTPException(404, "任务不存在或已过期")
    return state


@app.post("/api/sn/import/commit")
def import_commit(body: dict = Body(...)):
    actor = verified_actor(str(body.get("actor", "")).strip())
    note = str(body.get("default_note", "")).strip()
    if not actor or not note:
        raise HTTPException(400, "导入操作人与来源说明均为必填")
    with SessionLocal() as s:
        try:
            result = excel_importer.commit_preview(
                s, str(body.get("preview_id", "")), list(body.get("selected_row_keys") or []),
                dict(body.get("column_overrides") or {}), actor, note)
        except excel_importer.ImportError_ as e:
            raise HTTPException(400, str(e)) from None
        s.add(AuditLog(at=datetime.now(SHANGHAI), actor=actor, action="Excel批量导入",
                       target_type="observation", target_id=str(body.get("preview_id", ""))[:80],
                       detail={"committed": result["committed"], "revisions": result["revisions"],
                               "stale": len(result["stale_rows"]), "note": note,
                               "recalculated_dates": result["recalculated_dates"]}))
        s.commit()
        return JSONResponse(result)


# ---------- Excel 自定义导出 ----------


@app.get("/api/sn/export/fields")
def export_fields():
    with SessionLocal() as s:
        return JSONResponse({"groups": field_catalog(s, V)})


@app.get("/api/sn/export/templates")
def export_templates_list():
    with SessionLocal() as s:
        return JSONResponse({"templates": export_templates.list_templates(s, V)})


@app.post("/api/sn/export/templates")
def export_templates_save(body: dict = Body(...)):
    with SessionLocal() as s:
        try:
            saved = export_templates.save_template(s, V, body)
        except export_templates.TemplateError as e:
            raise HTTPException(400, str(e)) from None
        return JSONResponse({"saved": saved, "templates": export_templates.list_templates(s, V)})


@app.delete("/api/sn/export/templates/{template_id}")
def export_templates_delete(template_id: int):
    with SessionLocal() as s:
        try:
            return JSONResponse(export_templates.delete_template(s, V, template_id))
        except export_templates.TemplateError as e:
            raise HTTPException(404, str(e)) from None


@app.post("/api/sn/export/excel")
def export_excel(body: dict = Body(...)):
    actor = verified_actor(str(body.get("actor", "")).strip())
    if not actor:
        raise HTTPException(400, "请填写导出人姓名（用于合规审计）")
    columns = [Column(field=str(c["field"]), label=str(c.get("label") or c["field"]), kind=str(c["kind"]))
               for c in (body.get("columns") or [])]
    if not columns:
        raise HTTPException(400, "请至少选择一个导出字段")

    with SessionLocal() as s:
        data, meta = build_export(
            s, V, columns, date_range=str(body.get("date_range") or "recent_30_trade_days"),
            start_date=body.get("start_date"), end_date=body.get("end_date"),
            sort_order=str(body.get("sort_order") or "desc"), actor=actor)
        # 商业授权数据带出系统必须留痕（04 §8）
        s.add(AuditLog(at=datetime.now(SHANGHAI), actor=actor, action="Excel数据导出",
                       target_type="export", target_id=f"{V}_{datetime.now(SHANGHAI):%Y%m%d%H%M%S}",
                       detail={"columns": [c.field for c in columns], "rows": meta["rows"],
                               "range": meta["range"], "skipped": meta["skipped"],
                               "commercial_sources": meta["commercial_sources"]}))
        s.commit()

    name = f"{V}_研究数据_{datetime.now(SHANGHAI):%Y%m%d}.xlsx"
    return StreamingResponse(iter([data]), media_type=XLSX_MEDIA, headers={
        "Content-Disposition": f'attachment; filename="{quote(name)}"',
        "X-Export-Rows": str(meta["rows"]),
        "X-Export-Skipped": quote(",".join(meta["skipped"])),
    })


@app.get("/api/sn/snapshot")
def api_snapshot(date: str | None = None):
    with _page_session('reports', day=date) as s:
        return JSONResponse(snapshot(s, V, _board_date(s, date)))


@app.get("/api/sn/series/{series_id}/history")
def api_series_history(series_id: str, date: str | None = None,
                       limit: int = Query(900, ge=2, le=2000)):
    with _page_session('indicators', day=date, series_ids=[series_id], points_per_series=limit) as s:
        day = _home_date(s, date) or datetime.now(SHANGHAI).date()
        payload = indicator_data.history(s, series_id, day, limit)
        if payload is None:
            raise HTTPException(404, "该指标不支持历史图表")
        return JSONResponse(payload)


@app.get("/api/sn/indicators/previews")
def api_indicator_previews(series_id: list[str] = Query(..., min_length=1, max_length=40),
                           date: str | None = None):
    with _page_session('indicators', day=date, series_ids=series_id, points_per_series=180) as s:
        day = _home_date(s, date)
        try:
            cards = indicator_data.previews(s, series_id, day or datetime.now(SHANGHAI).date())
        except ValueError as e:
            raise HTTPException(404, str(e)) from e
        html = templates.get_template("_indicator_cards.html").render(cards=cards, d=day.isoformat() if day else "")
        return {"cards": cards, "html": html}


@app.get("/api/sn/indicators/compare")
def api_indicator_compare(series_id: list[str] = Query(..., min_length=2, max_length=4),
                          date: str | None = None):
    if len(set(series_id)) != len(series_id):
        raise HTTPException(422, "请选择不同的指标")
    with _page_session('indicators', day=date, series_ids=series_id, points_per_series=2000) as s:
        day = _home_date(s, date) or datetime.now(SHANGHAI).date()
        try:
            return indicator_data.compare(s, series_id, day)
        except ValueError as e:
            raise HTTPException(404, str(e)) from e

"""Local management UI plus authenticated, read-only MCP tools."""
from __future__ import annotations

from contextlib import asynccontextmanager
import json
import os
from pathlib import Path
import secrets
import threading
import time
import uuid
from urllib.parse import urlsplit

from fastapi import FastAPI, HTTPException, Request, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings
from pydantic import BaseModel, Field
from starlette.concurrency import run_in_threadpool

from .store import DataStore, dump
from .imports import ImportManager

MAX_UPLOAD = 100 * 1024 * 1024


class MappingRequest(BaseModel):
    mapping: dict
    save_template: bool = True


class SuggestRequest(BaseModel):
    sheet: str


def create_app(data_dir: str | Path | None = None):
    public_origin = os.environ.get("STRUCTURED_PUBLIC_ORIGIN", "").rstrip("/")
    public_authority = ""
    if public_origin:
        public_url = urlsplit(public_origin)
        if (public_url.scheme not in {"http", "https"} or not public_url.hostname
                or public_url.username is not None or public_url.password is not None
                or public_url.path or public_url.query or public_url.fragment
                or "*" in public_origin or any(c.isspace() for c in public_origin)):
            raise ValueError("STRUCTURED_PUBLIC_ORIGIN must be one HTTP(S) origin")
        # Validate a supplied port before accepting the authority as a Host.
        _ = public_url.port
        public_authority = public_url.netloc

    def expected_origin(request: Request) -> str:
        authority = request.headers.get("host", "")
        return public_origin if public_authority and authority == public_authority else f"http://{authority}"

    root = Path(data_dir or os.environ.get("STRUCTURED_DATA_DIR", os.environ.get("COTTON_DATA_DIR", "data"))).resolve()
    root.mkdir(parents=True, exist_ok=True)
    token_path = root / "service-token"
    if not token_path.exists():
        try:
            with open(token_path, "x", opener=lambda p, flags: os.open(p, flags, 0o600)) as f:
                f.write(secrets.token_urlsafe(32))
        except FileExistsError:
            pass
    token = token_path.read_text().strip()
    if not token:
        raise RuntimeError("Empty service token")
    store = DataStore(root)
    manager = ImportManager(root)
    from .field_resolver import FieldResolver
    resolver = FieldResolver(store)
    sessions: dict[str, float] = {}
    jobs: dict[str, dict] = {}
    job_lock = threading.Lock()
    import_lock = threading.Lock()
    hosts = ["127.0.0.1:*", "localhost:*", "structured-data:*", "cotton-data:*", "host.docker.internal:*"]
    origins = ["http://127.0.0.1:*", "http://localhost:*"]
    if public_authority:
        hosts.append(public_authority)
        origins.append(public_origin)
    mcp = FastMCP("结构化数据工作台", stateless_http=True, json_response=True,
                  transport_security=TransportSecuritySettings(allowed_hosts=hosts, allowed_origins=origins))

    @mcp.tool()
    def resolve_fields(question: str, dataset_id: str | None = None, context_sheet: str | None = None, prior_series_ids: list[str] | None = None, date: str | None = None, sheet: str | None = None) -> dict:
        """用大模型知识理解用户别称和真实表头的关系。只发送字段目录，不发送整表数据。question写完整用户意图及必要的上文；context_sheet及prior_series_ids用于连续追问。sheet仅在用户明确限制工作表时填写。返回真实候选列、理由和查询/计算方案。needs_clarification会触发网页选择弹窗：立即结束本轮并等用户选择，不能猜选或取数。resolved会同时返回answer中的原值或确定性计算结果及来源，直接据此回答，不要另行心算。dataset_id可省略，只有一份文件时自动选最新版本；多文件需指定。"""
        return resolver.tool_answer(question,dataset_id,context_sheet,prior_series_ids,date,sheet)

    @mcp.tool()
    def confirm_fields(resolution_id: str, selections: dict[str,str] | None = None, note: str = '') -> dict:
        """仅在用户回复字段确认弹窗后使用。resolution_id来自待确认结果；selections是用户选择的角色v1等到真实series_id映射；用户自行补充的文字放note，会重新识别。不能替用户选择；结果仍needs_clarification时再次等待。"""
        try:
            return resolver.attach_answer(resolver.confirm(resolution_id,selections,note))
        except ValueError as exc:
            return resolver.tool_notice('confirmation_unavailable',str(exc),False)

    @mcp.tool()
    def execute_field_plan(resolution_id: str, date: str | None = None) -> dict:
        """执行已明确/经用户确认的字段方案。待确认方案会拒绝。具体日期可传YYYY-MM-DD或沿用方案日期；返回原数值及双方来源，四则计算用Decimal，不能自行改写返回的计算结果。每个输入必须是同日唯一有效值；未知、缺失不补造。"""
        return resolver.execute(resolution_id,date)

    @mcp.tool()
    def list_datasets() -> dict:
        """列出已完成的Excel数据集、文件版本及支持/排除区域。同名文件默认选is_latest_for_source=true；用户指定历史版本时按指定版本。不把旧数据当实时行情。"""
        # Historical versions need identity metadata, not repeated full coverage
        # inventories. Keep the management API's full report unchanged.
        datasets=[]
        for source in store.list_datasets():
            item={k:source[k] for k in ('dataset_id','source_file','imported_at','is_latest_for_source','series_count')}
            if source['is_latest_for_source']:
                item['sheets']=[s['name'] for s in source['sheets']]
                item['pending_sheets']=source['pending_sheets']
                item['coverage_notice']=source.get('coverage_notice','仅查询已导入范围。')
            datasets.append(item)
        return {"datasets": datasets}

    @mcp.tool()
    def list_charts(dataset_id: str, sheet: str | None = None, search: str = '', limit: int = 50, offset: int = 0) -> dict:
        """列出Excel原生图表、标题、图例、源数据引用及关联状态。图片不解析。linked曲线的series_id可交给query_series查询，日期使用源表日期；pending_source先确认源表映射，needs_review不可猜测年份。部分关联只覆盖linked曲线。问图表优先resolve_fields；不能把未解析曲线说成没有原数据。"""
        return store.list_charts(dataset_id, sheet, search, limit, offset)

    @mcp.tool()
    def list_series(dataset_id: str, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None) -> dict:
        """字面查指标ID、单位、日期范围。用户用别称、概念或连续追问时优先resolve_fields语义识别，不靠手写同义词。search空格分词AND。sheet严格过滤；context_sheet仅优先排序。partial_candidates须核对口径；truncated用next_offset分页。pending_scope只说明对应表未导入，不说明相关主题在其他已导入列中不存在。"""
        return store.list_series(dataset_id, search, limit, offset, sheet, context_sheet)

    @mcp.tool()
    def query_series(dataset_id: str, series_id: str, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None) -> dict:
        """确定性查询：具体日期用YYYY-MM-DD，起止均包含。date_mode=period时用period精确匹配原标签，不可转为日期；date_mode=none不填时间。不带日期的系列不支持latest；期间统计须指定period。operation=rows/latest/mean/sum/min/max/count。min/max返回aggregate及真正取得极值的rows/extremum_rows，日期只能用其中的date，actual_first_date/actual_last_date仅是范围边界。并列极值的数量看extremum_count，超过limit会truncated。求最高/最低日期不需要用户先给日期；未限定时间则不填日期查询全范围。latest返回最新有效数值；mean为算术平均；count为有效值数。缺失不作零。mean/sum/count的rows仅示例来源，aggregate才是统计值。返回原文件、sheet/cell和公式缓存来源。"""
        return store.query_series(dataset_id, series_id, start_date, end_date, operation, limit, period)

    @mcp.tool()
    def convert_price(dataset_id: str, price_series_id: str, fx_series_id: str, date: str, target_unit: str = '元/吨') -> dict:
        """用同日原价格与美元兑人民币汇率确定性换算，避免模型心算。先list_series选两项准确ID，追问汇率优先同表。输入价格单位必须为美元或美分/磅、公斤、千克、吨；目标支持元/吨(默认)、元/磅、元/公斤、元/千克。date格式YYYY-MM-DD。返回数值、公式及价格/汇率双方原单元格。不同币种方向、未知单位、缺失数据拒绝；不包含税费与通关成本。"""
        return store.convert_price(dataset_id,price_series_id,fx_series_id,date,target_unit)

    @mcp.tool()
    def compare_series(dataset_id: str, left_series_id: str, right_series_id: str, date: str) -> dict:
        """同日两个同单位指标作差，严格按左减右。额外允许原表同一合约收盘/结算的原始数值比较，但单位未知必须明说。先从list_series取得准确ID，回答附双方原单元格。缺失不补造，其他单位未知/不一致拒绝。"""
        return store.compare_series(dataset_id, left_series_id, right_series_id, date)

    mcp_app = mcp.streamable_http_app()

    @asynccontextmanager
    async def lifespan(app):
        async with mcp.session_manager.run():
            yield

    app = FastAPI(title="结构化数据工作台", lifespan=lifespan)
    app.state.store = store
    app.state.import_manager = manager
    app.state.mcp = mcp

    async def verify_weknora(authorization, active_tenant=None):
        import httpx
        base=os.environ.get('WEKNORA_API_URL','http://127.0.0.1:18081')
        expected=os.environ.get('STRUCTURED_TENANT_ID',os.environ.get('COTTON_TENANT_ID','10000'))
        if active_tenant and active_tenant != expected:
            raise HTTPException(403,'此工作台尚未配置当前空间的数据目录。')
        try:
            async with httpx.AsyncClient(trust_env=False,timeout=10) as client:
                result=await client.get(base+'/api/v1/auth/me',headers={'Authorization':authorization})
        except httpx.HTTPError:
            raise HTTPException(503,'无法核验 WeKnora 登录，请稍后重试。') from None
        if result.status_code != 200:raise HTTPException(401,'WeKnora 登录已过期，请重新登录。')
        identity=result.json(); user=identity.get('data',identity); user=user.get('user',user)
        if str(user.get('tenant_id')) != expected:
            raise HTTPException(403,'此工作台尚未配置当前空间的数据目录。')

    @app.middleware("http")
    async def protect(request: Request, call_next):
        authority = request.headers.get("host", "")
        host = authority.split(":")[0]
        if host not in {"127.0.0.1", "localhost", "structured-data", "cotton-data", "host.docker.internal", "testserver"} and not (public_authority and authority == public_authority):
            return JSONResponse({"detail": "Host not allowed"}, status_code=403)
        path = request.url.path
        if path in {"/", "/health", "/login", "/api/ui-config"}:
            return await call_next(request)
        if path.startswith("/mcp"):
            auth = request.headers.get("authorization", "")
            if not secrets.compare_digest(auth, "Bearer " + token):
                return JSONResponse({"detail": "Unauthorized"}, status_code=401)
        else:
            authorization=request.headers.get('authorization','')
            if authorization.startswith('Bearer '):
                try:
                    await verify_weknora(authorization,request.headers.get('x-tenant-id'))
                except HTTPException as exc:
                    return JSONResponse({'detail':exc.detail},status_code=exc.status_code)
            else:
                sid = request.cookies.get("structured_session", request.cookies.get("cotton_session", ""))
                if sessions.get(sid, 0) < time.time():
                    return JSONResponse({"detail": "请先从本地 WeKnora 登录。"}, status_code=401)
            if request.method not in {"GET", "HEAD"}:
                origin = request.headers.get("origin", "")
                if origin != expected_origin(request):
                    return JSONResponse({"detail": "Origin not allowed"}, status_code=403)
        return await call_next(request)

    @app.get("/")
    def index():
        return FileResponse(Path(__file__).with_name("index.html"))

    @app.get("/health")
    def health():
        return {"status": "ok", "version": "0.3.0"}

    @app.get("/api/ui-config")
    def ui_config():
        from .ui_config import read_ui_config
        return read_ui_config(store.root)

    @app.post("/login")
    async def login(request: Request):
        # Credentials go only to the configured local WeKnora and are never stored.
        import httpx

        origin = request.headers.get("origin", "")
        if origin != expected_origin(request):
            raise HTTPException(403, "Origin not allowed")
        body = await request.json()
        base = os.environ.get("WEKNORA_API_URL", "http://127.0.0.1:18081")
        async with httpx.AsyncClient(trust_env=False, timeout=10) as client:
            if not body.get("email") or not body.get("password"):
                raise HTTPException(401, "请输入 WeKnora 邮箱和密码。")
            authenticated = await client.post(base + "/api/v1/auth/login", json={"email": body["email"], "password": body["password"]})
            if authenticated.status_code != 200:
                raise HTTPException(401, "邮箱或密码错误，请使用本地 WeKnora 账号。")
            user_token = authenticated.json().get("token", "")
            result = await client.get(base + "/api/v1/auth/me", headers={"Authorization": "Bearer " + user_token})
        if result.status_code != 200:
            raise HTTPException(401, "WeKnora 登录状态无效。")
        identity = result.json()
        user = identity.get("data", identity)
        if "user" in user:
            user = user["user"]
        tenant = user.get("tenant_id")
        expected = os.environ.get("STRUCTURED_TENANT_ID", os.environ.get("COTTON_TENANT_ID", "10000"))
        if str(tenant) != expected:
            raise HTTPException(403, "当前结构化试点只开放给已配置的测试租户。")
        now = time.time()
        for sid, expiry in list(sessions.items()):
            if expiry < now:
                sessions.pop(sid, None)
        sid = secrets.token_urlsafe(32)
        sessions[sid] = now + 8 * 3600
        response = JSONResponse({"ok": True})
        response.set_cookie("structured_session", sid, httponly=True, samesite="strict", max_age=8*3600)
        return response

    @app.get('/api/model-settings')
    def model_settings():
        from .model_settings import public_model
        return public_model(root)

    @app.post('/api/model-settings')
    async def update_model_settings(request: Request):
        from .model_settings import save_model
        return save_model(root,await request.json())

    @app.get("/api/datasets")
    def datasets():
        return {"datasets": store.list_datasets()}

    @app.get("/api/datasets/{dataset_id}/series")
    def series(dataset_id: str, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None):
        return store.list_series(dataset_id, search, limit, offset, sheet, context_sheet)

    @app.get("/api/datasets/{dataset_id}/charts")
    def charts(dataset_id: str, sheet: str | None = None, search: str = '', limit: int = 50, offset: int = 0):
        return store.list_charts(dataset_id, sheet, search, limit, offset)

    @app.get("/api/datasets/{dataset_id}/chart-series/{series_id}")
    def chart_series(dataset_id: str, series_id: str):
        if not series_id.startswith('chart_'):
            raise ValueError('请选择图表指标。')
        return store._series(dataset_id, series_id)

    @app.get("/api/datasets/{dataset_id}/report")
    def report(dataset_id: str):
        issues = json.loads((store._path(dataset_id)/"issues.json").read_text())
        return {"manifest": store.manifest(dataset_id), "issues": issues[:200], "issues_total": len(issues), "issues_truncated": len(issues)>200}

    @app.get("/api/datasets/{dataset_id}/source")
    def source(dataset_id: str):
        path = store._path(dataset_id) / "source.xlsx"
        if not path.exists():
            raise HTTPException(404, "此数据集没有保存源文件副本。")
        return FileResponse(path, filename=Path(store.manifest(dataset_id)["source_file"]).name)

    @app.get("/api/query")
    def query(dataset_id: str, series_id: str, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None):
        return store.query_series(dataset_id, series_id, start_date, end_date, operation, limit, period)

    def run_import(job_id: str, path: Path):
        try:
            result = manager.ingest(path)
            with job_lock:
                jobs[job_id] = {"id": job_id, "status": "completed", "result": result}
        except Exception as exc:
            with job_lock:
                jobs[job_id] = {"id": job_id, "status": "failed", "error": str(exc)}
        finally:
            path.unlink(missing_ok=True)
            path.parent.rmdir()
            import_lock.release()

    @app.post("/api/import")
    async def upload(file: UploadFile):
        if not file.filename or not file.filename.lower().endswith(".xlsx"):
            raise HTTPException(400, "目前支持 .xlsx 文件。")
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, "已有导入任务，请等待完成。")
        job_id = uuid.uuid4().hex
        uploads = root / "uploads" / job_id
        uploads.mkdir(parents=True)
        path = uploads / Path(file.filename.replace("\\", "/")).name
        try:
            size = 0
            with path.open("wb") as f:
                while chunk := await file.read(1024*1024):
                    size += len(chunk)
                    if size > MAX_UPLOAD:
                        raise HTTPException(413, "文件超过100MB。")
                    f.write(chunk)
            with job_lock:
                jobs[job_id] = {"id": job_id, "status": "processing", "stage": "扫描全部工作表并导入结构明确的表，不调用模型"}
            threading.Thread(target=run_import, args=(job_id, path), daemon=True).start()
        except Exception:
            path.unlink(missing_ok=True)
            uploads.rmdir()
            import_lock.release()
            raise
        return {"id": job_id, "status": "processing"}

    @app.get("/api/jobs/{job_id}")
    def job(job_id: str):
        with job_lock:
            if job_id not in jobs:
                raise HTTPException(404, "任务不存在；服务重启后请查看数据集列表确认导入结果。")
            return jobs[job_id]

    @app.get("/api/imports")
    def imports():
        return {"imports": manager.list()}

    @app.get("/api/imports/{import_id}")
    def import_review(import_id: str):
        doc = manager.get(import_id)
        return {k: v for k, v in doc.items() if k != "source_path"}

    @app.get("/api/imports/{import_id}/sheet")
    def sheet_review(import_id: str, name: str):
        return manager.sheet(import_id, name)

    @app.post('/api/imports/{import_id}/rescan')
    async def rescan(import_id: str):
        manager.path(import_id)
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409,'已有导入或确认任务，请等待完成。')
        try:
            doc=manager.get(import_id)
            return await run_in_threadpool(manager.ingest,manager.source_path(doc))
        finally:
            import_lock.release()

    @app.post("/api/imports/{import_id}/preview")
    async def preview(import_id: str, body: MappingRequest):
        return await run_in_threadpool(manager.preview, import_id, body.mapping)

    def run_confirm(job_id, import_id, body):
        try:
            result = manager.confirm(import_id, body.mapping, body.save_template)
            with job_lock:
                jobs[job_id] = {"id": job_id, "status": "completed", "result": result}
        except Exception as exc:
            with job_lock:
                jobs[job_id] = {"id": job_id, "status": "failed", "error": str(exc)}
        finally:
            import_lock.release()

    @app.post("/api/imports/{import_id}/confirm")
    def confirm(import_id: str, body: MappingRequest):
        manager.path(import_id)
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, "已有导入或确认任务，请等待完成。")
        job_id = uuid.uuid4().hex
        with job_lock:
            jobs[job_id] = {"id": job_id, "status": "processing", "stage": "校验映射并保存新版本"}
        threading.Thread(target=run_confirm, args=(job_id, import_id, body), daemon=True).start()
        return {"id": job_id, "status": "processing"}

    @app.post("/api/imports/{import_id}/suggest")
    async def suggest(import_id: str, body: SuggestRequest):
        return await run_in_threadpool(manager.suggest, import_id, body.sheet)

    @app.get("/api/templates")
    def templates():
        return {"templates": manager.list_templates()}

    @app.exception_handler(ValueError)
    async def value_error(request, exc):
        return JSONResponse({"detail": str(exc)}, status_code=400)

    app.mount("/", mcp_app)
    return app

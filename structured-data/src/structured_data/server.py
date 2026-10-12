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
import contextvars
import inspect
from typing import get_type_hints
from urllib.parse import urlsplit

from fastapi import FastAPI, HTTPException, Request, UploadFile
from fastapi.responses import FileResponse, JSONResponse
from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings
from pydantic import BaseModel, Field, ConfigDict, create_model, ValidationError
from starlette.concurrency import run_in_threadpool

from .store import DataStore, dump
from .imports import ImportManager
from .workspaces import WorkspaceRegistry, Identity, current_identity

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
    pilot = int(os.environ.get('STRUCTURED_TENANT_ID', os.environ.get('COTTON_TENANT_ID', '10000')))
    registry = WorkspaceRegistry(root, pilot)
    store, manager, resolver = (registry.proxy(name) for name in ('store', 'manager', 'resolver'))
    sessions: dict[str, dict] = {}
    jobs: dict[str, dict] = {}
    job_lock = threading.Lock()
    import_lock = registry.proxy('import_lock')
    hosts = ["127.0.0.1:*", "localhost:*", "structured-data:*", "cotton-data:*", "host.docker.internal:*"]
    origins = ["http://127.0.0.1:*", "http://localhost:*"]
    if public_authority:
        hosts.append(public_authority)
        origins.append(public_origin)
    mcp = FastMCP("结构化数据工作台", stateless_http=True, json_response=True,
                  transport_security=TransportSecuritySettings(allowed_hosts=hosts, allowed_origins=origins))

    @mcp.tool()
    async def resolve_fields(question: str, dataset_id: str | None = None, context_sheet: str | None = None, prior_series_ids: list[str] | None = None, date: str | None = None, sheet: str | None = None, pending_resolution_id: str | None = None, previous_resolution_id: str | None = None) -> dict:
        """用大模型知识理解用户别称和真实表头的关系。只发送字段目录，不发送整表数据。question描述当前需要识别或执行的完整子任务，保留相关条件；组合分析可先用list_series绑定真实字段，再交analyze_table，不要求此工具表达整套分析。previous_resolution_id只能来自上一轮真实工具结果，系统据此读取完整任务，理解续问、补充和改口。context_sheet及prior_series_ids仅用于候选召回。sheet仅在用户明确限制工作表时填写。返回真实候选列、理由和查询/计算方案。needs_clarification会在网页展示实际待补信息：等用户补充或选择，不能代替用户确认或取数；系统错误不属于业务歧义。resolved返回answer原值/计算和来源；检查是否满足本轮完整需求，必要时组合其他工具，最终解释实际覆盖，不要另行心算。dataset_id可省略，只有一份文件时自动选最新版本；多文件需指定。"""
        return await run_in_threadpool(resolver.tool_answer,question,dataset_id,context_sheet,prior_series_ids,date,sheet,pending_resolution_id,previous_resolution_id)

    @mcp.tool()
    async def confirm_fields(resolution_id: str, selections: dict[str,str] | None = None, note: str = '') -> dict:
        """仅在用户回复字段确认弹窗后使用。resolution_id来自待确认结果；selections是用户选择的角色v1等到真实series_id映射；用户自行补充的文字放note，会重新识别。不能替用户选择；结果仍needs_clarification时再次等待。"""
        try:
            return await run_in_threadpool(lambda: resolver.attach_answer(resolver.confirm(resolution_id,selections,note)))
        except ValueError as exc:
            return resolver.tool_notice('confirmation_unavailable',str(exc),False)

    @mcp.tool()
    async def execute_field_plan(resolution_id: str, date: str | None = None) -> dict:
        """执行已明确/经用户确认的字段方案。待确认方案会拒绝。具体日期可传YYYY-MM-DD或沿用方案日期；返回原数值及双方来源，四则计算用Decimal，不能自行改写返回的计算结果。每个输入必须是同日唯一有效值；未知、缺失不补造。"""
        return await run_in_threadpool(resolver.execute,resolution_id,date)

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
    def inspect_source(dataset_id: str, sheet: str | None = None, searches: list[str] | None = None,
                       ranges: list[str] | None = None, sums: list[list[str]] | None = None,
                       max_matches: int = 10, max_cells: int = 200,
                       padding_rows: int = 2, padding_cols: int = 2) -> dict:
        """只读核验保留的原 Excel，包括 parsed_range 外区域。dataset_id 必须来自真实目录，文件 SHA256 必须与该版本一致。searches 是原值/公式的字面匹配（最多8词），可跨全部列；未指定 sheet 时按原工作簿顺序扫描，预算可能耗在其他表，目标表根本未扫描。已定位字段后必须指定该字段的真实 sheet 定向检索。跨多表搜索截断时看 scan.unscanned_sheets/incomplete_sheets、truncated 和 next_step；matches=[] 不等于没有数据，继续目标表检索。ranges 支持 A1、$绝对坐标、大小写及同工作表前缀；给出 sheet，或每个引用都带同一真实工作表前缀，最多8段、总格数不超过 max_cells(1..400)，返回每格原值、坐标及公式缓存。sums 最多8组、每组1..16个A1引用（单格或有限矩形范围，如 C74:C85、D74:D85），展开后每组至少2格；组内重叠坐标拒绝，组间重复按实际输出格数计入 max_cells。同样允许同表前缀与 $，不接受公式、整列、跨表或外部文件，由 Decimal 求和并返回全部展开原单元格；缺失/错误/无缓存不补零。max_matches 1..30；邻域 padding_rows 0..3、padding_cols 0..4。只检视原件，不重解析、不重新计算公式、不猜单位或年度；年度小表可能与月度汇总/不同定义冲突，必须核对并展示，不能自行选优先表。"""
        from .source_inspection import inspect_source as run_inspection
        try:
            return run_inspection(store, dataset_id, sheet, searches, ranges, sums,
                                  max_matches, max_cells, padding_rows, padding_cols)
        except ValueError as exc:
            error = str(exc)
            damaged_original = any(marker in error for marker in ('SHA256', '原 Excel 发生变化', '没有保留原 Excel'))
            return {'kind': 'structured_source_inspection', 'status': 'error', 'error': error,
                    'retryable': not damaged_original,
                    'next_step': '原件完整性无法核验；重新导入可信原件并获取真实版本目录，不使用本次数据作答'
                    if damaged_original else '缩小读取范围或修正参数；数据集编号必须重新从当前空间的版本目录取得。这是系统反馈，不是原表没有数据。'}

    @mcp.tool()
    def list_series(dataset_id: str | None = None, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None) -> dict:
        """字面查指标ID、单位、日期范围。dataset_id可省略，仅有一个当前文件时自动选择；多文件必须先取得真实ID，不猜ID。用户用别称、概念或连续追问时优先resolve_fields语义识别，不靠手写同义词。search空格分词AND。sheet严格过滤；context_sheet仅优先排序。partial_candidates须核对口径；truncated用next_offset分页。pending_scope只说明对应表未导入，不说明相关主题在其他已导入列中不存在。"""
        return store.list_series(store.select_dataset(dataset_id), search, limit, offset, sheet, context_sheet)

    @mcp.tool()
    def query_series(series_id: str, dataset_id: str | None = None, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None) -> dict:
        """确定性查询：dataset_id可省略，只有一个当前文件时自动选择；具体日期用YYYY-MM-DD，起止均包含。date_mode=period时用period精确匹配原标签，不可转为日期；date_mode=none不填时间。不带日期的系列不支持latest；期间统计须指定period。operation=rows/latest/mean/sum/min/max/count。min/max返回aggregate及真正取得极值的rows/extremum_rows，日期只能用其中的date，actual_first_date/actual_last_date仅是范围边界。并列极值的数量看extremum_count，超过limit会truncated。求最高/最低日期不需要用户先给日期；未限定时间则不填日期查询全范围。latest返回最新有效数值；mean为算术平均；count为有效值数。缺失不作零。mean/sum/count的rows仅示例来源，aggregate才是统计值。返回原文件、sheet/cell和公式缓存来源。"""
        return store.query_series(store.select_dataset(dataset_id), series_id, start_date, end_date, operation, limit, period)

    @mcp.tool()
    def convert_price(dataset_id: str, price_series_id: str, fx_series_id: str, date: str, target_unit: str = '元/吨') -> dict:
        """用同日原价格与美元兑人民币汇率确定性换算，避免模型心算。先list_series选两项准确ID，追问汇率优先同表。输入价格单位必须为美元或美分/磅、公斤、千克、吨；目标支持元/吨(默认)、元/磅、元/公斤、元/千克。date格式YYYY-MM-DD。返回数值、公式及价格/汇率双方原单元格。不同币种方向、未知单位、缺失数据拒绝；不包含税费与通关成本。"""
        return store.convert_price(dataset_id,price_series_id,fx_series_id,date,target_unit)

    @mcp.tool()
    def compare_series(dataset_id: str, left_series_id: str, right_series_id: str, date: str) -> dict:
        """同日两个同单位指标作差，严格按左减右。额外允许原表同一合约收盘/结算的原始数值比较，但单位未知必须明说。先从list_series取得准确ID，回答附双方原单元格。缺失不补造，其他单位未知/不一致拒绝。"""
        return store.compare_series(dataset_id, left_series_id, right_series_id, date)

    @mcp.tool()
    async def get_result_evidence(resolution_id: str) -> dict:
        """读取上次查询的事实而不重新理解问题：请求范围、各字段实际记录/有效值范围、数值、来源、缺失情况和公式。resolution_id来自真实历史工具结果，亦可填写analyze_table返回的result_id以读取该次分析。日期边界不是极值日期。旧结果没有保存证据时只重新执行原计划，不调用意图模型。"""
        def read():
            import re
            if not isinstance(resolution_id,str) or not re.fullmatch(r'[a-f0-9]{24}',resolution_id):raise ValueError('结果编号无效')
            analysis_path=store.root/'analysis-results'/(resolution_id+'.json')
            if analysis_path.is_file():return json.loads(analysis_path.read_text())
            plan=json.loads(resolver._path(resolution_id).read_text())
            if plan['status'] not in ('resolved','no_data'):
                return {'status':plan['status'],'question':plan.get('question'),'notice':'此方案尚未得到可执行结果'}
            from .result_evidence import result_evidence
            evidence=plan.get('result_evidence') or result_evidence(resolver.execute(resolution_id))
            return {'kind':'structured_result_evidence','status':'ok','resolution_id':resolution_id,'original_question':plan['original_question'],'evidence':evidence}
        return await run_in_threadpool(read)

    @mcp.tool()
    async def analyze_table(series_ids: list[str], sql: str, dataset_id: str | None = None, start_date: str | None = None, end_date: str | None = None, limit: int = 200) -> dict:
        """只读通用分析SQL；先取得真实series_id并核验口径。dataset_id可省略，只有一个当前文件时自动选择；多文件先取得真实ID，不猜ID。隔离表data仅含指定字段/日期范围的全部记录（最多50000条），列series_id VARCHAR,date DATE,value DOUBLE,source_cell,date_cell,period,value_status,metric,sheet,unit均VARCHAR；缺失value为NULL。SQL方言为DuckDB，支持SELECT/WITH、四则、GROUP BY、JOIN、窗口/滚动与多阶段分析，无需预定义问题类型。优先使用原生聚合、窗口和日期函数，例如date_trunc(part,date)；不要自行拼日历算法，CAST小数到整数会舍入而非向下取整，向下取整用floor。输出columns和rows（按列顺序的二维数组）、SQL及输入覆盖/来源；输出截断不影响完整输入计算。所有聚合必须使用完整输入，按需WHERE value IS NOT NULL，SQL不自动排除零、换算单位或解释年度。输入过大/SQL错误时根据反馈修正，不让用户选字段。每个输入最多一个文件版本，不允许读取文件、联网、修改数据或多条语句。不要混加存量、累计值与当期流量。"""
        from .table_analysis import analyze_table as run_analysis
        try:
            return await run_in_threadpool(run_analysis,store,store.select_dataset(dataset_id),series_ids,sql,start_date,end_date,limit)
        except ValueError as exc:
            return {'kind':'structured_analysis','status':'error','error':str(exc),'retryable':True,'next_step':'修正SQL或输入范围；这是系统反馈，不是字段确认请求'}

    read_operations = {name: fn for name, fn in locals().copy().items() if name in {
        'list_datasets','list_series','list_charts','query_series','compare_series','convert_price',
        'resolve_fields','confirm_fields','execute_field_plan','get_result_evidence','analyze_table','inspect_source'}}
    argument_models = {}
    for name, fn in read_operations.items():
        hints = get_type_hints(fn)
        fields = {key:(hints.get(key,object), ... if value.default is inspect.Parameter.empty else value.default)
                  for key,value in inspect.signature(fn).parameters.items()}
        argument_models[name] = create_model(name+'_Arguments', __config__=ConfigDict(extra='forbid',strict=True), **fields)

    mcp_app = mcp.streamable_http_app()

    @asynccontextmanager
    async def lifespan(app):
        async with mcp.session_manager.run():
            yield

    app = FastAPI(title="结构化数据工作台", lifespan=lifespan)
    app.state.store = store
    app.state.import_manager = manager
    app.state.mcp = mcp
    app.state.workspaces = registry

    @app.post('/api/tools/read')
    async def native_read(request: Request):
        body = await request.json()
        if not isinstance(body, dict) or set(body) != {'operation','arguments'}:
            raise HTTPException(400, '只接受 operation 和 arguments')
        if not isinstance(body['operation'], str) or not isinstance(body['arguments'], dict):
            raise HTTPException(400, '未知只读操作')
        if body['operation'] == 'describe_tools':
            selected = body['arguments'].get('operations', [])
            if (set(body['arguments']) - {'operations'} or not isinstance(selected, list)
                    or len(selected)>len(read_operations) or any(not isinstance(n,str) or n not in read_operations for n in selected)):
                raise HTTPException(400, '请指定目录中的操作名称')
            tools = await mcp.list_tools()
            return {'tools':[{'name':tool.name, 'description':tool.description,
                              **({'inputSchema':tool.inputSchema} if selected else {})}
                             for tool in tools if not selected or tool.name in selected]}
        fn = read_operations.get(body['operation'])
        if fn is None or not isinstance(body['arguments'], dict):
            raise HTTPException(400, '未知只读操作')
        try:
            arguments = argument_models[body['operation']].model_validate(body['arguments']).model_dump()
        except (TypeError,ValidationError):
            raise HTTPException(400, '查询参数与操作不匹配') from None
        if inspect.iscoroutinefunction(fn):
            return await fn(**arguments)
        return await run_in_threadpool(lambda: fn(**arguments))

    @app.post('/api/research/export')
    async def research_export(request: Request):
        body = await request.json()
        if (not isinstance(body, dict) or set(body) - {'dataset_id','series_id','offset','limit'}
                or not body.get('dataset_id') or not body.get('series_id')):
            raise HTTPException(400, '请指定真实数据集版本和字段')
        offset, limit = body.get('offset', 0), body.get('limit', 10000)
        if type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 10000:
            raise HTTPException(400, '导出分页参数无效')
        dataset, series_id = body['dataset_id'], body['series_id']
        spec = store._series(dataset, series_id)
        with store._connect(dataset) as con:
            total = con.execute('SELECT count(*) FROM observations WHERE series_id=?', [series_id]).fetchone()[0]
            cursor = con.execute('SELECT * FROM observations WHERE series_id=? ORDER BY source_cell LIMIT ? OFFSET ?', [series_id, limit, offset])
            names = [column[0] for column in cursor.description]
            rows = [dict(zip(names, row)) for row in cursor.fetchall()]
        return {'manifest':store.manifest(dataset), 'series':spec, 'rows':rows, 'total':total,
                'offset':offset, 'next_offset':offset+len(rows) if offset+len(rows)<total else None,
                'truncated':offset+len(rows)<total}

    @app.get('/api/research/source/{dataset_id}')
    def research_source(dataset_id: str):
        return source(dataset_id)

    async def verify_weknora(authorization, active_tenant=None):
        import httpx
        base=os.environ.get('WEKNORA_API_URL','http://127.0.0.1:18081')
        headers = {'Authorization': authorization}
        if active_tenant:
            headers['X-Tenant-ID'] = str(active_tenant)
        try:
            async with httpx.AsyncClient(trust_env=False,timeout=10,follow_redirects=False) as client:
                result=await client.get(base+'/api/v1/auth/me',headers=headers)
        except httpx.HTTPError:
            raise HTTPException(503,'无法核验 WeKnora 登录，请稍后重试。') from None
        if result.status_code != 200:raise HTTPException(401,'WeKnora 登录已过期，请重新登录。')
        doc = result.json(); data = doc.get('data', doc)
        if not isinstance(data,dict) or data.get('tenant_required') or ('tenant' in data and not isinstance(data['tenant'],dict)):
            raise HTTPException(403, '请先选择有权限的空间')
        user = data.get('user', data)
        if not isinstance(user,dict):
            raise HTTPException(403, '没有已核验的空间成员权限')
        tenant = data.get('tenant', {}).get('id', user.get('tenant_id'))
        try:
            tenant = int(tenant)
            if tenant <= 0 or (active_tenant and tenant != int(active_tenant)):
                raise ValueError('workspace mismatch')
            membership = next((m for m in data.get('memberships', []) if int(m['tenant_id']) == tenant), None)
            role = membership.get('role') if membership else None
            if user.get('is_active') is False or not user.get('id') or role not in {'owner','admin','contributor','viewer'}:
                raise ValueError('membership missing')
            return Identity(str(user['id']), str(user.get('email','')), str(user.get('username') or user.get('email','')), tenant, role)
        except (ValueError, TypeError, KeyError):
            raise HTTPException(403, '没有已核验的空间成员权限') from None

    @app.middleware("http")
    async def protect(request: Request, call_next):
        authority = request.headers.get("host", "")
        host = authority.split(":")[0]
        if host not in {"127.0.0.1", "localhost", "structured-data", "cotton-data", "host.docker.internal", "testserver"} and not (public_authority and authority == public_authority):
            return JSONResponse({"detail": "Host not allowed"}, status_code=403)
        path = request.url.path
        if path in {"/", "/health", "/login", "/api/ui-config"}:
            return await call_next(request)
        identity = None
        signed = path in {'/api/tools/read', '/api/research/export'} or path.startswith('/api/research/source/')
        if signed:
            from .service_auth import verify_service
            try:
                identity = await verify_service(request)
            except HTTPException as exc:
                return JSONResponse({'detail': exc.detail}, status_code=exc.status_code)
        elif path.startswith("/mcp"):
            auth = request.headers.get("authorization", "")
            if not secrets.compare_digest(auth, "Bearer " + token):
                return JSONResponse({"detail": "Unauthorized"}, status_code=401)
            # Legacy MCP registrations remain bound to their original pilot space.
            identity = Identity('legacy-mcp', '', '', pilot, 'viewer')
        else:
            authorization=request.headers.get('authorization','')
            if authorization.startswith('Bearer '):
                try:
                    identity = await verify_weknora(authorization,request.headers.get('x-tenant-id'))
                except HTTPException as exc:
                    return JSONResponse({'detail':exc.detail},status_code=exc.status_code)
            else:
                sid = request.cookies.get("structured_session", request.cookies.get("cotton_session", ""))
                session = sessions.get(sid)
                if not session or session['expires'] < time.time():
                    return JSONResponse({"detail": "请先从本地 WeKnora 登录。"}, status_code=401)
                try:
                    identity = await verify_weknora(session['authorization'], session['identity'].tenant_id)
                except HTTPException as exc:
                    return JSONResponse({'detail': exc.detail}, status_code=exc.status_code)
                requested = request.headers.get('x-tenant-id')
                if requested and requested != str(identity.tenant_id):
                    return JSONResponse({'detail':'当前空间已改变，请重新打开工作台'}, status_code=403)
            if request.method not in {"GET", "HEAD"}:
                if not identity.can_write and not path.endswith('/preview'):
                    return JSONResponse({'detail':'当前空间角色只有读取权限'}, status_code=403)
                origin = request.headers.get("origin", "")
                if origin != expected_origin(request):
                    return JSONResponse({"detail": "Origin not allowed"}, status_code=403)
        marker = current_identity.set(identity)
        try:
            return await call_next(request)
        finally:
            current_identity.reset(marker)

    @app.get("/")
    def index():
        return FileResponse(Path(__file__).with_name("index.html"))

    @app.get("/health")
    def health():
        return {"status": "ok", "version": "0.3.0"}

    @app.get("/api/ui-config")
    def ui_config():
        from .ui_config import read_ui_config
        return read_ui_config(root)

    @app.post("/login")
    async def login(request: Request):
        # Credentials go only to the configured local WeKnora and are never stored.
        import httpx

        origin = request.headers.get("origin", "")
        if origin != expected_origin(request):
            raise HTTPException(403, "Origin not allowed")
        body = await request.json()
        base = os.environ.get("WEKNORA_API_URL", "http://127.0.0.1:18081")
        async with httpx.AsyncClient(trust_env=False, timeout=10, follow_redirects=False) as client:
            if not body.get("email") or not body.get("password"):
                raise HTTPException(401, "请输入 WeKnora 邮箱和密码。")
            authenticated = await client.post(base + "/api/v1/auth/login", json={"email": body["email"], "password": body["password"]})
            if authenticated.status_code != 200:
                raise HTTPException(401, "邮箱或密码错误，请使用本地 WeKnora 账号。")
            auth_doc = authenticated.json()
            user_token = auth_doc.get('data', auth_doc).get('token', '')
        authorization = 'Bearer ' + user_token
        identity = await verify_weknora(authorization, body.get('tenant_id'))
        now = time.time()
        for sid, session in list(sessions.items()):
            if session['expires'] < now:
                sessions.pop(sid, None)
        sid = secrets.token_urlsafe(32)
        sessions[sid] = {'expires': now + 8*3600, 'identity':identity, 'authorization':authorization}
        response = JSONResponse({"ok": True})
        response.set_cookie("structured_session", sid, httponly=True, samesite="strict", max_age=8*3600)
        return response

    @app.get('/api/model-settings')
    def model_settings():
        from .model_settings import public_model
        return public_model(store.root)

    @app.post('/api/model-settings')
    async def update_model_settings(request: Request):
        from .model_settings import save_model
        if current_identity.get().role not in {'owner','admin'}:
            raise HTTPException(403, '只有空间所有者或管理员可修改模型配置')
        return save_model(store.root,await request.json())

    @app.get("/api/datasets")
    def datasets():
        return {"datasets": store.list_datasets()}

    @app.get("/api/datasets/{dataset_id}/series")
    def series(dataset_id: str, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None):
        return store.list_series(store.select_dataset(dataset_id), search, limit, offset, sheet, context_sheet)

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
        return store.query_series(store.select_dataset(dataset_id), series_id, start_date, end_date, operation, limit, period)

    def run_import(job_id: str, path: Path):
        try:
            result = manager.ingest(path)
            with job_lock:
                jobs[job_id].update(status='completed', result=result)
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status='failed', error=str(exc))
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
        uploads = store.root / "uploads" / job_id
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
                jobs[job_id] = {"id": job_id, 'tenant_id':current_identity.get().tenant_id, "status": "processing", "stage": "扫描全部工作表并导入结构明确的表，不调用模型"}
            context = contextvars.copy_context()
            threading.Thread(target=context.run, args=(run_import, job_id, path), daemon=True).start()
        except Exception:
            path.unlink(missing_ok=True)
            uploads.rmdir()
            import_lock.release()
            raise
        return {"id": job_id, "status": "processing"}

    @app.get("/api/jobs/{job_id}")
    def job(job_id: str):
        with job_lock:
            if job_id not in jobs or jobs[job_id].get('tenant_id') != current_identity.get().tenant_id:
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
                jobs[job_id].update(status='completed', result=result)
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status='failed', error=str(exc))
        finally:
            import_lock.release()

    @app.post("/api/imports/{import_id}/confirm")
    def confirm(import_id: str, body: MappingRequest):
        manager.path(import_id)
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, "已有导入或确认任务，请等待完成。")
        job_id = uuid.uuid4().hex
        with job_lock:
            jobs[job_id] = {"id": job_id, 'tenant_id':current_identity.get().tenant_id, "status": "processing", "stage": "校验映射并保存新版本"}
        context = contextvars.copy_context()
        threading.Thread(target=context.run, args=(run_confirm, job_id, import_id, body), daemon=True).start()
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

"""Local management UI plus authenticated, read-only MCP tools."""
from __future__ import annotations

from contextlib import asynccontextmanager
import asyncio
from dataclasses import replace
import datetime as dt
from decimal import Decimal
import json
import logging
import os
from pathlib import Path
import secrets
import threading
import time
import uuid
import contextvars
import inspect
from typing import get_type_hints
from urllib.parse import quote, urlsplit

from fastapi import FastAPI, HTTPException, Query, Request, UploadFile
from fastapi.encoders import decimal_encoder
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings
from pydantic import BaseModel, Field, ConfigDict, create_model, ValidationError
from starlette.concurrency import run_in_threadpool

from .store import DataStore, dump
from .imports import ImportManager
from .workspaces import WorkspaceRegistry, Identity, current_identity, valid_knowledge_base_id
from .review_worker import run_review_task, MAX_REQUEST_BYTES
from .review_errors import error_location
from .pdf_api import pdf_store, install_pdf_routes, verify_pdf_original
from .progress import progress_value

MAX_UPLOAD = 100 * 1024 * 1024
logger = logging.getLogger(__name__)


class _ResearchFactsJSONResponse(JSONResponse):
    """Encode the already-validated facts document once in its worker thread."""
    @staticmethod
    def _default(value):
        if isinstance(value,dt.date):
            return value.isoformat()
        if isinstance(value,Decimal):
            # Match FastAPI's existing integral/fractional Decimal behavior.
            return decimal_encoder(value)
        raise TypeError(f'Object of type {type(value).__name__} is not JSON serializable')

    def render(self,content):
        return json.dumps(content,ensure_ascii=False,allow_nan=False,indent=None,
                          separators=(',',':'),default=self._default).encode('utf-8')


class MappingRequest(BaseModel):
    mapping: dict
    save_template: bool = True


class SuggestRequest(BaseModel):
    sheet: str


class BatchConfirmRequest(BaseModel):
    model_config = ConfigDict(extra='forbid')
    mappings: list[dict] = Field(default_factory=list, max_length=256)
    save_template: bool = Field(default=True, strict=True)


class AssistConfirmRequest(BaseModel):
    model_config = ConfigDict(extra='forbid')
    sheet: str = Field(min_length=1, max_length=254)
    mapping: dict | None = None
    save_template: bool = Field(default=True, strict=True)
    max_requests: int = Field(default=12, ge=1, le=32, strict=True)
    request_id: str | None = Field(default=None, pattern=r'^[0-9a-f]{32}$')


class BatchAssistConfirmRequest(BaseModel):
    model_config = ConfigDict(extra='forbid')
    acknowledged_risk: bool = Field(strict=True)
    request_id: str | None = Field(default=None, pattern=r'^[0-9a-f]{32}$')
    total_max_requests: int = Field(default=256, ge=1, le=512, strict=True)
    per_sheet_max_requests: int = Field(default=12, ge=1, le=32, strict=True)
    save_template: bool = Field(default=True, strict=True)
    mappings: list[dict] = Field(default_factory=list, max_length=256)


class NativeImportRequest(BaseModel):
    model_config = ConfigDict(extra='forbid')
    source_knowledge_id: str = Field(min_length=1, max_length=128, pattern=r'^[A-Za-z0-9][A-Za-z0-9_-]*$')
    restore_reviewed_state: bool = Field(default=True, strict=True)


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
    # A browser can cancel before its create-job response arrives. Keep these
    # short-lived intents separate from real jobs, scoped to the verified user,
    # so another member cannot reserve/cancel someone else's future task ID.
    pending_ai_cancels: dict[tuple, float] = {}
    job_lock = threading.Lock()

    def update_job_progress(job_id, value):
        value = progress_value(value)
        with job_lock:
            jobs[job_id].update(stage=value['stage'],
                progress={key: item for key, item in value.items() if key != 'stage'})

    import_lock = registry.proxy('import_lock')
    sheet_view_lock = threading.BoundedSemaphore(1)
    hosts = ["127.0.0.1:*", "localhost:*", "structured-data:*", "cotton-data:*", "host.docker.internal:*"]
    origins = ["http://127.0.0.1:*", "http://localhost:*"]
    if public_authority:
        hosts.append(public_authority)
        origins.append(public_origin)
    mcp = FastMCP("结构化数据工作台", stateless_http=True, json_response=True,
                  transport_security=TransportSecuritySettings(allowed_hosts=hosts, allowed_origins=origins))

    @mcp.tool()
    async def resolve_fields(question: str, dataset_id: str | None = None, context_sheet: str | None = None, prior_series_ids: list[str] | None = None, date: str | None = None, sheet: str | None = None, pending_resolution_id: str | None = None, previous_resolution_id: str | None = None, source_question: str | None = None) -> dict:
        """用大模型知识理解用户别称和真实表头的关系。只发送字段目录，不发送整表数据。question可描述技术子任务；应用提供source_question时，其真实用户原话和带角色对话是意图与时间依据，question和date仅是父模型技术建议，不得新增用户没要求的统计或时间。web不提供source_question时question就是用户原话。保留相关条件；组合分析可先用list_series绑定真实字段，再交analyze_table，不要求此工具表达整套分析。previous_resolution_id只能来自上一轮真实工具结果，系统据此读取完整任务，理解续问、补充和改口。context_sheet及prior_series_ids仅用于候选召回。sheet仅在用户明确限制工作表时填写。返回真实候选列、理由和查询/计算方案。needs_clarification会在网页展示实际待补信息：等用户补充或选择，不能代替用户确认或取数；系统错误不属于业务歧义。resolved返回answer原值/计算和来源；检查是否满足本轮完整需求，必要时组合其他工具，最终解释实际覆盖，不要另行心算。dataset_id可省略，只有一份文件时自动选最新版本；多文件需指定。"""
        return await run_in_threadpool(resolver.tool_answer,question,dataset_id,context_sheet,prior_series_ids,date,sheet,pending_resolution_id,previous_resolution_id,source_question)

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
    def list_datasets(question: str | None = None) -> dict:
        """列出已完成的Excel数据集、文件版本及支持/排除区域。同名文件默认选is_latest_for_source=true；用户指定历史版本时按指定版本。不把旧数据当实时行情。"""
        # Historical versions need identity metadata, not repeated full coverage
        # inventories. Keep the management API's full report unchanged.
        directory_started = time.perf_counter()
        logger.info('structured list_datasets directory_start question_supplied=%s', bool(question))
        datasets=[]
        try:
            sources = store.list_datasets()
        except Exception as exc:
            logger.warning('structured list_datasets directory_failed elapsed_ms=%d error_type=%s',
                           round((time.perf_counter()-directory_started)*1000), type(exc).__name__)
            raise
        for source in sources:
            item={k:source[k] for k in ('dataset_id','source_file','sha256','imported_at',
                'is_latest_for_source','series_count','import_id','use_reviewed_mappings','parent_dataset_id')}
            if source['is_latest_for_source']:
                item['sheets']=[s['name'] for s in source['sheets']]
                item['pending_sheets']=source['pending_sheets']
                item['pending_sheet_states']=source.get('pending_sheet_states', [])
                item['empty_sheets']=source['empty_sheets']
                item['coverage_notice']=source.get('coverage_notice','仅查询已导入范围。')
            datasets.append(item)
        latest_count = sum(1 for item in datasets if item['is_latest_for_source'])
        logger.info('structured list_datasets directory_complete elapsed_ms=%d datasets=%d latest=%d',
                    round((time.perf_counter()-directory_started)*1000), len(datasets), latest_count)
        result={"datasets": datasets}
        pdf_catalog = pdf_store(registry).list_tables()['tables']
        if pdf_catalog:
            result['pdf_tables'] = pdf_catalog
            result['pdf_notice'] = 'PDF 表的 status=auto_accepted 表示自动校验入库，status=reviewed 表示人工确认；用 list_pdf_tables/read_pdf_table/query_pdf_table 核验和计算。保留原页出处，识别正确性与整份文档覆盖未自动保证。'
        if question:
            from .field_index import literal_fields
            matches=[]
            for position, dataset in enumerate([d for d in datasets if d['is_latest_for_source']][:8], 1):
                catalog_started = time.perf_counter()
                logger.info('structured list_datasets catalog_start position=%d dataset_id=%s',
                            position, dataset['dataset_id'])
                try:
                    catalog = store.catalog(dataset['dataset_id'])
                except Exception as exc:
                    logger.warning('structured list_datasets catalog_failed position=%d dataset_id=%s elapsed_ms=%d error_type=%s',
                                   position, dataset['dataset_id'],
                                   round((time.perf_counter()-catalog_started)*1000), type(exc).__name__)
                    raise
                logger.info('structured list_datasets catalog_complete position=%d dataset_id=%s elapsed_ms=%d fields=%d',
                            position, dataset['dataset_id'],
                            round((time.perf_counter()-catalog_started)*1000), len(catalog))
                literal_started = time.perf_counter()
                try:
                    fields = literal_fields(catalog,question)
                except Exception as exc:
                    logger.warning('structured list_datasets literal_recall_failed position=%d dataset_id=%s elapsed_ms=%d error_type=%s',
                                   position, dataset['dataset_id'],
                                   round((time.perf_counter()-literal_started)*1000), type(exc).__name__)
                    raise
                logger.info('structured list_datasets literal_recall position=%d dataset_id=%s elapsed_ms=%d matches=%d',
                            position, dataset['dataset_id'],
                            round((time.perf_counter()-literal_started)*1000), len(fields))
                for field in fields:
                    matches.append({'dataset_id':dataset['dataset_id'],'source_file':dataset['source_file'],**{k:field.get(k) for k in ('series_id','sheet','metric','unit','dimensions','header_cells','source_context')}})
            result['literal_fields']=matches[:64]
            result['literal_field_count']=len(result['literal_fields'])
            result['literal_fields_truncated']=len(matches)>64 or sum(d['is_latest_for_source'] for d in datasets)>8
            result['literal_field_notice']='这些是实际原表表头的字面召回证据，不执行取数或决定语义。模型依据完整问题、上下文和真实来源生成绑定计划；程序核验同名来源的全部实际ID及选择依据，不选择第一列。'
        return result

    @mcp.tool()
    def list_charts(dataset_id: str, sheet: str | None = None, search: str = '', limit: int = 50, offset: int = 0) -> dict:
        """列出Excel原生图表、标题、图例、源数据引用及关联状态。图片不解析。linked曲线的series_id可交给query_series查询，日期使用源表日期；pending_source先确认源表映射，needs_review不可猜测年份。部分关联只覆盖linked曲线。问图表优先resolve_fields；不能把未解析曲线说成没有原数据。"""
        return store.list_charts(dataset_id, sheet, search, limit, offset)

    @mcp.tool()
    def inspect_source(dataset_id: str, sheet: str | None = None, searches: list[str] | None = None,
                       ranges: list[str] | None = None, sums: list[list[str]] | None = None,
                       max_matches: int = 10, max_cells: int = 200,
                       padding_rows: int = 2, padding_cols: int = 2) -> dict:
        """只读核验原 Excel 的单元格、公式及相交原生图表的标题、轴标题和系列引用，追查字段目录未覆盖的来源证据；包括 parsed_range 外区域。dataset_id 必须来自真实目录，文件 SHA256 必须与该版本一致。searches 是原值/公式的字面匹配（最多8词），可跨全部列；未指定 sheet 时按原工作簿顺序扫描，预算可能耗在其他表，目标表根本未扫描。已定位字段后必须指定该字段的真实 sheet 定向检索。跨多表搜索截断时看 scan.unscanned_sheets/incomplete_sheets、truncated 和 next_step；matches=[] 不等于没有数据，继续目标表检索。ranges 支持 A1、$绝对坐标、大小写及同工作表前缀；给出 sheet，或每个引用都带同一真实工作表前缀，最多8段、总格数不超过 max_cells(1..400)，返回每格原值、坐标及公式缓存。sums 最多8组、每组1..16个A1引用（单格或有限矩形范围，如 C74:C85、D74:D85），展开后每组至少2格；组内重叠坐标拒绝，组间重复按实际输出格数计入 max_cells。同样允许同表前缀与 $，不接受公式、整列、跨表或外部文件，由 Decimal 求和并返回全部展开原单元格；缺失/错误/无缓存不补零。max_matches 1..30；邻域 padding_rows 0..3、padding_cols 0..4。只检视原件，不重解析、不重新计算公式、不猜单位或年度；年度小表可能与月度汇总/不同定义冲突，必须核对并展示，不能自行选优先表。提供 ranges 时另返回最多20条与这些单元格相交的原生图表文本引用（标题、轴标题、曲线标签与原始范围）；chart_references_truncated 标明超限遗漏。图表返回的是原始文本及真实引用，不自动确认字段单位；模型须结合系列范围、轴对应、表头、公式和完整上下文判断适用性，不把其他系列或另一轴的单位套用。目录单位缺省不代表原件没有单位证据。"""
        from .source_inspection import (
            SourceEvidenceReadBudgetExceeded,
            SourceInspectionReadError,
            SourceVerificationError,
            inspect_source as run_inspection,
        )
        try:
            return run_inspection(store, dataset_id, sheet, searches, ranges, sums,
                                  max_matches, max_cells, padding_rows, padding_cols)
        except SourceEvidenceReadBudgetExceeded as exc:
            return {'kind': 'structured_source_inspection', 'status': 'error',
                    'error': 'source_read_budget_exceeded', 'phase': exc.phase,
                    'scope': exc.scope, 'retryable': True,
                    'next_step': '原件处理预算已用尽，未返回部分结果；减少并行读取、缩小范围或改为定向读取后重试。此状态不表示原表没有证据。'}
        except SourceInspectionReadError as exc:
            logger.exception('Structured source inspection failed during %s (cause type %s)',
                             exc.phase, exc.exception_type)
            return {'kind': 'structured_source_inspection', 'status': 'error',
                    'error': 'source_read_failed', 'phase': exc.phase,
                    'exception_type': exc.exception_type, 'retryable': False,
                    'next_step': '原件读取未能完成，未返回来源内容；可核对版本后重试。这不表示原表没有证据。'}
        except SourceVerificationError as exc:
            return {'kind': 'structured_source_inspection', 'status': 'error',
                    'error': str(exc), 'phase': exc.phase,
                    'exception_type': exc.exception_type, 'retryable': False,
                    'next_step': '原件完整性无法核验；重新导入可信原件并获取真实版本目录，不使用本次数据作答。'}
        except ValueError as exc:
            error = str(exc)
            damaged_original = any(marker in error for marker in ('SHA256', '原 Excel 发生变化', '没有保留原 Excel'))
            return {'kind': 'structured_source_inspection', 'status': 'error', 'error': error,
                    'retryable': not damaged_original,
                    'next_step': '原件完整性无法核验；重新导入可信原件并获取真实版本目录，不使用本次数据作答'
                    if damaged_original else '缩小读取范围或修正参数；数据集编号必须重新从当前空间的版本目录取得。这是系统反馈，不是原表没有数据。'}
        except Exception as exc:
            logger.exception('Unexpected structured source inspection failure')
            return {'kind': 'structured_source_inspection', 'status': 'error',
                    'error': 'source_read_failed', 'phase': 'inspection',
                    'exception_type': type(exc).__name__, 'retryable': False,
                    'next_step': '原件读取未能完成，未返回来源内容；可核对版本后重试。这不表示原表没有证据。'}

    @mcp.tool()
    def list_series(dataset_id: str | None = None, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None) -> dict:
        """读取字段目录中的真实ID、原表头、维度、单位和日期覆盖，不读取业务观测值。dataset_id可省略，仅有一个当前文件时自动选择；多文件必须先取得真实ID，不猜ID。模型依据完整问题、上下文和实际目录决定是否需要读取记录或计算；列举字段结构不要求额外查询时序。search空格分词AND，仅作字面召回；别名与partial_candidates也仅为候选，不决定业务口径。sheet严格过滤；context_sheet仅优先排序。catalog_summary按分页前实际匹配目录的sheet/metric精确分组计数，可作为实际字段结构列举的来源，不判断业务类别；groups_truncated表示分组展示也有截断。truncated与next_offset说明字段分页，不能将当前页当作全目录。pending_scope只说明对应表未导入，不说明相关主题在其他已导入列中不存在。"""
        return store.list_series(store.select_dataset(dataset_id), search, limit, offset, sheet, context_sheet)

    @mcp.tool()
    def query_series(series_id: str, dataset_id: str | None = None, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None, source_question: str | None = None, source_binding: list[dict] | None = None) -> dict:
        """source_binding可选：涉及同名来源时，由你依据完整问题和真实同名来源组给出[{considered_ids:本组全部真实ID,selected_ids:所需ID,reason:依据}]，程序校验后直接执行，不再另行调用来源模型；不提供时由独立来源核验。确定性查询：dataset_id可省略，只有一个当前文件时自动选择；具体日期用YYYY-MM-DD，起止均包含。date_mode=period时用period精确匹配原标签，不可转为日期；date_mode=none不填时间。不带日期的系列不支持latest；期间统计须指定period。operation=rows/latest/mean/sum/min/max/count。min/max返回aggregate及真正取得极值的rows/extremum_rows，日期只能用其中的date，actual_first_date/actual_last_date仅是范围边界。并列极值的数量看extremum_count，超过limit会truncated。求最高/最低日期不需要用户先给日期；未限定时间则不填日期查询全范围。latest返回最新有效数值；mean为算术平均；count为有效值数。缺失不作零。mean/sum/count的rows仅示例来源，aggregate才是统计值。返回原文件、sheet/cell和公式缓存来源。"""
        selected=store.select_dataset(dataset_id)
        from .source_context import verify_selection
        ambiguity=verify_selection(store,selected,[series_id],source_question,model_fn=resolver.model_fn,source_binding=source_binding)
        return ambiguity or store.query_series(selected, series_id, start_date, end_date, operation, limit, period)

    @mcp.tool()
    def convert_price(dataset_id: str, price_series_id: str, fx_series_id: str, date: str, target_unit: str = '元/吨', source_question: str | None = None, fx_operation: str = 'multiply', source_binding: list[dict] | None = None) -> dict:
        """source_binding可选：涉及同名来源时，由你依据完整问题和真实同名来源组给出[{considered_ids:本组全部真实ID,selected_ids:所需ID,reason:依据}]，程序校验后直接执行，不再另行调用来源模型；不提供时由独立来源核验。由模型依据完整问题及显式币种方向元数据选择两项真实来源。程序对同日原始价格和汇率确定性计算，不猜标题或默认来源优先级。fx_operation=multiply|divide必须与来源base_currency/quote_currency及evidence_cells一致。先list_series取得真实ID。输入价格单位必须为美元或美分/磅、公斤、千克、吨；目标支持元/吨(默认)、元/磅、元/公斤、元/千克。date格式YYYY-MM-DD。返回数值、公式及价格/汇率双方原单元格。不同币种方向、未知单位、缺失数据拒绝；不包含税费与通关成本。"""
        from .source_context import verify_selection
        ambiguity=verify_selection(store,dataset_id,[price_series_id,fx_series_id],source_question,model_fn=resolver.model_fn,source_binding=source_binding)
        return ambiguity or store.convert_price(dataset_id,price_series_id,fx_series_id,date,target_unit,fx_operation)

    @mcp.tool()
    def compare_series(dataset_id: str, left_series_id: str, right_series_id: str, date: str, source_question: str | None = None, comparison_basis: str = 'same_explicit_unit', source_binding: list[dict] | None = None) -> dict:
        """source_binding可选：涉及同名来源时，由你依据完整问题和真实同名来源组给出[{considered_ids:本组全部真实ID,selected_ids:所需ID,reason:依据}]，程序校验后直接执行，不再另行调用来源模型；不提供时由独立来源核验。同日两个来源作差，严格按左减右。comparison_basis=same_explicit_unit默认需要两侧相同显式单位；raw_values是模型明确提交的原始数值比较计划，保留未知单位和来源，不授权单位换算或推定业务等价。先从list_series取得准确ID，回答附双方原单元格。缺失不补造，其他单位未知/不一致拒绝。"""
        from .source_context import verify_selection
        ambiguity=verify_selection(store,dataset_id,[left_series_id,right_series_id],source_question,model_fn=resolver.model_fn,source_binding=source_binding)
        return ambiguity or store.compare_series(dataset_id, left_series_id, right_series_id, date, comparison_basis)

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
    async def analyze_table(series_ids: list[str], sql: str, dataset_id: str | None = None, start_date: str | None = None, end_date: str | None = None, limit: int = 200, source_question: str | None = None, source_binding: list[dict] | None = None) -> dict:
        """source_binding可选：涉及同名来源时，由你依据完整问题和真实同名来源组给出[{considered_ids:本组全部真实ID,selected_ids:所需ID,reason:依据}]，程序校验后直接执行，不再另行调用来源模型；不提供时由独立来源核验。只读通用分析SQL；先取得真实series_id并核验口径。dataset_id可省略，只有一个当前文件时自动选择；多文件先取得真实ID，不猜ID。隔离表data仅含指定字段/日期范围的全部记录（最多50000条），列series_id VARCHAR,date DATE,value DOUBLE,source_cell,date_cell,period,value_status,metric,sheet,unit均VARCHAR；缺失value为NULL。SQL方言为DuckDB，支持SELECT/WITH、四则、GROUP BY、JOIN、窗口/滚动与多阶段分析，无需预定义问题类型。优先使用原生聚合、窗口和日期函数，例如date_trunc(part,date)；不要自行拼日历算法，CAST小数到整数会舍入而非向下取整，向下取整用floor。输出columns和rows（按列顺序的二维数组）、SQL及输入覆盖/来源；输出截断不影响完整输入计算。所有聚合必须使用完整输入，按需WHERE value IS NOT NULL，SQL不自动排除零、换算单位或解释年度。输入过大/SQL错误时根据反馈修正，不让用户选字段。每个输入最多一个文件版本，不允许读取文件、联网、修改数据或多条语句。不要混加存量、累计值与当期流量。"""
        from .table_analysis import analyze_table as run_analysis
        try:
            selected=store.select_dataset(dataset_id)
            from .source_context import verify_selection
            ambiguity=await run_in_threadpool(verify_selection,store,selected,series_ids,source_question,model_fn=resolver.model_fn,source_binding=source_binding)
            if ambiguity:return ambiguity
            return await run_in_threadpool(run_analysis,store,selected,series_ids,sql,start_date,end_date,limit)
        except ValueError as exc:
            return {'kind':'structured_analysis','status':'error','error':str(exc),'retryable':True,'next_step':'修正SQL或输入范围；这是系统反馈，不是字段确认请求'}

    @mcp.tool()
    def list_pdf_tables(source_sha256: str | None = None) -> dict:
        """列出当前授权空间已入库的 PDF 逻辑表、真实 table_id、列类型/单位及覆盖。草稿不参与查询。模型根据完整问题与实际标题/列/来源选择表，不按页相邻或列数推定同表；多个表可能重叠，不能重复累计。"""
        return {**pdf_store(registry).list_tables(source_sha256), 'source_kind': 'pdf_table', 'kind': 'structured_pdf_tables', 'status': 'ok'}

    @mcp.tool()
    def read_pdf_table(table_id: str, offset: int = 0, limit: int = 100) -> dict:
        """按实际 table_id 分页核对 PDF 逻辑表，返回原值、列映射、每格页码/坐标和跨页来源。output_truncated 表示还需翻页，空值不补零。review_status=auto_accepted 表示自动校验入库，reviewed 表示映射经人工确认；两者都不保证 OCR 正确或整篇覆盖。"""
        result = pdf_store(registry).read_table(table_id, offset=offset, limit=limit)
        verify_pdf_original(registry, result['source'])
        return {**result, 'kind': 'structured_source_inspection', 'source_kind': 'pdf_table', 'status': 'ok', 'review_status': result['status']}

    @mcp.tool()
    async def query_pdf_table(table_id: str, sql: str, limit: int = 100) -> dict:
        """对已入库 PDF 表执行单条 DuckDB SELECT/WITH。先 list_pdf_tables 获取真实 ID 和列名；data 含 _row_id 和真实列，decimal 为 DECIMAL(38,18)，text 为 VARCHAR。模型根据完整问题/来源/单位构造 SQL，程序只读计算全部已选逻辑行，输出 limit 不截断输入。禁止联网/文件/其他表/写操作。缺失为 NULL，不补零或自动换算单位。结果小数以字符串无损返回，带原文件/计划指纹与输入覆盖；聚合不伪造逐格 SQL 血缘，需 read_pdf_table 核对输入页码。input_complete=false 时不得声称全表统计。公式保留不等于可执行。"""
        def run():
            selected = pdf_store(registry)
            verify_pdf_original(registry, selected.read_table(table_id, limit=1)['source'])
            return {**selected.query(table_id, sql, limit=limit), 'source_kind': 'pdf_table'}
        try:
            return await run_in_threadpool(run)
        except ValueError as exc:
            return {'kind': 'structured_analysis', 'status': 'error', 'error': str(exc),
                    'next_step': '核对真实表编号、原件和 SQL；失败不是原表没有数据。'}

    read_operations = {name: fn for name, fn in locals().copy().items() if name in {
        'list_datasets','list_series','list_charts','query_series','compare_series','convert_price',
        'resolve_fields','confirm_fields','execute_field_plan','get_result_evidence','analyze_table','inspect_source',
        'list_pdf_tables','read_pdf_table','query_pdf_table'}}
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
    font_assets = Path(__file__).resolve().parents[3] / "WeKnora/frontend/public/fonts"
    if font_assets.is_dir():
        app.mount("/fonts", StaticFiles(directory=font_assets), name="fonts")
    app.state.store = store
    app.state.import_manager = manager
    app.state.mcp = mcp
    app.state.workspaces = registry
    install_pdf_routes(app, registry, publish_guard=lambda authorization, identity: check_job_write(authorization, identity))

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

    @app.post('/api/research/facts')
    async def research_facts(request: Request):
        from .research_facts import read_facts
        body = await request.json()
        manifest = getattr(request.state, 'research_source_manifest', None)
        if manifest is not None:
            from .research_facts import _parameters
            sources, _, _, _ = _parameters(body)
            permitted = {(row.get('id'), str(row.get('file_hash', '')).lower()) for row in manifest
                         if isinstance(row, dict) and row.get('parse_status') not in {'deleting', 'deleted'}}
            if any((row['knowledge_id'], row['file_hash']) not in permitted for row in sources):
                raise HTTPException(403, '来源不在当前工作台已批准的研究数据范围内')
        def read_current():
            with manager.lock:
                document=read_facts(store, manager, body)
            return _ResearchFactsJSONResponse(document)
        return await run_in_threadpool(read_current)

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

    async def verify_knowledge_base(identity, authorization, knowledge_base_id):
        if not valid_knowledge_base_id(knowledge_base_id):
            raise HTTPException(400, '知识库编号无效')
        import httpx
        base = os.environ.get('WEKNORA_API_URL', 'http://127.0.0.1:18081')
        try:
            async with httpx.AsyncClient(trust_env=False, timeout=10, follow_redirects=False) as client:
                response = await client.get(base + '/api/v1/knowledge-bases/' + quote(knowledge_base_id, safe=''),
                    headers={'Authorization': authorization, 'X-Tenant-ID': str(identity.tenant_id)})
        except httpx.HTTPError:
            raise HTTPException(503, '无法核验知识库权限，请稍后重试。') from None
        if response.status_code != 200:
            raise HTTPException(403, '没有当前知识库的访问权限')
        try:
            document = response.json()
            data = document.get('data', document)
            owner = data.get('tenant_id')
            permission = data.get('my_permission')
            if (document.get('success') is False or data.get('id') != knowledge_base_id
                    or type(owner) is not int or owner <= 0
                    or permission not in {'viewer', 'editor', 'admin'}):
                raise ValueError('invalid knowledge-base grant')
            writable = permission in {'editor', 'admin'}
            return replace(identity, knowledge_base_id=knowledge_base_id,
                           knowledge_base_tenant_id=owner, knowledge_base_can_write=writable)
        except (ValueError, TypeError, AttributeError):
            raise HTTPException(403, '知识库权限信息无效') from None

    @app.middleware("http")
    async def protect(request: Request, call_next):
        authority = request.headers.get("host", "")
        host = authority.split(":")[0]
        if host not in {"127.0.0.1", "localhost", "structured-data", "cotton-data", "host.docker.internal", "testserver"} and not (public_authority and authority == public_authority):
            return JSONResponse({"detail": "Host not allowed"}, status_code=403)
        path = request.url.path
        if path in {"/", "/health", "/login", "/api/ui-config", "/sheet-review.js", "/pdf-review.js", "/sheet-review.css", "/workspace.css"}:
            return await call_next(request)
        if path.startswith("/fonts/") and request.method in {"GET", "HEAD"}:
            return await call_next(request)
        identity = None
        signed = path in {'/api/tools/read', '/api/research/export', '/api/research/facts'} or path.startswith('/api/research/source/')
        if signed:
            from .service_auth import verify_service
            try:
                identity = await verify_service(request)
            except HTTPException as exc:
                return JSONResponse({'detail': exc.detail}, status_code=exc.status_code)
        elif path.startswith("/mcp"):
            if request.headers.get('x-knowledge-base-id'):
                return JSONResponse({'detail': '旧 MCP 连接未绑定知识库，请使用已核验的结构化查询入口'}, status_code=403)
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
                authorization = session['authorization']
            knowledge_base_id = request.headers.get('x-knowledge-base-id')
            if knowledge_base_id is not None:
                try:
                    identity = await verify_knowledge_base(identity, authorization, knowledge_base_id)
                except HTTPException as exc:
                    return JSONResponse({'detail': exc.detail}, status_code=exc.status_code)
            if request.method not in {"GET", "HEAD"}:
                if not identity.can_write and not path.endswith('/preview'):
                    return JSONResponse({'detail':'当前空间角色只有读取权限'}, status_code=403)
                origin = request.headers.get("origin", "")
                if origin != expected_origin(request):
                    return JSONResponse({"detail": "Origin not allowed"}, status_code=403)
            request.state.verified_authorization = authorization
        if request.method == 'POST' and path.endswith(('/assist-confirm', '/confirm-all', '/assist-confirm-all')):
            chunks, total = [], 0
            async for chunk in request.stream():
                total += len(chunk)
                if total > MAX_REQUEST_BYTES:
                    return JSONResponse({'detail': '确认配置过大，请分批处理工作表。'}, status_code=413)
                chunks.append(chunk)
            request._body = b''.join(chunks)
        marker = current_identity.set(identity)
        from .model_gateway import current_authorization
        authorization_marker = current_authorization.set(getattr(request.state, 'verified_authorization', ''))
        try:
            return await call_next(request)
        finally:
            current_authorization.reset(authorization_marker)
            current_identity.reset(marker)

    @app.get("/")
    def index():
        return FileResponse(Path(__file__).with_name("index.html"))

    @app.get("/pdf-review.js")
    def pdf_review_script():
        return FileResponse(Path(__file__).with_name("static") / "pdf-review.js", media_type="text/javascript", headers={"Cache-Control": "no-cache"})

    @app.get("/sheet-review.js")
    def sheet_review_script():
        return FileResponse(Path(__file__).with_name("sheet-review.js"), media_type="text/javascript", headers={"Cache-Control": "no-cache"})

    @app.get("/sheet-review.css")
    def sheet_review_styles():
        return FileResponse(Path(__file__).with_name("sheet-review.css"), media_type="text/css", headers={"Cache-Control": "no-cache"})

    @app.get("/workspace.css")
    def workspace_styles():
        return FileResponse(Path(__file__).with_name("workspace.css"), media_type="text/css", headers={"Cache-Control": "no-cache"})

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
    async def model_settings():
        from .model_settings import public_model
        identity = current_identity.get()
        if identity.knowledge_base_id:
            from .model_gateway import available_models
            cfg = public_model(store.root)
            return {'native_model_id': cfg.get('native_model_id', ''), 'protocol': 'platform',
                    'legacy_configured': cfg.get('has_api_key', False),
                    'can_edit': identity.can_write, 'models': await available_models()}
        return public_model(store.root)

    @app.post('/api/model-settings')
    async def update_model_settings(request: Request):
        from .model_settings import save_model
        identity = current_identity.get()
        if identity.knowledge_base_id:
            if not identity.can_write:
                raise HTTPException(403, '只有知识库可编辑成员可修改字段模型')
            from .model_gateway import validate_model
            body = await request.json()
            if not isinstance(body, dict) or set(body) != {'native_model_id'}:
                raise ValueError('请选择已配置的通用模型，不需要地址或密钥。')
            await validate_model(body['native_model_id'])
            await run_in_threadpool(check_job_write, request.state.verified_authorization, identity)
            return save_model(store.root, body)
        if identity.role not in {'owner','admin'}:
            raise HTTPException(403, '只有空间所有者或管理员可修改模型配置')
        body = await request.json()
        if isinstance(body, dict) and 'native_model_id' in body:
            raise ValueError('请从具体知识库选择平台字段模型。')
        return save_model(store.root,body)

    @app.get("/api/datasets")
    def datasets(include_samples: bool = False):
        datasets = store.list_datasets(include_samples=include_samples)
        samples = [d for d in datasets if d["dataset_kind"] == "sample"]
        return {"datasets": [d for d in datasets if d["dataset_kind"] == "business"],
                **({"samples": samples} if samples else {})}

    @app.get("/api/datasets/{dataset_id}/series")
    def series(dataset_id: str, search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None, context_sheet: str | None = None):
        return store.list_series(store.select_dataset(dataset_id), search, limit, offset, sheet, context_sheet)

    @app.get("/api/series")
    def all_series(search: str = "", limit: int = 100, offset: int = 0, sheet: str | None = None):
        from .global_catalog import list_series
        return list_series(store, search, limit, offset, sheet)

    @app.get("/api/charts")
    def all_charts(sheet: str | None = None, search: str = '', limit: int = 50, offset: int = 0):
        from .global_catalog import list_charts
        return list_charts(store, sheet, search, limit, offset)

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
        from itertools import islice
        from .streaming import read_json_array
        manifest = store.manifest(dataset_id)
        issues = list(islice(read_json_array(store._path(dataset_id) / "issues.json"), 200))
        total = manifest["issue_count"]
        return {"manifest": manifest, "issues": issues, "issues_total": total, "issues_truncated": total > 200}

    @app.get("/api/datasets/{dataset_id}/source")
    def source(dataset_id: str):
        path = store._path(dataset_id) / "source.xlsx"
        if not path.exists():
            raise HTTPException(404, "此数据集没有保存源文件副本。")
        return FileResponse(path, filename=Path(store.manifest(dataset_id)["source_file"]).name)

    @app.get("/api/query")
    def query(dataset_id: str, series_id: str, start_date: str | None = None, end_date: str | None = None, operation: str = "rows", limit: int = 50, period: str | None = None, offset: int = 0):
        return store.query_series(store.select_dataset(dataset_id), series_id, start_date, end_date, operation, limit, period, offset=offset)

    def run_import(job_id: str, path: Path, native_source=None, legacy=None,
                   use_reviewed_mappings=True, authorization='', identity=None):
        try:
            result = None
            if legacy:
                check_job_write(authorization, identity)
                from .legacy_import import recover
                if not legacy['import_lock'].acquire(blocking=False):
                    raise ValueError('原空间正在处理表格，请稍后重试恢复；不会重新扫描或覆盖。')
                try:
                    with job_lock:
                        jobs[job_id]['stage'] = '校验并恢复已核对记录和历史数据，不重新扫描'
                    result = recover(manager, legacy['manager'], native_source, path,
                        before_commit=lambda: check_job_write(authorization, identity))
                finally:
                    legacy['import_lock'].release()
            if result is None:
                result = run_review_task(registry.directory(), "import", path=path,
                    use_reviewed_mappings=use_reviewed_mappings,
                    publish_guard=lambda: check_job_write(authorization, identity),
                    native_source=native_source,
                    progress_callback=lambda value: update_job_progress(job_id, value))
            if native_source:
                result['source_knowledge_id'] = native_source['source_knowledge_id']
            with job_lock:
                jobs[job_id].update(status='completed', result=result)
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status='failed', error=str(exc), **error_location(exc))
        finally:
            path.unlink(missing_ok=True)
            path.parent.rmdir()
            import_lock.release()

    @app.post('/api/import-knowledge')
    async def import_knowledge(body: NativeImportRequest, request: Request):
        from .native_import import download_source, load_source
        identity = current_identity.get()
        authorization = request.state.verified_authorization
        native = await load_source(body.source_knowledge_id, authorization, identity, MAX_UPLOAD)
        existing = await run_in_threadpool(manager.find_native_source, native,
                                           use_reviewed_mappings=body.restore_reviewed_state)
        legacy = None
        # Only the verified native source's own tenant may recover its legacy
        # workspace. Shared KB access never grants access to that owner's root.
        if body.restore_reviewed_state and identity.knowledge_base_id and identity.tenant_id == identity.knowledge_base_tenant_id:
            from dataclasses import replace
            from .legacy_import import candidate
            marker = current_identity.set(replace(identity, knowledge_base_id='', knowledge_base_tenant_id=None))
            try:
                legacy_objects = registry.objects()
            finally:
                current_identity.reset(marker)
            current = manager.get(existing['import_id']) if existing else None
            old = await run_in_threadpool(candidate, legacy_objects['manager'], native, current)
            if old:
                legacy = legacy_objects
        if existing and not legacy:
            return {'status': 'completed', 'reused': True, 'source_knowledge_id': body.source_knowledge_id,
                    'result': {**existing, 'source_knowledge_id': body.source_knowledge_id}}
        with job_lock:
            for job in jobs.values():
                if (job.get('tenant_id') == identity.tenant_id
                        and job.get('knowledge_base_id') == identity.knowledge_base_id
                        and job.get('restore_reviewed_state', True) == body.restore_reviewed_state
                        and job.get('native_source') == native and job['status'] == 'processing'):
                    return {'id': job['id'], 'status': 'processing', 'reused': True,
                            'source_knowledge_id': body.source_knowledge_id}
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, '已有导入任务，请等待完成。')
        job_id = uuid.uuid4().hex
        uploads = store.root / 'uploads' / job_id
        path = uploads / native['file_name']
        try:
            uploads.mkdir(parents=True)
            with job_lock:
                jobs[job_id] = {'id': job_id, 'tenant_id': identity.tenant_id,
                    'knowledge_base_id': identity.knowledge_base_id, 'status': 'processing',
                    'source_knowledge_id': body.source_knowledge_id, 'native_source': native,
                    'restore_reviewed_state': body.restore_reviewed_state,
                    'stage': '读取并校验当前知识库原件'}
            await download_source(native, path, authorization, identity, MAX_UPLOAD)
            with job_lock:
                jobs[job_id]['stage'] = '扫描本知识库原件并导入结构明确的表，不调用模型'
            context = contextvars.copy_context()
            threading.Thread(target=context.run, args=(run_import, job_id, path, native, legacy,
                body.restore_reviewed_state, authorization, identity), daemon=True).start()
        except Exception as exc:
            with job_lock:
                if job_id in jobs:
                    jobs[job_id].update(status='failed', error=getattr(exc, 'detail', '原件读取失败，请稍后重试。'))
            path.unlink(missing_ok=True)
            if uploads.is_dir():
                uploads.rmdir()
            import_lock.release()
            raise
        return {'id': job_id, 'status': 'processing', 'source_knowledge_id': body.source_knowledge_id}

    @app.post("/api/import")
    async def upload(request: Request, file: UploadFile):
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
            identity = current_identity.get()
            authorization = request.state.verified_authorization
            native = None
            if identity.knowledge_base_id:
                from .native_import import register_source
                native = await register_source(path, authorization, identity, MAX_UPLOAD)
            with job_lock:
                jobs[job_id] = {"id": job_id, 'tenant_id':identity.tenant_id,
                    'knowledge_base_id':identity.knowledge_base_id, 'user_id': identity.user_id,
                    **({'source_knowledge_id': native['source_knowledge_id'], 'native_source': native}
                       if native else {}),
                    "status": "processing", "stage": "扫描全部工作表并导入结构明确的表，不调用模型"}
            context = contextvars.copy_context()
            threading.Thread(target=context.run, args=(run_import, job_id, path, native, None,
                not bool(native), authorization, identity), daemon=True).start()
        except Exception:
            path.unlink(missing_ok=True)
            uploads.rmdir()
            import_lock.release()
            raise
        return {"id": job_id, "status": "processing",
                **({'source_knowledge_id': native['source_knowledge_id']} if native else {})}

    @app.post('/api/imports/{import_id}/register-original')
    async def register_original(import_id: str, request: Request):
        from .native_import import load_source, original_fingerprint, register_source
        identity = current_identity.get()
        if not identity.knowledge_base_id:
            raise HTTPException(400, '请在原件所属知识库中登记文件。')
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, '已有导入任务，请等待完成后登记原件。')
        try:
            authorization = request.state.verified_authorization
            doc = await run_in_threadpool(manager.get, import_id)
            path = manager.source_path(doc)
            # Only the current scoped manager's saved original is eligible;
            # request bodies cannot supply a local path, a destination KB or bytes.
            if (doc.get('import_id') != import_id or path.resolve().parent != manager.path(import_id).resolve()
                    or not isinstance(doc.get('sha256'), str)):
                raise HTTPException(409, '原件登记信息无效。')
            size, digest = await run_in_threadpool(original_fingerprint, path, MAX_UPLOAD, doc['sha256'])
            source = None
            for binding in doc.get('native_source_bindings', []):
                if (binding.get('knowledge_base_id') != identity.knowledge_base_id
                        or binding.get('tenant_id') != identity.knowledge_base_tenant_id):
                    continue
                if not valid_knowledge_base_id(binding.get('source_knowledge_id')):
                    raise HTTPException(409, '原件标识无效，请核对已保存的来源关联。')
                try:
                    candidate = await load_source(binding.get('source_knowledge_id'), authorization, identity, MAX_UPLOAD)
                except HTTPException as exc:
                    if exc.status_code == 404:
                        continue
                    raise
                if candidate['file_size'] != size or candidate['file_hash'] != digest:
                    raise HTTPException(409, '知识库原件已改变，未重复登记旧结构化数据。')
                source = candidate
                break
            if source is None:
                source = await register_source(path, authorization, identity, MAX_UPLOAD,
                                               expected_sha256=doc['sha256'])
            await run_in_threadpool(manager.bind_native_source, import_id, source,
                before_commit=lambda: check_job_write(authorization, identity))
            identifier = source['source_knowledge_id']
            library = quote(identity.knowledge_base_id, safe='')
            return {'source_knowledge_id': identifier, 'import_id': import_id,
                    'preview_path': f'/platform/knowledge-bases/{library}?knowledge_id={quote(identifier, safe="")}',
                    'structured_path': f'/platform/knowledge-bases/{library}/structured-data?mode=import&source_knowledge_id={quote(identifier, safe="")}'}
        finally:
            import_lock.release()

    @app.get("/api/jobs/{job_id}")
    def job(job_id: str):
        with job_lock:
            if (job_id not in jobs or jobs[job_id].get('tenant_id') != current_identity.get().tenant_id
                    or jobs[job_id].get('knowledge_base_id', '') != current_identity.get().knowledge_base_id
                    or (jobs[job_id].get('user_id') is not None
                        and jobs[job_id]['user_id'] != current_identity.get().user_id)):
                raise HTTPException(404, "任务不存在；服务重启后请查看数据集列表确认导入结果。")
            return jobs[job_id]

    @app.get("/api/imports")
    def imports(include_samples: bool = False):
        records = manager.list(include_samples=include_samples)
        samples = [d for d in records if d["dataset_kind"] == "sample"]
        return {"imports": [d for d in records if d["dataset_kind"] == "business"],
                **({"samples": samples} if samples else {})}

    @app.get("/api/imports/{import_id}")
    def import_review(import_id: str, summary_only: bool = False):
        doc = manager.publication_view(manager.get(import_id))
        result = {k: v for k, v in doc.items() if k != "source_path"}
        if summary_only:
            fields = {"name", "rows", "columns", "status", "layout", "fingerprint", "reasons", "template_status",
                      "import_state", "review_state", "automatic_skip",
                      "published_in_dataset", "published_dataset_id", "published_fingerprint"}
            briefs = []
            for sheet in doc["sheets"]:
                item = {k: v for k, v in sheet.items() if k in fields}
                item["block_count"] = len((sheet.get("mapping") or {}).get("blocks") or [])
                charts = sheet.get("charts") or {}
                item["chart_count"] = charts.get("chart_count") or 0
                item["image_count"] = charts.get("image_count") or 0
                briefs.append(item)
            result["sheets"] = briefs
        return result

    @app.get("/api/imports/{import_id}/sheet")
    def sheet_review(import_id: str, name: str):
        sheet = manager.sheet(import_id, name)
        doc = manager.publication_view(manager.get(import_id), verified_legacy_sheet=sheet)
        publication = next(s for s in doc['sheets'] if s['name'] == name)
        return {**sheet, **{k: v for k, v in publication.items() if k.startswith('published_')}}

    @app.get("/api/imports/{import_id}/sheet-view")
    async def sheet_view(
        import_id: str, name: str,
        start_row: int = Query(1, ge=1, le=1048576),
        start_col: int = Query(1, ge=1, le=16384),
        row_count: int = Query(60, ge=1, le=160),
        col_count: int = Query(24, ge=1, le=64),
    ):
        from .sheet_view import read_sheet_view
        manager.sheet(import_id, name)
        if not sheet_view_lock.acquire(blocking=False):
            raise HTTPException(409, "正在读取原表，请稍后再查看其他区域。")
        try:
            doc = manager.get(import_id)
            return await run_in_threadpool(
                read_sheet_view, manager.source_path(doc), name,
                start_row, start_col, row_count, col_count,
            )
        finally:
            sheet_view_lock.release()

    @app.post("/api/imports/{import_id}/sample-preview")
    async def sample_preview(import_id: str, body: MappingRequest):
        from .review_samples import preview_mapping_samples
        sheet = manager.sheet(import_id, body.mapping.get("sheet"))
        if not sheet_view_lock.acquire(blocking=False):
            raise HTTPException(409, "正在读取原表，请稍后再查看样本。")
        try:
            doc = manager.get(import_id)
            return await run_in_threadpool(preview_mapping_samples, manager.source_path(doc), sheet, body.mapping)
        finally:
            sheet_view_lock.release()

    @app.post('/api/imports/{import_id}/rescan')
    async def rescan(import_id: str, request: Request):
        manager.path(import_id)
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409,'已有导入或确认任务，请等待完成。')
        try:
            doc=manager.get(import_id)
            identity = current_identity.get()
            authorization = request.state.verified_authorization
            return await run_in_threadpool(run_review_task, registry.directory(), "import", path=manager.source_path(doc),
                use_reviewed_mappings=doc.get('use_reviewed_mappings', True),
                publish_guard=lambda: check_job_write(authorization, identity))
        finally:
            import_lock.release()

    @app.post("/api/imports/{import_id}/preview")
    async def preview(import_id: str, body: MappingRequest, background: bool = False):
        manager.sheet(import_id, body.mapping.get("sheet"))
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, "已有导入或完整检查任务，请等待完成。")
        if background:
            job_id = uuid.uuid4().hex
            with job_lock:
                jobs[job_id] = {"id": job_id, "tenant_id": current_identity.get().tenant_id, "knowledge_base_id": current_identity.get().knowledge_base_id, "status": "processing", "stage": "检查选定区块的全部数据"}
            context = contextvars.copy_context()
            threading.Thread(target=context.run, args=(run_preview, job_id, import_id, body), daemon=True).start()
            return {"id": job_id, "status": "processing"}
        try:
            return await run_in_threadpool(run_review_task, registry.directory(), "preview", import_id=import_id, mapping=body.mapping)
        finally:
            import_lock.release()

    def run_preview(job_id, import_id, body):
        try:
            result = run_review_task(registry.directory(), "preview", import_id=import_id, mapping=body.mapping,
                progress_callback=lambda value: update_job_progress(job_id, value))
            with job_lock:
                jobs[job_id].update(status="completed", result=result)
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status="failed", error=str(exc), **error_location(exc))
        finally:
            import_lock.release()

    def run_confirm(job_id, import_id, body, authorization, identity):
        try:
            result = run_review_task(registry.directory(), "confirm", import_id=import_id,
                mapping=body.mapping, save_template=body.save_template,
                publish_guard=lambda: check_job_write(authorization, identity),
                progress_callback=lambda value: update_job_progress(job_id, value))
            with job_lock:
                jobs[job_id].update(status='completed', result=result)
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status='failed', error=str(exc), **error_location(exc))
        finally:
            import_lock.release()

    @app.post("/api/imports/{import_id}/confirm")
    def confirm(import_id: str, body: MappingRequest, request: Request):
        manager.path(import_id)
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, "已有导入或确认任务，请等待完成。")
        job_id = uuid.uuid4().hex
        identity = current_identity.get()
        with job_lock:
            jobs[job_id] = {"id": job_id, 'tenant_id':identity.tenant_id,
                'knowledge_base_id':identity.knowledge_base_id, 'user_id': identity.user_id,
                "status": "processing", "stage": "校验当前工作表并增量保存，复用其他已导入工作表"}
        context = contextvars.copy_context()
        threading.Thread(target=context.run, args=(run_confirm, job_id, import_id, body,
            request.state.verified_authorization, identity), daemon=True).start()
        return {"id": job_id, "status": "processing"}

    @app.post("/api/imports/{import_id}/suggest")
    async def suggest(import_id: str, body: SuggestRequest):
        return await run_in_threadpool(manager.suggest, import_id, body.sheet)

    def check_job_write(authorization, expected):
        async def refresh():
            identity = await verify_weknora(authorization, expected.tenant_id)
            if identity.user_id != expected.user_id:
                raise ValueError('账号已改变，未自动导入。')
            if expected.knowledge_base_id:
                identity = await verify_knowledge_base(identity, authorization, expected.knowledge_base_id)
            if (not identity.can_write
                    or identity.knowledge_base_tenant_id != expected.knowledge_base_tenant_id):
                raise ValueError('编辑权限或知识库归属已改变，未自动导入。')
        try:
            asyncio.run(refresh())
        except HTTPException as exc:
            raise ValueError(str(exc.detail)) from None

    def run_batch_confirm(job_id, import_id, body, authorization, identity):
        try:
            from .imports import READY
            doc = manager.get(import_id)
            selected = {sheet['name']: sheet['mapping'] for sheet in doc['sheets']
                        if sheet['status'] in READY and sheet.get('mapping')
                        and sheet.get('import_state') != 'imported'}
            seen = set()
            for mapping in body.mappings:
                name = mapping.get('sheet')
                if not isinstance(name, str) or name in seen:
                    raise ValueError('批量确认工作表无效或重复。')
                manager.sheet(import_id, name)
                seen.add(name)
                selected[name] = mapping
            imported, failed = [], []
            check_job_write(authorization, identity)
            total = len(selected)
            for index, (name, mapping) in enumerate(selected.items()):
                # A long batch must not continue publishing other sheets after
                # the caller loses editing rights during an earlier sheet.
                check_job_write(authorization, identity)
                update_job_progress(job_id, {'phase': 'batch', 'completed': index, 'total': total})

                def sheet_progress(value):
                    value = progress_value(value)
                    with job_lock:
                        jobs[job_id]['stage'] = f'工作表 {index + 1}/{total} · {value["stage"]}'

                try:
                    run_review_task(registry.directory(), 'confirm', import_id=import_id,
                        mapping=mapping, save_template=body.save_template,
                        publish_guard=lambda: check_job_write(authorization, identity),
                        progress_callback=sheet_progress)
                except ValueError as exc:
                    failed.append({'sheet': name, 'message': str(exc), **error_location(exc)})
                else:
                    imported.append(name)
                update_job_progress(job_id, {'phase': 'batch', 'completed': index + 1, 'total': total})
            doc = manager.get(import_id)
            result = {**manager._result(doc), 'sheets_imported': imported, 'sheets_failed': failed,
                'sheets_pending': [sheet['name'] for sheet in doc['sheets']
                    if sheet['status'] not in READY and sheet['status'] != 'trusted_existing']}
            with job_lock:
                jobs[job_id].update(status='completed', result=result)
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status='failed', error=str(exc), **error_location(exc))
        finally:
            import_lock.release()

    @app.post('/api/imports/{import_id}/confirm-all')
    def confirm_all(import_id: str, body: BatchConfirmRequest, request: Request):
        manager.path(import_id)
        if not import_lock.acquire(blocking=False):
            raise HTTPException(409, '已有导入或确认任务，请等待完成。')
        job_id = uuid.uuid4().hex
        identity = current_identity.get()
        with job_lock:
            jobs[job_id] = {'id': job_id, 'tenant_id': identity.tenant_id,
                'knowledge_base_id': identity.knowledge_base_id, 'user_id': identity.user_id,
                'status': 'processing', 'stage': '逐表校验并导入已确认工作表，失败表保留待确认'}
        context = contextvars.copy_context()
        threading.Thread(target=context.run, args=(run_batch_confirm, job_id, import_id, body,
            request.state.verified_authorization, identity), daemon=True).start()
        return {'id': job_id, 'status': 'processing'}

    def run_assist_confirm(job_id, import_id, body, authorization, identity):
        deadline = time.monotonic() + 600

        def cancelled():
            with job_lock:
                return jobs[job_id].get('cancel_requested', False) or time.monotonic() >= deadline

        def progress(value):
            if cancelled():
                raise ValueError('AI 辅助已取消或超时，尚未确认的工作表未导入。')
            check_job_write(authorization, identity)
            current = progress_value({'phase': 'ai', 'completed': value.get('completed_blocks'),
                                      'total': value.get('total_blocks')})
            with job_lock:
                jobs[job_id].update(stage=str(value.get('stage') or current['stage']),
                    progress={**{key: item for key, item in current.items() if key != 'stage'},
                              **{key: value[key] for key in ('block_index', 'total_blocks', 'completed_blocks')
                                 if type(value.get(key)) is int}})

        def review(mapping):
            if cancelled():
                raise ValueError('AI 辅助已取消或超时，尚未确认的工作表未导入。')
            return run_review_task(registry.directory(), 'review', import_id=import_id,
                mapping=mapping, timeout=min(300, max(.001, deadline - time.monotonic())))

        try:
            from .ai_review import AIRequestBudget, assist_mapping
            check_job_write(authorization, identity)
            result = assist_mapping(manager, import_id, body.sheet, mapping=body.mapping,
                budget=AIRequestBudget(max_requests=body.max_requests), cancelled=cancelled, progress=progress,
                review=review)
            if cancelled():
                raise ValueError('AI 辅助已取消或超时，尚未确认的工作表未导入。')
            result = {**result, 'import_id': import_id, 'sheet': body.sheet,
                      'dataset_id': manager.get(import_id).get('dataset_id'), 'imported': False}
            if result.get('review_state', {}).get('status') == 'confirmed':
                check_job_write(authorization, identity)
                try:
                    actual = run_review_task(registry.directory(), 'preview', import_id=import_id,
                        mapping=result['mapping'], timeout=min(300, max(.001, deadline - time.monotonic())),
                        cancel_check=cancelled)
                except ValueError as exc:
                    if not cancelled():
                        exc.review_state = review(result['mapping'])
                    raise
                if not actual.get('valid_observation_count'):
                    from .batch_ai_import import NO_DATA_NOTICE
                    run_review_task(registry.directory(), 'mark_skip', import_id=import_id,
                        mapping=result['mapping'], publish_guard=lambda: check_job_write(authorization, identity),
                        timeout=min(300, max(.001, deadline - time.monotonic())))
                    result.update(skipped={'code': 'no_indicator_data', 'message': NO_DATA_NOTICE},
                                  notice=NO_DATA_NOTICE)
                    with job_lock:
                        jobs[job_id].update(status='completed', result=result, cancellable=False)
                    return
                strict_state = review(result['mapping'])
                if strict_state.get('status') != 'confirmed':
                    result.update(review_state=strict_state,
                        notice='AI 建议未通过逐区块真实数据检查，请补齐或排除无数据区块。')
                    with job_lock:
                        jobs[job_id].update(status='completed', result=result, cancellable=False)
                    return
                with job_lock:
                    if jobs[job_id].get('cancel_requested') or time.monotonic() >= deadline:
                        raise ValueError('AI 辅助已取消或超时，尚未确认的工作表未导入。')
                    jobs[job_id].update(cancellable=False, stage='全部区块检查通过，校验并导入当前表')
                published = run_review_task(registry.directory(), 'confirm', import_id=import_id,
                    mapping=result['mapping'], save_template=body.save_template,
                    publish_guard=lambda: check_job_write(authorization, identity),
                    progress_callback=lambda value: update_job_progress(job_id, value))
                result.update(published, imported=True)
            with job_lock:
                jobs[job_id].update(status='completed', result=result, cancellable=False)
        except Exception as exc:
            with job_lock:
                status = 'cancelled' if jobs[job_id].get('cancel_requested') else 'failed'
                jobs[job_id].update(status=status, error=str(exc), cancellable=False, **error_location(exc))
        finally:
            import_lock.release()

    @app.post('/api/imports/{import_id}/assist-confirm')
    def assist_confirm(import_id: str, body: AssistConfirmRequest, request: Request):
        manager.sheet(import_id, body.sheet)
        if body.mapping is not None and body.mapping.get('sheet') != body.sheet:
            raise HTTPException(400, '当前草稿不属于所选工作表。')
        job_id = body.request_id or uuid.uuid4().hex
        identity = current_identity.get()
        with job_lock:
            prune_pending_ai_cancels()
            if job_id in jobs:
                raise HTTPException(409, '任务编号已使用，请重新发起。')
            cancel_key = ai_cancel_key(identity, job_id)
            if cancel_key in pending_ai_cancels:
                pending_ai_cancels.pop(cancel_key)
                jobs[job_id] = {'id': job_id, 'tenant_id': identity.tenant_id,
                    'knowledge_base_id': identity.knowledge_base_id, 'user_id': identity.user_id,
                    'status': 'cancelled', 'cancel_requested': True, 'cancellable': False,
                    'error': 'AI 辅助已取消，未调用模型或自动导入。'}
                return {'id': job_id, 'status': 'cancelled'}
            if not import_lock.acquire(blocking=False):
                raise HTTPException(409, '已有导入或确认任务，请等待完成。')
            jobs[job_id] = {'id': job_id, 'tenant_id': identity.tenant_id,
                'knowledge_base_id': identity.knowledge_base_id, 'user_id': identity.user_id,
                'status': 'processing', 'stage': '检查当前映射，逐个辅助待确认区块', 'cancellable': True}
        context = contextvars.copy_context()
        try:
            threading.Thread(target=context.run, args=(run_assist_confirm, job_id, import_id, body,
                request.state.verified_authorization, identity), daemon=True).start()
        except Exception:
            with job_lock:
                jobs[job_id].update(status='failed', cancellable=False, error='AI 任务未能启动，请重试。')
            import_lock.release()
            raise
        return {'id': job_id, 'status': 'processing'}

    def run_assist_confirm_all(job_id, import_id, body, authorization, identity):
        from .batch_ai_import import import_all_with_ai

        def cancelled():
            with job_lock:
                return bool(jobs[job_id].get('cancel_requested'))

        def progress(value):
            current = progress_value({'phase': 'batch', 'completed': value['completed'], 'total': value['total']})
            with job_lock:
                jobs[job_id].update(
                    stage=f'工作表 {min(value["completed"] + 1, value["total"])}/{value["total"]} · {value["stage"]}',
                    progress={key: item for key, item in current.items() if key != 'stage'},
                    batch_ai={key: item for key, item in value.items() if key != 'stage'})

        def sheet_result(row):
            # Keep visible partial results while a later model request is
            # running. The final result is rebuilt from authoritative state.
            with job_lock:
                result = jobs[job_id]['result']
                result['details'].append(row)
                field = {'imported': 'sheets_imported', 'existing': 'sheets_existing',
                         'needs_review': 'sheets_pending', 'not_started': 'sheets_pending',
                         'failed': 'sheets_failed', 'skipped_no_data': 'sheets_skipped'}[row['status']]
                result[field].append(row if field in {'sheets_failed', 'sheets_skipped'} else row['sheet'])
                result['requests_used'] += row['requests_used']

        try:
            result = import_all_with_ai(manager, import_id, mappings=body.mappings,
                save_template=body.save_template, total_max_requests=body.total_max_requests,
                per_sheet_max_requests=body.per_sheet_max_requests,
                task=lambda action, **kwargs: run_review_task(registry.directory(), action, **kwargs),
                check_write=lambda: check_job_write(authorization, identity), cancelled=cancelled,
                progress=progress, sheet_result=sheet_result)
            stopped = result.get('stopped')
            status = 'cancelled' if stopped == 'cancelled' else 'failed' if stopped else 'completed'
            with job_lock:
                jobs[job_id].update(status=status, result=result, cancellable=False,
                    stage=result['notice'], **({'error': result['notice']} if stopped else {}))
        except Exception as exc:
            with job_lock:
                jobs[job_id].update(status='cancelled' if jobs[job_id].get('cancel_requested') else 'failed',
                    error=str(exc)[:2000], cancellable=False)
        finally:
            import_lock.release()

    @app.post('/api/imports/{import_id}/assist-confirm-all')
    def assist_confirm_all(import_id: str, body: BatchAssistConfirmRequest, request: Request):
        manager.path(import_id)
        if not body.acknowledged_risk:
            raise HTTPException(400, '请先确认逐表 AI 辅助的调用预算和自动导入风险。')
        names = set()
        for mapping in body.mappings:
            name = mapping.get('sheet')
            if not isinstance(name, str) or name in names:
                raise HTTPException(400, '批量 AI 的工作表草稿无效或重复。')
            manager.sheet(import_id, name)
            names.add(name)
        job_id = body.request_id or uuid.uuid4().hex
        identity = current_identity.get()
        with job_lock:
            prune_pending_ai_cancels()
            if job_id in jobs:
                raise HTTPException(409, '任务编号已使用，请重新发起。')
            cancel_key = ai_cancel_key(identity, job_id)
            if cancel_key in pending_ai_cancels:
                pending_ai_cancels.pop(cancel_key)
                jobs[job_id] = {'id': job_id, 'tenant_id': identity.tenant_id,
                    'knowledge_base_id': identity.knowledge_base_id, 'user_id': identity.user_id,
                    'status': 'cancelled', 'cancel_requested': True, 'cancellable': False,
                    'error': '批量 AI 已取消，未调用模型或自动导入。'}
                return {'id': job_id, 'status': 'cancelled'}
            if not import_lock.acquire(blocking=False):
                raise HTTPException(409, '已有导入或确认任务，请等待完成。')
            jobs[job_id] = {'id': job_id, 'kind': 'batch_ai', 'tenant_id': identity.tenant_id,
                'knowledge_base_id': identity.knowledge_base_id, 'user_id': identity.user_id,
                'status': 'processing', 'stage': '逐表检查，辅助待确认区块并导入核验通过的表',
                'cancellable': True, 'result': {'import_id': import_id,
                    'sheets_imported': [], 'sheets_existing': [], 'sheets_pending': [],
                    'sheets_failed': [], 'sheets_skipped': [], 'details': [], 'requests_used': 0}}
        context = contextvars.copy_context()
        try:
            threading.Thread(target=context.run, args=(run_assist_confirm_all, job_id, import_id, body,
                request.state.verified_authorization, identity), daemon=True).start()
        except Exception:
            with job_lock:
                jobs[job_id].update(status='failed', cancellable=False, error='批量 AI 任务未能启动，请重试。')
            import_lock.release()
            raise
        return {'id': job_id, 'status': 'processing'}

    def ai_cancel_key(identity, job_id):
        return (identity.tenant_id, identity.knowledge_base_id, identity.user_id, job_id)

    def prune_pending_ai_cancels():
        # Caller holds job_lock. A bounded ten-minute intent covers delayed
        # requests without retaining arbitrary IDs or growing with page closes.
        now = time.monotonic()
        for key, expiry in list(pending_ai_cancels.items()):
            if expiry <= now:
                pending_ai_cancels.pop(key, None)

    @app.post('/api/jobs/{job_id}/cancel')
    def cancel_job(job_id: str):
        identity = current_identity.get()
        with job_lock:
            record = jobs.get(job_id)
            if record is None:
                if len(job_id) != 32 or any(c not in '0123456789abcdef' for c in job_id):
                    raise HTTPException(404, '任务不存在。')
                prune_pending_ai_cancels()
                key = ai_cancel_key(identity, job_id)
                if key not in pending_ai_cancels:
                    own = sum(item[:3] == key[:3] for item in pending_ai_cancels)
                    if own >= 32 or len(pending_ai_cancels) >= 512:
                        raise HTTPException(429, '待取消任务过多，请稍后重试。')
                pending_ai_cancels[key] = time.monotonic() + 600
                return {'id': job_id, 'status': 'cancelled', 'cancel_requested': True}
            if (record.get('tenant_id') != identity.tenant_id
                    or record.get('knowledge_base_id') != identity.knowledge_base_id
                    or record.get('user_id') != identity.user_id):
                raise HTTPException(404, '任务不存在。')
            if record.get('cancel_requested') and record.get('status') == 'cancelled':
                return {'id': job_id, 'status': 'cancelled', 'cancel_requested': True}
            if record.get('status') != 'processing' or not record.get('cancellable'):
                raise HTTPException(409, '任务已结束或正在保存已确认数据，无法取消。')
            record['cancel_requested'] = True
            record['stage'] = ('正在取消；已完成工作表保留，后续不再导入' if record.get('kind') == 'batch_ai'
                               else '正在取消，等待当前请求返回；不会自动导入')
        return {'id': job_id, 'status': 'processing', 'cancel_requested': True}

    @app.get("/api/templates")
    def templates():
        return {"templates": manager.list_templates()}

    @app.exception_handler(ValueError)
    async def value_error(request, exc):
        return JSONResponse({"detail": str(exc), **error_location(exc)}, status_code=400)

    app.mount("/", mcp_app)
    return app

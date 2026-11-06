"""Signed, source-scoped native-chart reads using the structured-service contract."""
from datetime import date
from typing import Literal

import httpx
from fastapi import HTTPException
from pydantic import Field, ValidationError

from tin.schemas.chart import ChartModel


class NativeStyle(ChartModel):
    color: str | None = Field(default=None, pattern=r'^#[0-9a-fA-F]{6}$')
    width_pt: float | None = Field(default=None, gt=0)
    dash: Literal['solid', 'dash', 'dot'] | None = None


class NativeDescriptor(ChartModel):
    sheet: str = ''
    metric: str = ''
    column: str = ''
    start_row: int | None = None
    end_row: int | None = None


class NativeCurve(ChartModel):
    label: str = ''
    chart_type: str
    axis: Literal['primary', 'secondary'] = 'primary'
    series_id: str | None = None
    status: Literal['linked', 'pending_source', 'unsupported', 'no_data', 'unconfirmed_sheet']
    reason: str | None = None
    first_date: date | None = None
    last_date: date | None = None
    unit: str | None = None
    descriptor: NativeDescriptor = Field(default_factory=NativeDescriptor)
    style: NativeStyle = Field(default_factory=NativeStyle)


class NativeAxis(ChartModel):
    id: str
    type: Literal['dateAx', 'catAx', 'valAx']
    title: str | None = None
    position: Literal['l', 'r', 'b', 't'] | None = None


class NativeChart(ChartModel):
    chart_key: str = Field(pattern=r'^nc-[0-9a-f]{32}$')
    knowledge_id: str
    source_file: str
    sheet: str
    title: str
    axes: list[NativeAxis] = Field(default_factory=list)
    curves: list[NativeCurve]


class NativeChartPage(ChartModel):
    charts: list[NativeChart]
    total: int = Field(ge=0, strict=True)
    offset: int = Field(ge=0, strict=True)
    next_offset: int | None
    # Optional provenance envelopes are verified when supplied. The specified
    # native-charts response does not require the /facts envelope.
    scope_version: int | None = None
    workbench_id: str | None = None
    sources_revision: int | None = None
    version: str | None = Field(default=None, pattern=r'^[0-9a-f]{64}$')
    truncated: bool | None = None


def native_chart_page(scope, *, offset=0, limit=200):
    from tin.integration.workbench_scope import current_workbench
    from tin.provenance.bridge import structured_request
    if scope.restricted:
        raise HTTPException(403, '受限视角暂不开放原表图表')
    if scope.scope_version != 2:
        raise HTTPException(409, '请重新打开研究台更新来源授权')
    if type(offset) is not int or offset < 0 or type(limit) is not int or not 1 <= limit <= 200:
        raise HTTPException(422, '原表图表分页参数无效')
    token = current_workbench.set(scope)
    try:
        response = structured_request('POST', '/api/research/native-charts', {'offset': offset, 'limit': limit},
                                      return_error_response=True)
        if response.status_code in {400, 401, 403, 404, 409, 413, 422}:
            raise HTTPException(response.status_code, '原表图表来源或授权已变化，请刷新核对')
        response.raise_for_status()
        page = NativeChartPage.model_validate(response.json())
        if (page.offset != offset or len(page.charts) > limit
                or offset + len(page.charts) > max(offset, page.total)
                or page.next_offset is not None and (not page.charts or page.next_offset != offset + len(page.charts)
                                                     or page.next_offset >= page.total)
                or page.next_offset is None and offset + len(page.charts) < page.total
                or page.truncated is not None and page.truncated != (page.next_offset is not None)):
            raise ValueError('Invalid native chart pagination')
        for key, expected in (('scope_version', 2), ('workbench_id', scope.workbench_id), ('sources_revision', scope.sources_revision)):
            if getattr(page, key) is not None and getattr(page, key) != expected:
                raise HTTPException(409, '原表图表来源版本在读取期间已改变')
        return page
    except HTTPException:
        raise
    except (httpx.HTTPError, ValidationError, ValueError, TypeError, AttributeError, OSError):
        raise HTTPException(503, '当前原表图表暂不可读；未使用缓存图值或历史副本') from None
    finally:
        current_workbench.reset(token)

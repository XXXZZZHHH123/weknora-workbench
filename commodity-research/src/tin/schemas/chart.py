"""Strict, data-independent definitions for shared charts and cycle templates."""
from datetime import date
import re
from typing import Annotated, Literal

from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, field_validator, model_validator


class ChartModel(BaseModel):
    model_config = ConfigDict(extra='forbid', allow_inf_nan=False)


def calendar_date(value):
    if type(value) is date:
        return value
    if not isinstance(value, str) or date.fromisoformat(value).isoformat() != value:
        raise ValueError('日期需要明确为YYYY-MM-DD')
    return date.fromisoformat(value)


CalendarDate = Annotated[date, BeforeValidator(calendar_date)]


def month_day(value: str) -> str:
    if not re.fullmatch(r'\d{2}-\d{2}', value):
        raise ValueError('周期起点需要为MM-DD')
    date.fromisoformat('2000-' + value)
    return value


class TemplateBase(ChartModel):
    name: str = Field(min_length=1, max_length=40)
    align: Literal['days'] = 'days'
    transform: Literal['raw', 'change', 'rebase'] = 'raw'


class RepeatTemplate(TemplateBase):
    kind: Literal['repeat']
    start: str
    period: Literal['year', 'half', 'quarter', 'month']
    _start = field_validator('start')(month_day)


class EventAnchor(ChartModel):
    label: str = Field(min_length=1, max_length=120)
    date: CalendarDate


class EventTemplate(TemplateBase):
    kind: Literal['event']
    calendar: Literal['spring_festival'] | None = None
    anchors: list[EventAnchor] | None = Field(default=None, min_length=1, max_length=40)
    before_days: int = Field(ge=0, le=3660, strict=True)
    after_days: int = Field(ge=0, le=3660, strict=True)

    @model_validator(mode='after')
    def one_calendar(self):
        if (self.calendar is None) == (self.anchors is None):
            raise ValueError('事件模板需明确选择日历或事件日期')
        return self


class CycleSegment(ChartModel):
    label: str = Field(min_length=1, max_length=120)
    start: CalendarDate
    end: CalendarDate

    @model_validator(mode='after')
    def ordered(self):
        if self.start > self.end:
            raise ValueError('周期终点不能早于起点')
        return self


class SegmentsTemplate(TemplateBase):
    kind: Literal['segments']
    segments: list[CycleSegment] = Field(min_length=1, max_length=40)


CycleTemplate = Annotated[RepeatTemplate | EventTemplate | SegmentsTemplate, Field(discriminator='kind')]


class CurveStyle(ChartModel):
    color: str | None = Field(default=None, pattern=r'^#[0-9a-fA-F]{6}$')
    width: float | None = Field(default=None, gt=0, le=100)
    dash: Literal['solid', 'dash', 'dot'] | None = None


class SegmentStyle(CurveStyle):
    label: str = Field(min_length=1, max_length=40)


class CurveDescriptor(ChartModel):
    source_file: str = Field(default='', max_length=1000)
    sheet: str = Field(default='', max_length=1000)
    metric: str = Field(default='', max_length=1000)
    column: str = Field(default='', max_length=80)


class DerivedSeries(ChartModel):
    op: Literal['sub', 'div']
    a: str = Field(min_length=1, max_length=128)
    b: str = Field(min_length=1, max_length=128)
    # This records the researcher's explicit confirmation, not a unit inferred
    # from the label or a programmatic conversion.
    unit_confirmed: bool = Field(default=False, strict=True)


class ChartCurve(ChartModel):
    series_id: str | None = Field(default=None, min_length=1, max_length=128)
    derived: DerivedSeries | None = None
    label: str = Field(min_length=1, max_length=120)
    kind: Literal['line', 'bar', 'area'] = 'line'
    axis: Literal['left', 'right'] = 'left'
    style: CurveStyle = Field(default_factory=CurveStyle)
    descriptor: CurveDescriptor = Field(default_factory=CurveDescriptor)
    curve_cycles: Literal['current', 'current_and_previous'] | None = None

    @model_validator(mode='after')
    def one_source(self):
        if (self.series_id is None) == (self.derived is None):
            raise ValueError('每条曲线需明确选择一个指标或一个计算公式')
        return self


class RecentTime(ChartModel):
    mode: Literal['recent']
    points: int = Field(default=120, ge=1, le=2000, strict=True)
    transform: Literal['raw', 'change', 'rebase'] = 'raw'


class SinceTime(ChartModel):
    mode: Literal['since']
    start: CalendarDate
    transform: Literal['raw', 'change', 'rebase'] = 'raw'


class RangeTime(ChartModel):
    mode: Literal['range']
    start: CalendarDate
    end: CalendarDate
    transform: Literal['raw', 'change', 'rebase'] = 'raw'

    @model_validator(mode='after')
    def ordered(self):
        if self.start > self.end:
            raise ValueError('图表终点不能早于起点')
        return self


class CycleTime(ChartModel):
    mode: Literal['cycle']
    template: CycleTemplate
    cycles: int = Field(default=8, ge=1, strict=True)
    visible_cycles: int | None = Field(default=None, ge=1, le=40, strict=True)
    band_cycles: int = Field(default=5, ge=1, strict=True)
    transform: Literal['raw', 'change', 'rebase'] = 'raw'
    curve_cycles: Literal['current', 'current_and_previous'] = 'current'
    segment_styles: list[SegmentStyle] = Field(default_factory=list, max_length=40)

    @model_validator(mode='after')
    def template_transform(self):
        if 'transform' not in self.model_fields_set:
            self.transform = self.template.transform
        return self


ChartTime = Annotated[RecentTime | SinceTime | RangeTime | CycleTime, Field(discriminator='mode')]


class AxisRange(ChartModel):
    min: float | None = None
    max: float | None = None

    @model_validator(mode='after')
    def ordered(self):
        if self.min is not None and self.max is not None and self.min >= self.max:
            raise ValueError('纵轴最大值必须大于最小值')
        return self


class ChartAxes(ChartModel):
    left: AxisRange = Field(default_factory=AxisRange)
    right: AxisRange = Field(default_factory=AxisRange)


class ChartOrigin(ChartModel):
    kind: Literal['manual', 'native', 'chat', 'ppt'] = 'manual'
    ref: str = Field(default='', max_length=200)


class ChartDefinition(ChartModel):
    format: Literal['chart.v1'] = 'chart.v1'
    title: str = Field(min_length=1, max_length=120)
    group: str = Field(default='', max_length=80)
    note: str = Field(default='', max_length=600)
    curves: list[ChartCurve] = Field(min_length=1, max_length=16)
    time: ChartTime = Field(default_factory=lambda: RecentTime(mode='recent'))
    axes: ChartAxes = Field(default_factory=ChartAxes)
    origin: ChartOrigin = Field(default_factory=ChartOrigin)

    @model_validator(mode='after')
    def cycle_curve(self):
        if not self.title.strip():
            raise ValueError('请填写图表标题')
        if self.time.mode == 'cycle' and len(self.curves) > 8:
            raise ValueError('同周期对比最多支持8条指标曲线，请减少指标或使用批量周期图')
        return self


class ChartUpdate(ChartModel):
    revision: int = Field(ge=1, strict=True)
    format: Literal['chart.v1'] | None = None
    title: str | None = Field(default=None, min_length=1, max_length=120)
    group: str | None = Field(default=None, max_length=80)
    note: str | None = Field(default=None, max_length=600)
    curves: list[ChartCurve] | None = Field(default=None, min_length=1, max_length=16)
    time: ChartTime | None = None
    axes: ChartAxes | None = None
    origin: ChartOrigin | None = None


class RevisionInput(ChartModel):
    revision: int = Field(ge=1, strict=True)


class NativePreviewInput(ChartModel):
    offset: int = Field(default=0, ge=0, strict=True)
    limit: int = Field(default=200, ge=1, le=200, strict=True)


class NativeSheetSelection(ChartModel):
    knowledge_id: str = Field(min_length=1, max_length=128)
    sheet: str = Field(max_length=1000)
    chart_keys: list[str] | None = Field(default=None, min_length=1, max_length=2000)
    start: str | None = None

    @field_validator('start')
    @classmethod
    def valid_start(cls, value):
        return month_day(value) if value is not None else value


class NativeApplyInput(ChartModel):
    sheets: list[NativeSheetSelection] = Field(min_length=1, max_length=200)

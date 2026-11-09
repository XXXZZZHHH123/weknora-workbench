"""判断单元字段规格（04 §1.4 / §4）。结构校验在这里；“能否生效”的硬规则在 judgments/validate.py。"""

import re
from datetime import date
from enum import StrEnum
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_serializer, model_validator
from tin.judgments.numbers import NUMBER, check_rule_number, exact_float

TONES = ("偏多", "偏空", "观望", "区间震荡", "回调买入", "反弹做空", "等信号")
PARADIGMS = ("总线合成", "对抗称重", "编排层")
EXPRESSION = re.compile(r"^(value|change)\s*(>=|<=|>|<)\s*(" + NUMBER + r")$")


class ThresholdType(StrEnum):
    风控线 = "风控线"
    介入位 = "介入位"
    观察位 = "观察位"


class Horizon(StrEnum):
    短期 = "短期"
    中长期 = "中长期"


class _Model(BaseModel):
    model_config = ConfigDict(extra="forbid")


class _Bindings(_Model):
    related_series_ids: list[Annotated[str, Field(strict=True, min_length=1, max_length=256)]] = Field(
        default_factory=list, max_length=7)

    @model_validator(mode='after')
    def _distinct_bindings(self):
        ids = ([self.series_id] if getattr(self, 'series_id', None) else []) + self.related_series_ids
        if len(ids) != len(set(ids)):
            raise ValueError('同一条目不能重复绑定相同指标')
        return self

    @model_serializer(mode='wrap')
    def _without_empty_related(self, handler):
        value = handler(self)
        if not value.get('related_series_ids'):
            value.pop('related_series_ids', None)
        return value


def binding_ids(value):
    """Ordered explicit bindings; only series_id owns quantitative semantics."""
    primary = value.get('series_id') if isinstance(value, dict) else getattr(value, 'series_id', None)
    related = value.get('related_series_ids', []) if isinstance(value, dict) else getattr(value, 'related_series_ids', [])
    return ([primary] if primary else []) + list(related)


class Evidence(_Bindings):
    desc: str
    series_id: str | None = None
    value: float | None = None
    as_of: str | None = None
    source: str | None = None
    note: str | None = None


class Side(_Model):
    claim: str = ""
    evidence: list[Evidence] = Field(default_factory=list)


class Window(_Model):
    start: str | None = Field(None, alias="from")
    to: str | None = None
    note: str | None = None

    model_config = ConfigDict(extra="forbid", populate_by_name=True)


class Contradiction(_Model):
    bull: Side = Field(default_factory=Side)
    bear: Side = Field(default_factory=Side)
    window: Window | None = None
    text: str | None = None


class MarginalItem(_Bindings):
    desc: str
    series_id: str | None = None


class CostItem(_Bindings):
    desc: str
    series_id: str | None = None
    value: float | None = None


class CostLine(_Model):
    type: Literal["cash", "full"] | None = None
    value: float | None = None
    source: str | None = None
    items: list[CostItem] = Field(default_factory=list)
    note: str | None = None


class PricingPower(_Model):
    category: Literal["有境外市场", "有LME外盘", "纯宏观", "无外盘"] | None = None
    short: list[str] = Field(default_factory=list)
    long: list[str] = Field(default_factory=list)
    key: str | None = None
    sources: list[str] = Field(default_factory=list)


class TimeDimension(_Model):
    short: str | None = None
    long: str | None = None


class MovingAverageTarget(_Model):
    kind: Literal['sma']
    window: int = Field(ge=2, le=120, strict=True)


class CaliberConfirmation(_Model):
    """A human declaration stored only with this saved rule, never with a source."""
    series_id: str = Field(min_length=1, max_length=256)
    unit: str = Field(min_length=1, max_length=80)
    basis: str = Field(min_length=1, max_length=1000)
    scope_note: str = Field(default='', max_length=1000)
    confirmed_by: str = Field(min_length=1, max_length=128)
    confirmed_at: str = Field(min_length=16, max_length=40)
    catalog_version: str = Field(min_length=1, max_length=128)
    frequency: Literal['日', '周', '月', '季', '年'] | None = None

    @model_serializer(mode='wrap')
    def _without_empty_frequency(self, handler):
        value = handler(self)
        if value.get('frequency') is None:
            value.pop('frequency', None)
        return value

    @field_validator('unit', 'basis')
    @classmethod
    def _not_blank(cls, value):
        if not value.strip():
            raise ValueError('人工口径必须填写单位和核对依据')
        return value.strip()


class _Rule(_Bindings):
    @model_serializer(mode='wrap')
    def _without_empty_caliber(self, handler):
        value = handler(self)
        if value.get('caliber_confirmation') is None:
            value.pop('caliber_confirmation', None)
        if not value.get('related_series_ids'):
            value.pop('related_series_ids', None)
        return value


class Threshold(_Rule):
    id: str
    name: str
    series_id: str
    operator: Literal[">", ">=", "<", "<=", "between", "crosses_below", "crosses_above"]
    value: float | list[float] | MovingAverageTarget
    type: ThresholdType
    horizon: Horizon
    action_hint: str | None = None
    note: str | None = None
    caliber_confirmation: CaliberConfirmation | None = None

    @field_validator('value', mode='before')
    @classmethod
    def _exact_number(cls, value):
        if isinstance(value, (dict, MovingAverageTarget)):
            return value
        for number in value if isinstance(value, list) else [value]:
            check_rule_number(number)
        return value

    @model_validator(mode="after")
    def _value_shape(self):
        if self.operator in {"crosses_below", "crosses_above"} and not isinstance(self.value, MovingAverageTarget):
            raise ValueError("均线交叉事件必须引用简单移动平均窗口")
        if self.operator == "between":
            if not (isinstance(self.value, list) and len(self.value) == 2 and self.value[0] < self.value[1]):
                raise ValueError(f"阈值 {self.id}：区间必须是 [下限, 上限] 且下限小于上限")
        elif isinstance(self.value, list):
            raise ValueError(f"阈值 {self.id}：运算符 {self.operator} 只接受单个数值")
        return self


class Falsifier(_Rule):
    id: str
    desc: str
    series_id: str
    expression: str
    unit_confirmed: str
    consecutive_periods: int = Field(1, ge=1)
    side: Literal["bull", "bear"] = Field(description='被证伪的原假设一方：bull为多方原假设失效，bear为空方原假设失效；不是条件触发后的价格涨跌方向。')
    note: str | None = None
    caliber_confirmation: CaliberConfirmation | None = None

    @field_validator("expression")
    @classmethod
    def _expr(cls, v: str) -> str:
        match = EXPRESSION.fullmatch(v.strip())
        if match is None:
            raise ValueError(f"判定表达式无法自动验证：「{v}」，请写成 value/change 比较一个数，如 value >= 10000")
        exact_float(match.group(3))
        return v.strip()


class PriorityRule(_Model):
    order: list[str]
    note: str | None = None


class UnstructuredItem(_Bindings):
    section: str
    text: str
    reason: str
    series_id: str | None = None
    business_condition: str | None = None
    rule_kind: Literal["threshold", "falsifier"] | None = None

    @model_serializer(mode="wrap")
    def _preserve_condition_presence(self, handler):
        result = handler(self)
        # Old saved JSON has neither key. Explicit new nulls identify audit-only
        # entries and must not acquire the legacy display compatibility.
        for name in ("business_condition", "rule_kind", "series_id"):
            if name not in self.model_fields_set:
                result.pop(name, None)
        if not result.get('related_series_ids'):
            result.pop('related_series_ids', None)
        return result


class JudgmentIn(_Model):
    variety: str
    author: str
    written_at: date
    review_period_days: int = Field(30, ge=1)
    contradiction: Contradiction
    marginal_focus: list[MarginalItem] = Field(default_factory=list)
    cost_line: CostLine | None = None
    pricing_power: PricingPower = Field(default_factory=PricingPower)
    time_dimension: TimeDimension | None = None
    survey_notes: str | None = None
    whitelist: list[str] = Field(default_factory=list)
    paradigm: str | None = None
    tone: str | None = None
    tone_note: str | None = None
    thresholds: list[Threshold] = Field(default_factory=list)
    priority_rule: PriorityRule | None = None
    falsifiers: list[Falsifier] = Field(default_factory=list)
    unstructured: list[UnstructuredItem] = Field(default_factory=list)
    source_note: str | None = None
    chart_references: list[dict] = Field(default_factory=list, max_length=32)

    @field_validator("tone")
    @classmethod
    def _tone(cls, v):
        if v is not None and v not in TONES:
            raise ValueError(f"基调必须取自固定词表：{'/'.join(TONES)}")
        return v

    @field_validator("paradigm")
    @classmethod
    def _paradigm(cls, v):
        if v is not None and v not in PARADIGMS:
            raise ValueError(f"编排范式必须是：{'/'.join(PARADIGMS)}")
        return v

    @model_validator(mode="after")
    def _ids_unique(self):
        ids = [t.id for t in self.thresholds] + [f.id for f in self.falsifiers]
        dup = {i for i in ids if ids.count(i) > 1}
        if dup:
            raise ValueError(f"阈值/证伪条件 id 重复：{sorted(dup)}")
        for reference in self.chart_references:
            if (reference.get('kind', 'chart') != 'chart'
                    or not isinstance(reference.get('id'), str) or not 1 <= len(reference['id']) <= 128
                    or type(reference.get('revision')) is not int or reference['revision'] < 1):
                raise ValueError('图表引用需要明确的图表ID和修订号')
            target = reference.get('target', {'kind': 'judgment'})
            if not isinstance(target, dict) or not isinstance(target.get('kind'), str) or target['kind'] not in {'judgment', 'threshold', 'falsifier', 'evidence'}:
                raise ValueError('图表挂载位置无效')
            kind = target['kind']
            if kind in {'threshold', 'falsifier'}:
                rules = self.thresholds if kind == 'threshold' else self.falsifiers
                if not isinstance(target.get('id'), str) or target['id'] not in {rule.id for rule in rules}:
                    raise ValueError('图表挂载的规则不在当前研判中')
            if kind == 'evidence':
                side, index = target.get('side'), target.get('index')
                if not isinstance(side, str) or side not in {'bull', 'bear'} or type(index) is not int or not 0 <= index < len(getattr(self.contradiction, side).evidence):
                    raise ValueError('图表挂载的证据不在当前研判中')
            allowed = {'kind'} | ({'id'} if kind in {'threshold', 'falsifier'} else {'side', 'index'} if kind == 'evidence' else set())
            if set(target) != allowed:
                raise ValueError('图表挂载参数无效')
        if self.priority_rule:
            unknown = set(self.priority_rule.order) - {t.id for t in self.thresholds}
            if unknown:
                raise ValueError(f"冲突优先级引用了不存在的阈值：{sorted(unknown)}")
        return self

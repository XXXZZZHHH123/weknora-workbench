"""Researchers' presentation choices for a dated, one-page report."""
from fastapi import HTTPException
from pydantic import BaseModel, ConfigDict, Field, model_validator

from tin.integration.page_facts import SERIES
from tin.web.research_dimensions import DIMENSION_ID, read_dimensions

MAX_FIGURE_GROUPS = 100


class ReportLayout(BaseModel):
    model_config = ConfigDict(extra='forbid')

    dimension_order: list[str] = Field(default_factory=list, max_length=12)
    figure_groups: dict[str, str] = Field(default_factory=dict, max_length=MAX_FIGURE_GROUPS)

    @model_validator(mode='after')
    def validate_identifiers(self):
        if (len(set(self.dimension_order)) != len(self.dimension_order)
                or not all(DIMENSION_ID.fullmatch(value) for value in (*self.dimension_order, *self.figure_groups.values()))
                or not all(SERIES.fullmatch(value) for value in self.figure_groups)):
            raise ValueError('版式需要有效的维度与指标 ID')
        return self


def validate_layout(session, scope, layout):
    dimensions = {row['id'] for row in read_dimensions(session)}
    if not dimensions.issuperset((*layout.dimension_order, *layout.figure_groups.values())):
        raise HTTPException(422, '周报版式引用的维度尚未保存或已删除，请重新核对')
    if layout.figure_groups:
        from tin.web.chart_data import catalog_fields
        fields = catalog_fields(scope, layout.figure_groups)
        if set(fields) != set(layout.figure_groups):
            raise HTTPException(422, '周报版式含已不可用的指标，请重新核对')


def report_layout(payload):
    return (((payload.get('manual_report') or {}).get('draft') or {}).get('layout') or {})


def ordered_dimensions(payload, dimensions):
    order = {identifier: index for index, identifier in enumerate(report_layout(payload).get('dimension_order') or [])}
    return sorted(dimensions, key=lambda row: order.get(row['id'], len(order))) if order else dimensions

"""Explicit PPT binding edits and frontend-supplied export provenance."""
from typing import Annotated, Literal

from pydantic import AliasChoices, BaseModel, ConfigDict, Field, model_validator

from tin.schemas.chart import CalendarDate


class PptModel(BaseModel):
    model_config = ConfigDict(extra='forbid', allow_inf_nan=False)


class SourceSelection(PptModel):
    knowledge_base_id: str = Field(min_length=1, max_length=128)
    knowledge_id: str = Field(min_length=1, max_length=128)
    file_hash: str = Field(pattern=r'^(?:[0-9a-fA-F]{32}|[0-9a-fA-F]{64})$')


class SourceReplacement(SourceSelection):
    revision: int = Field(ge=1, strict=True)


class ChartBinding(PptModel):
    kind: Literal['chart']
    chart_id: str = Field(min_length=1, max_length=128)


class SeriesBinding(PptModel):
    kind: Literal['series']
    series_id: str = Field(pattern=r'^kb-[0-9a-f]{48}$')
    rows: int = Field(ge=1, le=60, strict=True)


class KeepBinding(PptModel):
    kind: Literal['keep']


Binding = Annotated[ChartBinding | SeriesBinding | KeepBinding, Field(discriminator='kind')]


class SlotBinding(PptModel):
    slot_id: str = Field(min_length=1, max_length=128)
    binding: Binding | None


class BindingsUpdate(PptModel):
    revision: int = Field(ge=1, strict=True)
    slots: list[SlotBinding] = Field(min_length=1)

    @model_validator(mode='after')
    def unique_slots(self):
        if len({slot.slot_id for slot in self.slots}) != len(self.slots):
            raise ValueError('每个图位只能提交一次绑定')
        return self


class ExportSlotMeta(PptModel):
    """meta is JSON {slot_id: {data_as_of: 'YYYY-MM-DD', error?: '...'}}.

    as_of is accepted as an alias. Dates are frontend declarations, not server
    attestations about the pixels. An error keeps the original even with a PNG.
    """
    data_as_of: CalendarDate | None = Field(default=None, validation_alias=AliasChoices('data_as_of', 'as_of'))
    error: str | None = Field(default=None, max_length=1000)

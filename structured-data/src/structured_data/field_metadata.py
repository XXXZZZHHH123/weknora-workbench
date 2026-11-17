"""Optional import metadata contract, mirrored by the research consumer."""
from typing import Literal
from pydantic import BaseModel, ConfigDict, Field, field_validator


class MetadataEvidence(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    sheet: str = Field(min_length=1, max_length=128)
    cell: str = Field(pattern=r'^(?:[A-Z]{1,3}[1-9][0-9]*|\$sheet_title)$')
    text: str = Field(min_length=1, max_length=32767)


class FieldMetadata(BaseModel):
    model_config = ConfigDict(extra='forbid', strict=True)
    role: Literal['numeric', 'text_dimension', 'annotation'] | None = None
    unit: str | None = Field(default=None, max_length=128)
    unit_source: Literal['source', 'declared'] | None = None
    frequency: str | None = Field(default=None, max_length=32)
    unit_evidence: list[MetadataEvidence] = Field(default_factory=list, max_length=16)
    frequency_evidence: list[MetadataEvidence] = Field(default_factory=list, max_length=16)

    @field_validator('unit', 'frequency')
    @classmethod
    def printable_declaration(cls, value):
        if value is not None and (not value.strip() or any(ord(c) < 32 for c in value)):
            raise ValueError('Metadata declaration must contain printable source text')
        return value


def validate_field_metadata(fields):
    """Validate the additive subobject while retaining legacy catalogue fields."""
    for field in fields:
        if not isinstance(field, dict):
            raise ValueError('Catalogue field must be an object')
        if field.get('field_metadata') is None:
            continue
        metadata = FieldMetadata.model_validate(field['field_metadata'])
        for key in ('unit', 'frequency'):
            value = getattr(metadata, key)
            if value is not None and value != field.get(key):
                raise ValueError('Field metadata declaration disagrees with catalogue')

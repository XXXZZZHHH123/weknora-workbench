"""Optional, freshly authorized worksheet review hints for research pages."""
from urllib.parse import quote, urlencode

import httpx
from fastapi import HTTPException
from pydantic import Field, ValidationError

from tin.schemas.chart import ChartModel


class ReviewSummary(ChartModel):
    confirmed: int = Field(ge=0, strict=True)
    pending: int = Field(ge=0, strict=True)
    pending_fields: int = Field(ge=0, strict=True)
    pending_without_fields: int = Field(ge=0, strict=True)


class ReviewSheet(ChartModel):
    name: str
    status: str
    field_count: int = Field(ge=0, strict=True)


class ReviewSource(ChartModel):
    knowledge_id: str = Field(min_length=1, max_length=128)
    source_file: str
    sheets: list[ReviewSheet]
    summary: ReviewSummary


class SourceStatus(ChartModel):
    sources: list[ReviewSource]
    summary: ReviewSummary


def source_status_hint(scope=None):
    """Return only an optional hint; failures must not block research pages.

    The service selects current originals from its fresh signed source grant.
    Knowledge-base links are derived separately from our verified grant, never
    from service-provided URLs or caller-selected source identifiers.
    """
    from tin.integration.workbench_scope import current_workbench
    from tin.provenance.bridge import structured_request

    scope = scope or current_workbench.get()
    if (scope is None or scope.restricted or scope.scope_version != 2
            or scope.sources_revision < 1):
        return None
    token = current_workbench.set(scope)
    try:
        response = structured_request('POST', '/api/research/source-status', {},
                                      return_error_response=True, timeout=2)
        response.raise_for_status()
        status = SourceStatus.model_validate(response.json())
        originals = {}
        for source in scope.sources:
            if source['delegated']:
                return None
            for original in source['knowledge']:
                if original.get('parse_status') not in {'deleting', 'deleted'}:
                    originals.setdefault(original['id'], set()).add(source['knowledge_base_id'])
        seen, links = set(), []
        for row in status.sources:
            bases = originals.get(row.knowledge_id, set())
            if len(bases) != 1 or row.knowledge_id in seen:
                raise ValueError('Worksheet status does not match current original authorization')
            seen.add(row.knowledge_id)
            summary = row.summary
            # Some drawing-only sheets are intentionally excluded from pending
            # by the structured service; do not reclassify sheets here.
            open_sheets = [sheet for sheet in row.sheets if sheet.status not in {'confirmed', 'empty'}]
            if (summary.confirmed != sum(sheet.status == 'confirmed' for sheet in row.sheets)
                    or summary.pending > len(open_sheets)
                    or summary.pending_fields > sum(sheet.field_count for sheet in open_sheets)
                    or summary.pending_without_fields > min(
                        summary.pending, sum(not sheet.field_count for sheet in open_sheets))
                    or (summary.pending_fields and summary.pending_without_fields == summary.pending)):
                raise ValueError('Invalid worksheet review counts')
            # Sheets without any recognised field are reported at import, in the
            # knowledge base, not on research pages; only reviewable ones link here.
            if summary.pending > summary.pending_without_fields:
                kb_id = next(iter(bases))
                links.append({'knowledge_base_id': kb_id, 'knowledge_id': row.knowledge_id,
                              'source_file': row.source_file, 'pending': summary.pending,
                              'pending_fields': summary.pending_fields,
                              'url': '/platform/knowledge-bases/' + quote(kb_id, safe='')
                              + '/structured-data?' + urlencode({'mode': 'import',
                                                                 'source_knowledge_id': row.knowledge_id})})
        for key in ('confirmed', 'pending', 'pending_fields', 'pending_without_fields'):
            if getattr(status.summary, key) != sum(getattr(row.summary, key) for row in status.sources):
                raise ValueError('Worksheet status totals do not match source counts')
        pending, fields = status.summary.pending, status.summary.pending_fields
        reviewable = pending - status.summary.pending_without_fields
        if not reviewable:
            return None
        return {'pending': reviewable, 'pending_fields': fields, 'quiet': False,
                'message': f'知识库还有 {reviewable} 张工作表待确认（含 {fields} 个字段），确认后才能在这里使用',
                'links': links}
    except (HTTPException, httpx.HTTPError, ValidationError, ValueError, TypeError,
            AttributeError, KeyError, OSError, PermissionError):
        return None
    finally:
        current_workbench.reset(token)

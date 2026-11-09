"""One ordered, revisioned watchlist per verified actor and workbench."""
from datetime import datetime, timezone
import json
from typing import Literal
from uuid import NAMESPACE_URL, uuid5

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, Field, StrictInt, field_validator, model_validator
from sqlalchemy import update
from sqlalchemy.exc import IntegrityError

from tin.integration.page_facts import LEGACY_ID, SERIES
from tin.integration.workbench_scope import artifact_session
from tin.models.workbench import WorkbenchArtifact
from tin.web.chart_data import catalog_fields
from tin.web.charts import artifact_query, require_scope
from tin.web.workbench_artifacts import record_revision

router = APIRouter(prefix='/api/sn/favorites')


class FavoriteItem(BaseModel):
    model_config = ConfigDict(extra='forbid')
    kind: Literal['indicator', 'chart']
    id: str = Field(min_length=1, max_length=128, strict=True)
    added_at: datetime | None = None

    @field_validator('id')
    @classmethod
    def real_identifier(cls, value):
        if not LEGACY_ID.fullmatch(value):
            raise ValueError('自选需要真实指标或图表ID')
        return value

    @model_validator(mode='after')
    def indicator_identifier(self):
        if self.kind == 'indicator' and not SERIES.fullmatch(self.id):
            raise ValueError('自选需要当前来源目录的真实指标ID')
        return self


class FavoritesInput(BaseModel):
    model_config = ConfigDict(extra='forbid')
    format: Literal['favorites.v1'] = 'favorites.v1'
    revision: StrictInt = Field(ge=0)
    items: list[FavoriteItem] = Field(max_length=100)

    @model_validator(mode='after')
    def unique_items(self):
        if len({(item.kind, item.id) for item in self.items}) != len(self.items):
            raise ValueError('自选条目不能重复')
        return self


def owned_query(scope):
    # The stamp also isolates legacy result databases shared by workbenches.
    return artifact_query('favorites').where(WorkbenchArtifact.actor == scope.identity.user_id)


def stored_items(row):
    if row is None:
        return []
    try:
        return FavoritesInput.model_validate({**json.loads(row.body), 'revision': row.revision}).items
    except (ValueError, TypeError):
        raise HTTPException(409, '已保存自选格式无效，请核对后重试') from None


def availability(session, scope, items):
    fields = catalog_fields(scope, [item.id for item in items if item.kind == 'indicator'])
    chart_ids = [item.id for item in items if item.kind == 'chart']
    charts = {row.id: row for row in session.scalars(
        artifact_query().where(WorkbenchArtifact.id.in_(chart_ids)))} if chart_ids else {}
    return fields, charts


def serialize(row, items, fields, charts):
    result = []
    for item in items:
        field = fields.get(item.id) if item.kind == 'indicator' else charts.get(item.id)
        value = {**item.model_dump(mode='json'), 'status': 'active' if field is not None else 'invalid'}
        if item.kind == 'indicator' and field is not None:
            value.update(title=field.get('metric') or field.get('name') or item.id, unit=field.get('unit'))
        elif field is not None:
            value.update(title=field.title, chart_revision=field.revision)
        result.append(value)
    return {'format': 'favorites.v1', 'revision': row.revision if row else 0, 'items': result}


@router.get('')
def get_favorites():
    scope = require_scope()
    with artifact_session(scope) as session:
        row = session.scalar(owned_query(scope))
        items = stored_items(row)
        fields, charts = availability(session, scope, items)
        return serialize(row, items, fields, charts)


@router.put('')
def put_favorites(body: FavoritesInput):
    scope = require_scope()
    if not scope.identity.can_write:
        raise HTTPException(403, '当前空间角色只有读取权限')
    with artifact_session(scope) as session:
        row = session.scalar(owned_query(scope))
        if body.revision != (row.revision if row else 0):
            raise HTTPException(409, '账号自选已更新，请重新加载后再修改')
        previous = {(item.kind, item.id): item for item in stored_items(row)}
        fields, charts = availability(session, scope, body.items)
        now = datetime.now(timezone.utc)
        items = []
        for item in body.items:
            key = (item.kind, item.id)
            available = item.id in (fields if item.kind == 'indicator' else charts)
            # Existing invalid entries may be reordered or removed, never silently dropped.
            if not available and key not in previous:
                raise HTTPException(422, '自选指标不在当前目录，或图表不属于本研究台、已删除')
            items.append(FavoriteItem(kind=item.kind, id=item.id,
                added_at=previous[key].added_at if key in previous else now))
        text = json.dumps({'format': 'favorites.v1', 'items': [item.model_dump(mode='json') for item in items]},
                          ensure_ascii=False, separators=(',', ':'))
        if row is None:
            # A deterministic primary key fences concurrent first writes as well.
            identifier = str(uuid5(NAMESPACE_URL, json.dumps(['favorites.v1', scope.identity.tenant_id,
                scope.workbench_id, scope.identity.user_id], separators=(',', ':'))))
            row = WorkbenchArtifact(id=identifier, module='favorites', actor=scope.identity.user_id,
                title='我的自选', body=text, status='draft', source_ids=[], source_snapshots=[],
                revision=1, created_at=now, updated_at=now)
            session.add(row)
            try:
                session.flush()
            except IntegrityError:
                session.rollback()
                raise HTTPException(409, '账号自选已更新，请重新加载后再修改') from None
            from tin.web.research_references import remember_record_scope
            remember_record_scope(session, 'artifact', row.id)
        else:
            changed = session.execute(update(WorkbenchArtifact).where(WorkbenchArtifact.id == row.id,
                WorkbenchArtifact.actor == scope.identity.user_id, WorkbenchArtifact.module == 'favorites',
                WorkbenchArtifact.revision == body.revision).values(body=text,
                    revision=body.revision + 1, updated_at=now))
            if changed.rowcount != 1:
                raise HTTPException(409, '账号自选已更新，请重新加载后再修改')
            session.refresh(row)
        record_revision(session, row, scope.identity.user_id)
        session.commit()
        return serialize(row, items, fields, charts)

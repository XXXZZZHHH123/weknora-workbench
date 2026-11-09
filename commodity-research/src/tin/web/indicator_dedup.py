"""Explicit indicator correspondence review; metadata recall never confirms a pair."""
from typing import Literal

from fastapi import APIRouter, HTTPException
from pydantic import BaseModel, ConfigDict, Field, StrictBool, StrictInt

from tin.integration import indicator_dedup as service
from tin.integration.workbench_scope import current_workbench

router = APIRouter(prefix='/api/sn/indicator-dedup')


class SourceRevision(BaseModel):
    model_config = ConfigDict(extra='forbid')
    sources_revision: StrictInt = Field(ge=1)


class DecisionInput(SourceRevision):
    ids: list[str] = Field(min_length=1, max_length=20)
    revisions: dict[str, StrictInt]
    state: Literal['confirmed_default', 'keep_both', 'rejected']
    batch: StrictBool = False


class UndoInput(SourceRevision):
    revision: StrictInt = Field(ge=1)


def scope_for(body=None):
    scope = service.require_scope(current_workbench.get())
    if body is not None and body.sources_revision != scope.sources_revision:
        raise HTTPException(409, '研究台来源已变化，请刷新后核对')
    return scope


@router.get('')
def get_indicator_dedup():
    return service.list_mappings(scope_for())


@router.post('/refresh')
def refresh_indicator_dedup(body: SourceRevision):
    scope = scope_for(body)
    pending = [item for item in service.list_mappings(scope)['items'] if item['state'] == 'pending']
    pending.sort(key=lambda item: item['evidence'] is not None)
    for item in pending[:20]:
        service.verify_candidate(scope, item['id'])
    result = service.list_mappings(scope)
    result['remaining_unverified'] = sum(item['state'] == 'pending' and item['evidence'] is None for item in result['items'])
    return result


@router.post('/decisions')
def decide_indicator_dedup(body: DecisionInput):
    scope = scope_for(body)
    if set(body.revisions) != set(body.ids):
        raise HTTPException(422, '需要为每组候选提供当前修订版本')
    return service.decide(scope, body.ids, body.revisions, body.state, batch=body.batch)


@router.post('/{identifier}/verify')
def verify_indicator_dedup(identifier: str, body: SourceRevision):
    return service.verify_candidate(scope_for(body), identifier)


@router.post('/{identifier}/undo')
def undo_indicator_dedup(identifier: str, body: UndoInput):
    return service.undo(scope_for(body), identifier, body.revision)

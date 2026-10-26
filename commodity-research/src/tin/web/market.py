"""Market cards read the same verified catalog as the indicator workspace."""
from datetime import date as CalendarDate, datetime

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import BaseModel, ConfigDict, Field
from sqlalchemy import select

from tin.config import SHANGHAI
from tin.export.board import _obs_card
from tin.export.indicators import catalog
from tin.models import Indicator
from tin.integration.page_facts import page_read_scope


def market_catalog(session, cards, day):
    rows = catalog(session)
    available = {row['series_id'] for row in rows}
    from tin.integration.workbench_scope import current_workbench
    scope = current_workbench.get()
    from tin.web.market_history import history_config
    return {
        'indicators': [{key: row.get(key) or '' for key in
                        ('series_id', 'name', 'category', 'unit', 'source')} for row in rows],
        'defaults': [card.series_id for card in cards if card.series_id in available][:12],
        'date': day.isoformat() if day else None,
        'catalog_paged': bool(scope and scope.scope_version == 2),
        'can_edit_shared': bool(scope and scope.can_edit and not scope.restricted),
        'history': history_config(session),
    }


class CoreConfig(BaseModel):
    model_config = ConfigDict(extra='forbid')
    series_ids: list[str] = Field(max_length=12)


class HistoryConfig(BaseModel):
    model_config = ConfigDict(extra='forbid')
    series_ids: list[str] = Field(max_length=12)
    points_per_series: int = Field(default=180, ge=2, le=180, strict=True)


class ContractsConfig(BaseModel):
    model_config = ConfigDict(extra='forbid')
    contracts: list[dict] = Field(max_length=24)


def saved_core_cards(session, day, fallback):
    from tin.models import SourceMapping
    from tin.web.restricted_views import CORE_KEY, core_config, mapping_key
    from tin.web.market_card_labels import compact_market_cards
    if not session.info.get('workbench_id') or session.get(SourceMapping, mapping_key(session, CORE_KEY)) is None:
        return compact_market_cards(session, fallback)
    ids = core_config(session)
    selected = {row.series_id: row for row in session.scalars(select(Indicator).where(Indicator.series_id.in_(ids)))}
    cards = [_obs_card(session, identifier, selected[identifier].name, identifier, day)
             for identifier in ids if identifier in selected] if day else []
    return compact_market_cards(session, cards)


def build_market_router(session_factory, templates, resolve_date):
    router = APIRouter()

    @router.post('/api/sn/market/core/config')
    def save_core(body: CoreConfig):
        from tin.integration.workbench_scope import current_workbench, require_shared_write
        from tin.web.restricted_views import save_core_config
        scope = current_workbench.get()
        if scope is None or scope.restricted:
            raise HTTPException(403, '只有来源可读的研究台编辑成员可以配置共享核心指标')
        require_shared_write(scope)
        with page_read_scope('market',series_ids=body.series_ids), session_factory() as session:
            save_core_config(session, body.series_ids)
        return {'series_ids': body.series_ids}

    @router.post('/api/sn/market/history/config')
    def save_history(body: HistoryConfig):
        from tin.integration.workbench_scope import current_workbench, require_shared_write
        from tin.web.market_history import save_history_config
        scope = current_workbench.get()
        if scope is None or scope.restricted:
            raise HTTPException(403, '只有来源可读的研究台编辑成员可以配置历史行情')
        require_shared_write(scope)
        with page_read_scope('market',series_ids=body.series_ids), session_factory() as session:
            save_history_config(session, body.series_ids, body.points_per_series)
        return body.model_dump()

    @router.post('/api/sn/market/contracts/config')
    def save_contracts(body: ContractsConfig):
        from tin.integration.workbench_scope import current_workbench, require_shared_write
        from tin.web.market_contracts import save_contract_config
        scope = current_workbench.get()
        if scope is None or scope.restricted:
            raise HTTPException(403, '只有来源可读的研究台编辑成员可以配置合约来源')
        require_shared_write(scope)
        from tin.web.market_contracts import _validate, _identifiers
        identifiers = _identifiers(_validate(body.contracts))
        with page_read_scope('market',series_ids=identifiers), session_factory() as session:
            return save_contract_config(session, body.contracts)

    @router.get('/api/sn/market/core')
    def core(request: Request, series_id: list[str] = Query(..., min_length=1, max_length=12), date: str | None = None):
        if set(request.query_params) - {'series_id', 'date'} or len(request.query_params.getlist('date')) > 1:
            raise HTTPException(422, '只接受明确的指标编号和一个查询日期')
        if len(set(series_id)) != len(series_id) or any(not value or len(value) > 128 for value in series_id):
            raise HTTPException(422, '核心指标编号不能重复或为空，最多选择12项')
        if date is not None:
            try:
                if CalendarDate.fromisoformat(date).isoformat() != date:
                    raise ValueError
            except ValueError:
                raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
        with page_read_scope('market',series_ids=series_id,day=date), session_factory() as session:
            if session.info.get('knowledge_facts') is not True or not session.info.get('workbench_id'):
                raise HTTPException(409, '请从绑定知识库的研究工作台进入；未使用旧研究库事实')
            day = resolve_date(session, date) or datetime.now(SHANGHAI).date()
            # The verified projection is the catalog. Keep only the chosen
            # metadata objects alive so _obs_card reuses them via Session.get.
            chosen = {row.series_id: row for row in session.scalars(
                select(Indicator).where(Indicator.series_id.in_(series_id)))}
            if any(identifier not in chosen for identifier in series_id):
                raise HTTPException(404, '部分指标已不在当前知识库目录，请刷新后重新选择')
            cards = [_obs_card(session, identifier, chosen[identifier].name, identifier, day)
                     for identifier in series_id]
            from tin.web.market_card_labels import compact_market_cards
            cards = compact_market_cards(session, cards)
            html = templates.get_template('_market_cards.html').render(cards=cards)
            return {'series_ids': series_id, 'ready_count': sum(card.status == 'ok' for card in cards), 'html': html}

    return router

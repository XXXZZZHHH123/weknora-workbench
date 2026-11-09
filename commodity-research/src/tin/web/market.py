"""Market cards read the same verified catalog as the indicator workspace."""
from datetime import date as CalendarDate, datetime
from typing import Literal

from fastapi import APIRouter, HTTPException, Query, Request
from pydantic import BaseModel, ConfigDict, Field, model_validator
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
    from tin.web.restricted_views import core_items
    items = core_items(session)
    charts = _chart_records(session, [item['id'] for item in items if item['kind'] == 'chart'])
    return {
        'indicators': [{key: row.get(key) or '' for key in
                        ('series_id', 'name', 'category', 'unit', 'source')} for row in rows],
        'defaults': [card.series_id for card in cards if not isinstance(card, dict) and card.series_id in available][:16],
        'core_items': items,
        'charts': [{'id': row.id, 'title': row.title} for row in charts.values()],
        'date': day.isoformat() if day else None,
        'catalog_paged': bool(scope and scope.scope_version == 2),
        'can_edit_shared': bool(scope and scope.can_edit and not scope.restricted),
        'history': history_config(session),
    }


class CoreItem(BaseModel):
    model_config = ConfigDict(extra='forbid')
    kind: Literal['indicator', 'chart']
    id: str = Field(min_length=1, max_length=128, strict=True)


class CoreConfig(BaseModel):
    model_config = ConfigDict(extra='forbid')
    items: list[CoreItem] | None = Field(default=None, max_length=16)
    series_ids: list[str] | None = Field(default=None, max_length=16)

    @model_validator(mode='after')
    def one_format(self):
        if (self.items is None) == (self.series_ids is None):
            raise ValueError('请提供核心关注 items 或旧格式 series_ids 其中一种')
        return self

    def selection(self):
        from tin.web.restricted_views import _core_items
        return _core_items([item.model_dump() for item in self.items] if self.items is not None else
                           [{'kind': 'indicator', 'id': identifier} for identifier in self.series_ids])


class HistoryConfig(BaseModel):
    model_config = ConfigDict(extra='forbid')
    series_ids: list[str] = Field(max_length=12)
    points_per_series: int = Field(default=180, ge=2, le=180, strict=True)


class ContractsConfig(BaseModel):
    model_config = ConfigDict(extra='forbid')
    contracts: list[dict] = Field(max_length=24)


def _chart_records(session, identifiers):
    from tin.models.workbench import WorkbenchArtifact
    from tin.web.charts import artifact_query
    return {row.id: row for row in session.scalars(artifact_query().where(
        WorkbenchArtifact.id.in_(identifiers)))} if identifiers else {}


def _selected_cards(session, items, day, selected=None):
    """Project indicator values now; chart cards fetch their bounded data lazily.

    Callers that already loaded the chosen metadata pass it in, so one card
    request keeps a single indexed metadata read.
    """
    from tin.web.market_card_labels import compact_market_cards
    identifiers = [item['id'] for item in items if item['kind'] == 'indicator']
    if selected is None:
        selected = {row.series_id: row for row in session.scalars(select(Indicator).where(Indicator.series_id.in_(identifiers)))}
    indicators = compact_market_cards(session, [_obs_card(session, identifier, selected[identifier].name, identifier, day)
        for identifier in identifiers if identifier in selected]) if day else []
    cards = {card.series_id: card for card in indicators}
    charts = _chart_records(session, [item['id'] for item in items if item['kind'] == 'chart'])
    result = []
    for item in items:
        if item['kind'] == 'chart':
            chart = charts.get(item['id'])
            result.append({'kind': 'chart', 'id': item['id'], 'title': chart.title if chart else '已失效图表',
                           'status': 'pending' if chart else 'missing'})
        elif item['id'] in cards:
            result.append(cards[item['id']])
    return result


def saved_core_cards(session, day, fallback):
    from tin.models import SourceMapping
    from tin.web.restricted_views import CORE_KEY, core_items, mapping_key
    from tin.web.market_card_labels import compact_market_cards
    if not session.info.get('workbench_id'):
        return compact_market_cards(session, fallback)
    if session.get(SourceMapping, mapping_key(session, CORE_KEY)) is None:
        return []
    return _selected_cards(session, core_items(session), day)


def build_market_router(session_factory, templates, resolve_date):
    router = APIRouter()

    @router.post('/api/sn/market/core/config')
    def save_core(body: CoreConfig):
        from tin.integration.workbench_scope import current_workbench, require_shared_write
        from tin.web.restricted_views import save_core_config
        scope = current_workbench.get()
        if scope is None or scope.restricted:
            raise HTTPException(403, '只有来源可读的研究台编辑成员可以配置共享核心关注')
        require_shared_write(scope)
        items = body.selection()
        identifiers = [item['id'] for item in items if item['kind'] == 'indicator']
        with page_read_scope('market',series_ids=identifiers), session_factory() as session:
            save_core_config(session, items)
        return {'items': items, 'series_ids': identifiers}

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
    def core(request: Request, series_id: list[str] | None = Query(None, max_length=16),
             item: list[str] | None = Query(None, max_length=16), date: str | None = None):
        if set(request.query_params) - {'series_id', 'item', 'date'} or len(request.query_params.getlist('date')) > 1:
            raise HTTPException(422, '只接受明确的核心关注条目和一个查询日期')
        if (series_id is None) == (item is None):
            raise HTTPException(422, '请提供一种核心关注条目格式')
        from tin.web.restricted_views import _core_items
        if item is not None:
            if any(':' not in value for value in item):
                raise HTTPException(422, '核心关注条目需要 kind:id')
            items = [{'kind': value.split(':', 1)[0], 'id': value.split(':', 1)[1]} for value in item]
        else:
            items = [{'kind': 'indicator', 'id': identifier} for identifier in series_id]
        try:
            items = _core_items(items)
        except HTTPException as error:
            raise HTTPException(422, error.detail) from None
        series_id = [value['id'] for value in items if value['kind'] == 'indicator']
        if date is not None:
            try:
                if CalendarDate.fromisoformat(date).isoformat() != date:
                    raise ValueError
            except ValueError:
                raise HTTPException(400, '日期格式应为 YYYY-MM-DD') from None
        with page_read_scope('market',series_ids=series_id,day=date), session_factory() as session:
            if session.info.get('knowledge_facts') is not True or not session.info.get('workbench_id'):
                raise HTTPException(409, '请从绑定知识库的研究工作台进入；未使用旧研究库事实')
            day = ((resolve_date(session, date) if series_id else CalendarDate.fromisoformat(date) if date else None)
                   or datetime.now(SHANGHAI).date())
            # The verified projection is the catalog. Keep only the chosen
            # metadata objects alive so _obs_card reuses them via Session.get.
            chosen = {row.series_id: row for row in session.scalars(
                select(Indicator).where(Indicator.series_id.in_(series_id)))}
            if any(identifier not in chosen for identifier in series_id):
                raise HTTPException(404, '部分指标已不在当前知识库目录，请刷新后重新选择')
            chart_ids = [value['id'] for value in items if value['kind'] == 'chart']
            if set(chart_ids) != set(_chart_records(session, chart_ids)):
                raise HTTPException(404, '部分图表不属于本研究台或已删除，请刷新后重新选择')
            cards = _selected_cards(session, items, day, selected=chosen)
            html = templates.get_template('_market_cards.html').render(cards=cards)
            return {'items': items, 'series_ids': series_id,
                    'ready_count': sum(not isinstance(card, dict) and card.status == 'ok' for card in cards), 'html': html}

    return router

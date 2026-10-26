"""Synthetic product specifications; no commodity-specific routing."""
from datetime import timedelta

import pytest
from sqlalchemy import select

from tin.models import Indicator, SourceMapping
from tin.schemas.strategy import StrategyPlan
from tin.strategy.generate import build_context, validate_evidence
from tin.strategy.service import _realized, _execution, StrategyError
from tin.schemas.strategy import ExecutionIn
from tin.web.market_contracts import CONTRACTS_KEY, _validate
from tin.web.restricted_views import mapping_key
from tests.test_strategy_platform import scoped_strategy, candidate, QUOTE, DAY, STAMP


def configure(session, judgment):
    judgment.variety = 'XY'
    row = session.get(SourceMapping, mapping_key(session, CONTRACTS_KEY))
    row.config = {'contracts': [{'contract': 'XY611', 'delivery_month': '2026-11',
        'close': QUOTE, 'settle': QUOTE, 'price_unit': '元/吨', 'variety': 'XY',
        'multiplier': 5, 'tick_size': 5, 'spec_source': 'https://exchange.example/specifications'}]}
    for indicator in session.scalars(select(Indicator)):
        indicator.variety = ''
    session.commit()


def test_explicit_product_contract_specs_are_context_and_model_gates(scoped_strategy):
    session, researcher, judgment = scoped_strategy
    configure(session, judgment)
    context, gaps = build_context(session, 'XY', DAY, researcher_id=researcher)
    assert gaps == [], gaps
    contract = context['available_contracts'][0]
    assert contract['contract'] == 'XY611' and contract['multiplier'] == 5 and contract['tick_size'] == 5
    data = candidate({**context, 'market_as_of': STAMP.isoformat()}).model_dump(mode='json')
    data.update(variety='XY')
    data['legs'][0].update(contract='XY611', multiplier=5, tick_size=5, delivery_month='2026-11')
    data['entry'].update(low=100, high=105)
    parsed = StrategyPlan.model_validate(data)
    assert validate_evidence(parsed, context) == []
    parsed.legs[0].multiplier = 1
    assert any('规格' in item or '乘数' in item for item in validate_evidence(parsed, context))


def test_undefined_product_execution_specs_do_not_inherit_legacy_multiplier(scoped_strategy):
    session, researcher, judgment = scoped_strategy
    configure(session, judgment)
    row = session.get(SourceMapping, mapping_key(session, CONTRACTS_KEY))
    row.config = {'contracts': [{key: value for key, value in row.config['contracts'][0].items()
                                if key not in {'multiplier', 'tick_size', 'spec_source', 'variety'}}]}
    session.commit()
    context, gaps = build_context(session, 'XY', DAY, researcher_id=researcher)
    assert context['available_contracts'] == [] and any('规格' in gap for gap in gaps)


def test_contract_specs_are_explicit_complete_and_finite():
    item = {'contract': 'XY611', 'delivery_month': '2026-11', 'settle': QUOTE,
            'price_unit': '元/吨', 'variety': 'XY', 'multiplier': 5, 'tick_size': 5,
            'spec_source': 'https://exchange.example/specifications'}
    assert _validate([item])[0]['multiplier'] == 5
    for key, value in [('multiplier', True), ('tick_size', float('nan')), ('spec_source', '')]:
        with pytest.raises(Exception):
            _validate([{**item, key: value}])


def test_execution_and_realized_accounting_use_saved_contract_specs():
    from types import SimpleNamespace
    from tests.test_research_ownership import plan
    data = plan().model_dump(mode='json')
    data['variety'] = 'XY'
    data['legs'][0].update(contract='XY611', quote_series_id=QUOTE, multiplier=5, tick_size=5)
    parsed = StrategyPlan.model_validate(data)
    stamp = parsed.as_of + timedelta(seconds=1)
    entered = {'contract': 'XY611', 'price': 100, 'quantity': 2, 'filled_at': stamp.isoformat(), 'costs': 3}
    exited = {**entered, 'price': 105}
    row = SimpleNamespace(plan=data, entry_fills=[entered], exit_fills=[exited])
    assert _realized(row) == 44
    invalid = ExecutionIn(fills=[{**entered, 'price': 101}], reason='Synthetic fill')
    with pytest.raises(StrategyError, match='最小变动价位'):
        _execution(parsed, invalid, opening=True)

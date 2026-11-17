"""A dimension edit carries the authorized source version's frozen charts unchanged."""
from copy import deepcopy
import json

from sqlalchemy import select

from tests.test_judgment_library_references import client, chart_client, payload
from tests.test_chart_references import create
from tests.test_workbench_sources_v2 import WB
from tin.integration.workbench_scope import isolated_session_factory
from tin.judgments.service import to_payload
from tin.models import AuditLog, Judgment
from tin.web import judgment_dimensions


def test_dimension_only_version_keeps_every_other_payload_field_and_frozen_chart_snapshot(client):
    browser, state = client
    state.app.include_router(judgment_dimensions.router)
    chart = create(browser)
    reference = {'kind': 'chart', 'id': chart['id'], 'revision': 1,
                 'target': {'kind': 'evidence', 'side': 'bull', 'index': 0}}
    seeded = browser.post('/seed-judgment', json=payload([reference]))
    assert seeded.status_code == 200, seeded.text
    original = seeded.json()
    with isolated_session_factory(17, WB)() as session:
        row = session.get(Judgment, original['id'])
        before = to_payload(row).model_dump(mode='json', by_alias=True)
        snapshot = deepcopy(row.contradiction['chart_references'])
    # Removing the live chart must not cause the dimension save to refreeze it.
    assert browser.delete('/api/sn/charts/' + chart['id'] + '?revision=1').status_code == 200
    response = browser.post('/api/sn/judgment/dimensions', json={
        'judgment_id': original['id'], 'base_version': original['version'], 'items': [
            {'section': 'supporting', 'index': 0, 'dimension': 'supply'},
            {'section': 'thresholds', 'index': 0, 'dimension': 'price'}]})
    assert response.status_code == 200, response.text
    result = response.json()
    assert result['version'] == 2 and result['id'] != original['id'] and result['status'] == '草稿'
    with isolated_session_factory(17, WB)() as session:
        saved = session.get(Judgment, result['id'])
        after = to_payload(saved).model_dump(mode='json', by_alias=True)
        assert json.dumps(saved.contradiction['chart_references'], sort_keys=True, ensure_ascii=False) == json.dumps(snapshot, sort_keys=True, ensure_ascii=False)
        assert after['contradiction']['bull']['evidence'][0].pop('dimension') == 'supply'
        assert after['thresholds'][0].pop('dimension') == 'price'
        assert after == before
        assert to_payload(session.get(Judgment, original['id'])).model_dump(mode='json', by_alias=True) == before
        audit = session.scalar(select(AuditLog).where(AuditLog.action == '研判维度保存'))
        assert audit.actor == 'first' and audit.target_id == str(result['id'])
        assert audit.detail['source_id'] == original['id'] and audit.detail['source_version'] == 1

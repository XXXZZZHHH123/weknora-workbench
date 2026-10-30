"""Human caliber choices belong to one rule/version, never change source metadata."""
import copy
from datetime import date
from types import SimpleNamespace

import pytest
from sqlalchemy import select

from tests.test_judgment_generation import generator, generate, apply, rows, document, TEXT
from tests.test_judgment_review import update, pick, all_items
from tests.test_knowledge_facts import canonical, SERIES, FIRST
from tests.test_moving_average import facts
from tin.integration.workbench_scope import isolated_session_factory
from tin.judgments.analysis import AnalysisError, validate_analysis
from tin.models import AuditLog
from tin.web import judgment_generation as generation


def test_wrong_model_unit_can_explicitly_adopt_catalog_without_switching_indicator(generator):
    client, headers, _, response, _ = generator
    response['binding_units'][SERIES] = 'kg'
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    assert item['caliber']['can_adopt_catalog'] and item['caliber']['catalog_unit'] == '吨'
    changed = update(client, headers, preview, item, caliber={'mode': 'catalog'})
    assert changed.status_code == 200, changed.text
    confirmed = pick(changed.json(), 'thresholds')
    assert confirmed['status'] == 'confirmed' and confirmed['binding']['series_id'] == SERIES
    assert confirmed['caliber']['mode'] == 'catalog'
    assert apply(client, headers, changed.json()).status_code == 200
    with isolated_session_factory(17, FIRST)() as session:
        audit = session.scalar(select(AuditLog).where(AuditLog.action == '研判逐项确认'))
        assert audit.detail['caliber']['mode'] == 'catalog' and audit.detail['caliber']['unit'] == '吨'


def missing_catalog(monkeypatch, unit=None):
    original = generation.snapshot
    def missing(*args, **kwargs):
        value = original(*args, **kwargs); value['catalog'] = copy.deepcopy(value['catalog'])
        value['catalog'][0]['unit'] = unit
        return value
    monkeypatch.setattr(generation, 'snapshot', missing)


@pytest.mark.parametrize('placeholder', ['—', '未注明'])
def test_missing_unit_placeholders_offer_the_same_local_confirmation(generator, monkeypatch, placeholder):
    client, headers, _, response, _ = generator
    missing_catalog(monkeypatch, placeholder)
    response['binding_units'][SERIES] = placeholder
    preview = generate(client, headers).json()
    item = pick(preview, 'thresholds')
    assert item['caliber']['can_declare'] and not item['caliber']['can_adopt_catalog']
    changed = update(client, headers, preview, item,
        caliber={'mode': 'declared', 'unit': '吨', 'basis': '已核对来源原表单位'})
    assert changed.status_code == 200, changed.text
    assert apply(client, headers, changed.json()).status_code == 200


def test_missing_unit_can_be_declared_with_basis_scoped_to_this_rule_and_version(generator, monkeypatch):
    client, headers, _, response, _ = generator
    missing_catalog(monkeypatch); response['binding_units'][SERIES] = None
    preview = generate(client, headers).json(); item = pick(preview, 'thresholds')
    assert item['caliber']['can_declare'] and item['caliber']['catalog_unit'] is None
    changed = update(client, headers, preview, item, caliber={'mode': 'declared', 'unit': '吨',
        'basis': '原始表头数量（吨），已人工核对', 'scope_note': '总量，原表口径'})
    assert changed.status_code == 200, changed.text
    data = changed.json(); confirmed = pick(data, 'thresholds')
    assert confirmed['status'] == 'confirmed' and confirmed['binding']['unit'] == '吨'
    assert confirmed['caliber']['catalog_unit'] is None
    assert apply(client, headers, data).status_code == 200
    saved = rows()[0].thresholds[0]['caliber_confirmation']
    assert saved['unit'] == '吨' and saved['confirmed_by'] == 'researcher'
    assert saved['series_id'] == SERIES and saved['basis']
    later = generate(client, headers, base=1).json()
    assert pick(later, 'thresholds')['status'] == 'pending'
    assert pick(later, 'thresholds')['caliber']['mode'] is None


@pytest.mark.parametrize('caliber', [
    {'mode': 'declared', 'unit': 'kg', 'basis': '人工核对'},
    {'mode': 'declared', 'unit': '吨', 'basis': ''},
])
def test_manual_caliber_does_not_overwrite_known_unit_or_allow_empty_basis(generator, caliber):
    client, headers, _, _, _ = generator
    preview = generate(client, headers).json()
    response = update(client, headers, preview, pick(preview, 'thresholds'), caliber=caliber)
    assert response.status_code == 422 and rows() == []


def test_declared_caliber_does_not_bypass_original_number_precision_or_current_revision(generator, monkeypatch):
    client, headers, _, response, _ = generator
    missing_catalog(monkeypatch); response['binding_units'][SERIES] = None
    response['judgment']['thresholds'][0]['value'] = 99999
    preview = generate(client, headers).json()
    result = update(client, headers, preview, pick(preview, 'thresholds'), caliber={
        'mode': 'declared', 'unit': '吨', 'basis': '已核对原表'})
    assert result.status_code == 422 and rows() == []


def declaration(unit='吨', sid=SERIES):
    return {'series_id': sid, 'unit': unit, 'basis': '已核对原始数据表头', 'scope_note': '原表总量，未换算',
        'confirmed_by': 'researcher', 'confirmed_at': '2032-01-02T03:04:05+00:00', 'catalog_version': 'synthetic-version'}


def test_model_or_import_document_cannot_forge_a_human_declaration():
    body = document(); body['judgment']['thresholds'][0]['caliber_confirmation'] = declaration()
    with pytest.raises(AnalysisError) as caught:
        validate_analysis(body, TEXT, [{'id': SERIES, 'unit': None}], variety='TEST', actor='researcher')
    assert caught.value.issues[0]['code'] == 'unit_confirmation_authority'


def test_explicit_declaration_is_checked_against_exact_original_numbers_and_its_own_binding():
    body = document(); confirmed = declaration()
    body['judgment']['thresholds'][0]['caliber_confirmation'] = confirmed
    parsed = validate_analysis(body, TEXT, [{'id': SERIES, 'unit': None}], variety='TEST', actor='researcher',
        caliber_confirmations={'watch': confirmed})
    assert parsed.payload.thresholds[0].caliber_confirmation.unit == '吨'
    for damage in ['number', 'other_user', 'other_series', 'real_conflicting_unit']:
        candidate = copy.deepcopy(body); catalog = [{'id': SERIES, 'unit': None}]
        if damage == 'number': candidate['judgment']['thresholds'][0]['value'] = 99999
        elif damage == 'other_user': candidate['judgment']['thresholds'][0]['caliber_confirmation']['confirmed_by'] = 'another-user'
        elif damage == 'other_series': candidate['judgment']['thresholds'][0]['caliber_confirmation']['series_id'] = 'foreign'
        else: catalog[0]['unit'] = 'kg'
        trusted = {'watch': candidate['judgment']['thresholds'][0]['caliber_confirmation']}
        with pytest.raises(AnalysisError):
            validate_analysis(candidate, TEXT, catalog, variety='TEST', actor='researcher', caliber_confirmations=trusted)


def test_one_binding_declaration_cannot_confirm_another_rule_or_conflicting_unit():
    body = document(); confirmed = declaration()
    body['judgment']['thresholds'][0]['caliber_confirmation'] = confirmed
    second = {**copy.deepcopy(body['judgment']['thresholds'][0]), 'id': 'second'}
    body['judgment']['thresholds'].append(second); body['rule_quotes']['second'] = body['rule_quotes']['watch']
    for conflict in [False, True]:
        second.pop('caliber_confirmation', None)
        if conflict: second['caliber_confirmation'] = declaration('kg')
        trusted = {'watch': confirmed, **({'second': second['caliber_confirmation']} if conflict else {})}
        with pytest.raises(AnalysisError) as caught:
            validate_analysis(body, TEXT, [{'id': SERIES, 'unit': None}], variety='TEST', actor='researcher',
                caliber_confirmations=trusted)
        assert caught.value.issues[0]['code'] == ('unit_confirmation_conflict' if conflict else 'unit_metadata_missing')


def test_rule_local_radar_can_use_declared_unit_without_changing_source_or_other_rule():
    from tin.export.board import threshold_radar_values
    candidate = document()['judgment']['thresholds'][0]
    declared = {**candidate, 'caliber_confirmation': declaration()}
    values = {SERIES: {'name': '合成数量', 'unit': None, 'value': 13, 'date': '2032-01-02',
        'points': [{'value': 13, 'unit': None}]}}
    original = copy.deepcopy(values)
    rows = threshold_radar_values(SimpleNamespace(thresholds=[declared, {**candidate, 'id': 'unconfirmed'}]), values)
    assert rows[0]['state'] == '已触发' and rows[0]['unit'] == '吨'
    assert rows[1]['state'] == '数据缺失' and rows[1]['progress'] == 0
    assert values == original
    values[SERIES]['points'][0]['unit'] = 'kg'
    assert threshold_radar_values(SimpleNamespace(thresholds=[declared]), values)[0]['state'] == '数据缺失'


def test_manual_unit_metadata_is_not_in_model_output_schema():
    from jsonschema import Draft202012Validator
    from tin.judgments.output_schema import OUTPUT_SCHEMA
    body = document(); body['judgment']['thresholds'][0]['caliber_confirmation'] = declaration()
    assert not Draft202012Validator(OUTPUT_SCHEMA).is_valid(body)


def test_daily_ma_preview_can_save_with_original_frequency_metadata(generator):
    from tests.test_judgment_generation import endpoint
    client, headers, _, response, _ = generator
    source = '合成数量下穿2日均线时复核'
    response.clear(); response.update({'judgment': {'contradiction': {}, 'thresholds': [{
        'id': 'daily_cross', 'name': '均线事件', 'series_id': SERIES, 'operator': 'crosses_below',
        'value': {'kind': 'sma', 'window': 2}, 'type': '观察位', 'horizon': '短期'}]},
        'binding_units': {SERIES: '吨'}, 'rule_quotes': {'daily_cross': source}, 'unresolved': []})
    preview = client.post(endpoint('/generate'), headers=headers, json={
        'model_id': 'synthetic-chat', 'text': source, 'base_version': 0})
    assert preview.status_code == 200, preview.text
    saved = apply(client, headers, preview.json())
    assert saved.status_code == 200, saved.text
    assert rows()[0].thresholds[0]['operator'] == 'crosses_below'


def test_declared_unit_preserves_missing_observations_for_sma_and_raw_catalog():
    from tin.export.board import threshold_radar_values
    candidate = {**document()['judgment']['thresholds'][0], 'operator': '<',
        'value': {'kind': 'sma', 'window': 2}, 'caliber_confirmation': declaration()}
    values = {SERIES: {'name': '合成数量', 'unit': None, 'value': 2, 'date': '2032-01-02',
        'points': [{'value': 4, 'unit': None}, {'value': 2, 'unit': None}]}}
    original = copy.deepcopy(values)
    result = threshold_radar_values(SimpleNamespace(thresholds=[candidate]), values)[0]
    assert result['state'] == '已触发' and result['target_value'] == 3 and result['unit'] == '吨'
    assert values == original
    values[SERIES]['points'][0]['value'] = None
    assert threshold_radar_values(SimpleNamespace(thresholds=[candidate]), values)[0]['state'] == '数据缺失'


def test_manual_falsifier_unit_is_scoped_to_that_rule_and_fails_if_real_unit_changes(facts):
    from tests.test_moving_average import SID as LOCAL_SID, DAY, point
    from tin.compute.signals import _falsifier_signal, _transient_signals
    from tin.models import Indicator
    indicator = facts.get(Indicator, LOCAL_SID); indicator.unit = ''; facts.commit()
    point(facts, 1, 13, unit=None)
    candidate = {'id': 'quantity_check', 'desc': '数量复核', 'series_id': LOCAL_SID, 'expression': 'value > 12',
        'unit_confirmed': '吨', 'consecutive_periods': 1, 'side': 'bull',
        'caliber_confirmation': declaration(sid=LOCAL_SID)}
    marker = _transient_signals.set(True)
    try:
        row = _falsifier_signal(facts, SimpleNamespace(id=1), candidate, DAY)
        assert row.state == '触发' and row.current_value == 13
        assert indicator.unit == '', 'The declaration never writes source metadata'
        plain = {key: value for key, value in candidate.items() if key != 'caliber_confirmation'}
        assert _falsifier_signal(facts, SimpleNamespace(id=1), plain, DAY).state == '数据缺失'
        indicator.unit = 'kg'; facts.commit()
        assert _falsifier_signal(facts, SimpleNamespace(id=1), candidate, DAY).state == '数据缺失'
    finally:
        _transient_signals.reset(marker)


def test_save_service_rejects_imported_or_different_actor_human_confirmation(facts):
    from tests.test_moving_average import SID as LOCAL_SID
    from tin.judgments.service import save_version, JudgmentError
    from tin.schemas.judgment import JudgmentIn
    confirmation = declaration(sid=LOCAL_SID)
    candidate = {**document()['judgment']['thresholds'][0], 'series_id': LOCAL_SID,
        'caliber_confirmation': confirmation}
    payload = JudgmentIn(variety='TEST', author='researcher', written_at=date(2032,1,2),
        contradiction={}, thresholds=[candidate])
    with pytest.raises(JudgmentError):
        save_version(facts, payload, actor='researcher')
    with pytest.raises(JudgmentError):
        save_version(facts, payload, actor='another-user', caliber_confirmations={'watch': confirmation})

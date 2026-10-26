"""Readable report text keeps original values and real source identities."""
from copy import deepcopy
from datetime import date

import pytest

from tin.models import Indicator
from tests.test_report_period import _observation


@pytest.mark.parametrize('first,last,expected', [(0.1,0.3,0.2), (3.246,2.498,-0.748)])
def test_report_changes_use_decimal_input_values_without_binary_subtraction_noise(session, first, last, expected):
    from tin.web.report_period import build_period
    from tin.web.report_view import present_snapshot, readable_markdown
    identifier = 'fixture.decimal'
    session.add(Indicator(series_id=identifier, name='样例指标', variety='SN', category='测试', caliber={},
                          unit='吨', source='fixture', frequency='日', fetch_mode='readonly'))
    session.flush()
    _observation(session, identifier, '2026-01-05T15:00', first)
    _observation(session, identifier, '2026-01-09T15:00', last)
    session.commit()
    source = {'as_of':'2026-01-09','observations':[{'series_id':identifier,'name':'样例指标',
        'value':last,'as_of':'2026-01-09T15:00:00+08:00','unit':'吨'}]}
    original = deepcopy(source)
    payload = build_period(session,'SN',date(2026,1,9),'weekly',source=source)
    assert payload['changes'][0]['change'] == expected
    assert payload['changes'][0]['first']['value'] == first
    assert payload['changes'][0]['last']['value'] == last
    if first == 0.1:
        assert payload['changes'][0]['change_pct'] == 200
    view = present_snapshot(payload)
    assert view['changes'][0]['change_text'] == str(expected)
    text = readable_markdown(payload)
    assert ' | ' + str(expected) + ' | ' in text
    assert source == original


def _saved_judgment_payload():
    identifier = 'kb-'+'e'*48
    return {'as_of':'2026-01-09','observations':[{'series_id':identifier,'name':'已确认指标名称',
        'value':0.3,'unit':'吨','as_of':'2026-01-09','source':'原件'}],
        'judgment':{'id':1,'version':2,'status':'草稿','tone':'区间震荡',
            'tone_note':'这是一段已保存的研判说明，用于核对假设。',
            'contradiction':{'bull':{'claim':'支持论点','evidence':[{'desc':'已保存支持依据','series_id':identifier}]},
                             'bear':{'claim':'反对论点','evidence':[]}},
            'thresholds':[{'id':'threshold','name':'观察区间','series_id':identifier,'operator':'between','value':[0.1,0.4]}],
            'falsifiers':[]},'briefs':[]}


def test_report_without_generated_brief_replays_saved_tone_and_note_as_saved_research():
    from tin.web.report_view import present_snapshot, readable_markdown
    payload = _saved_judgment_payload()
    original = deepcopy(payload)
    view = present_snapshot(payload)
    assert view['judgment_replay']['tone'] == '区间震荡'
    assert view['judgment_replay']['tone_note'] == payload['judgment']['tone_note']
    text = readable_markdown(payload).split('## 主要变化')[0]
    assert '已保存研判回放' in text
    assert payload['judgment']['tone_note'] in text
    assert '尚无已保存研究结论' not in text
    assert payload == original


def test_source_observation_names_are_used_in_main_claims_and_rules_without_losing_ids():
    from tin.web.report_view import present_snapshot, readable_markdown
    payload = _saved_judgment_payload()
    identifier = payload['observations'][0]['series_id']
    payload['briefs'] = [{'trade_date':'2026-01-09','status':'草稿','summary':'已保存日报正文',
        'claims':[{'text':'已保存论断','evidence':[{'series_id':identifier,'value':0.3,'as_of':'2026-01-09'}]}]}]
    payload['rule_preview'] = {'label':'当前草稿规则试算','notice':'核对实际数据时点',
        'signals':[{'rule_id':'threshold','state':'未触发','current_value':0.5120000000000005}]}
    view = present_snapshot(payload)
    assert view['judgment']['cases'][0]['evidence'][0]['name'] == '已确认指标名称'
    assert view['judgment']['thresholds'][0]['series_name'] == '已确认指标名称'
    assert view['briefs'][0]['claims'][0]['evidence'][0]['name'] == '已确认指标名称'
    assert view['judgment']['thresholds'][0]['current_value_text'] == '0.512'
    assert view['judgment']['thresholds'][0]['current_value'] == 0.5120000000000005
    assert payload['rule_preview']['signals'][0]['current_value'] == 0.5120000000000005
    text = readable_markdown(payload)
    main, sources = text.split('## 数据缺口与来源',1)
    assert identifier not in main
    assert '已确认指标名称' in main
    assert identifier in sources


def test_html_and_markdown_show_the_same_saved_judgment_fallback(loaded, monkeypatch, tmp_path):
    import json
    from fastapi.testclient import TestClient
    from tin.config import settings
    from tin.web import app as web
    payload = _saved_judgment_payload()
    payload['as_of'] = '2026-09-18'
    payload['generated_at'] = '2026-09-18T16:00:00+08:00'
    payload['rule_preview'] = {'label':'当前草稿规则试算','notice':'核对实际数据时点',
        'signals':[{'rule_id':'threshold','state':'未触发','current_value':0.5120000000000005}]}
    monkeypatch.setattr(settings,'weknora_api_url','')
    monkeypatch.setattr(settings,'exports_dir',tmp_path)
    monkeypatch.setattr(web,'SessionLocal',lambda:loaded)
    folder=tmp_path/web.V
    folder.mkdir()
    (folder/'2026-09-18.json').write_text(json.dumps(payload))
    monkeypatch.setattr(web,'archive_days',lambda *_:[{'date':'2026-09-18','weekday':'五',
        'tone':None,'main':None,'gaps':0,'generated_at':payload['generated_at']}])
    response=TestClient(web.app).get('/sn/reports')
    assert response.status_code == 200
    top=response.text.split('本周主要变化')[0].split('主要变化')[0]
    assert '已保存研判回放' in top and payload['judgment']['tone_note'] in top
    assert '暂无已保存研究结论' not in top
    main, audit = response.text.split('原始 JSON',1)
    assert '试算值 0.512</small>' in main
    assert '0.5120000000000005' not in main
    assert '0.5120000000000005' in audit

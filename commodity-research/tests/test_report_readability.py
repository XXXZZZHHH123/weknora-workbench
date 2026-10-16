"""Reports present stored facts without inventing narrative or frozen history."""
from tests.test_isolated_workbenches import isolated, confirm_kb_source
from tests.test_workbench_shared_ui import login, page


def test_integrated_report_is_readable_and_truthful_about_current_source(isolated):
    client, headers, state, _ = isolated
    field = confirm_kb_source(state, [{'date': '2026-01-02', 'value': 0,
        'value_status': 'valid', 'source_cell': 'B3', 'date_cell': 'A3'}])
    login(client, headers)
    response = client.get(page('/sn/reports'))
    assert response.status_code == 200
    assert 'id="report-readable"' in response.text
    assert 'Quantity' in response.text and field in response.text
    assert '截至所选日期的最新指标' in response.text
    assert '当前已确认数据' in response.text
    assert '并非历史冻结归档' in response.text
    assert '原始 JSON' in response.text and '<details' in response.text
    assert '已冻结' not in response.text and '份归档' not in response.text
    assert '未登记研究判断' in response.text
    assert '<td class="report-value">0.0</td>' in response.text


def test_report_presenter_keeps_null_and_zero_and_never_mutates_source():
    import copy
    from tin.web.report_view import present_snapshot
    payload = {'as_of':'2026-02-03', 'observations':[
        {'series_id':'a', 'value':0, 'unit':'件', 'as_of':'2026-02-02', 'source':'原件',
         'caliber': {'note':'原口径'}},
        {'series_id':'b', 'value':None, 'as_of':'2026-02-03'}],
        'derived':[{'formula_id':'f', 'value':None, 'status':'blocked', 'note':'缺输入'}],
        'judgment':None, 'signals':[], 'gaps':[], 'surveys':[]}
    before=copy.deepcopy(payload)
    result=present_snapshot(payload, {'a':'真实名称 <script>'})
    assert result['observations'][0]['name']=='真实名称 <script>'
    assert result['observations'][0]['value']==0
    assert result['observations'][1]['value'] is None
    assert result['derived'][0]['note']=='缺输入'
    assert result['counts']=={'observations':2, 'derived':1, 'signals':0, 'gaps':0}
    assert payload==before


def test_frozen_archive_uses_its_actual_payload_and_remains_escaped(loaded, monkeypatch, tmp_path):
    import json
    from tin.config import settings
    from tin.web import app as web
    monkeypatch.setattr(settings,'weknora_api_url','')
    monkeypatch.setattr(settings,'exports_dir',tmp_path)
    monkeypatch.setattr(web,'SessionLocal',lambda:loaded)
    folder=tmp_path/web.V
    folder.mkdir()
    payload={'as_of':'2026-09-18','generated_at':'2026-09-18T16:00:00+08:00',
      'observations':[{'series_id':'stored-id','name':'<script>alert(1)</script>',
        'value':0,'unit':'件','source':'原件','as_of':'2026-09-18'}],
      'judgment':None,'derived':[],'signals':[],'gaps':[],'surveys':[]}
    (folder/'2026-09-18.json').write_text(json.dumps(payload))
    monkeypatch.setattr(web,'archive_days',lambda *_:[{'date':'2026-09-18','weekday':'五',
        'tone':None,'main':None,'gaps':0,'generated_at':payload['generated_at']}])
    from fastapi.testclient import TestClient
    response=TestClient(web.app).get('/sn/reports')
    assert response.status_code==200
    assert '历史冻结归档' in response.text
    assert '&lt;script&gt;alert(1)&lt;/script&gt;' in response.text
    assert '<script>alert(1)</script>' not in response.text
    assert 'stored-id' in response.text

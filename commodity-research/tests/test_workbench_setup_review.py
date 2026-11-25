"""The setup review loads candidates without writing a workbench product."""
import os
from pathlib import Path
import shutil
import subprocess

import pytest
from fastapi import HTTPException

from tests.chart_fixtures import OTHER, SID
from tests.test_report_dimension_flow import (chart_client, configured_flow, reference_client, report_flow)  # noqa: F401
from tin.web.report_view import present_snapshot


def test_setup_dimension_save_preserves_only_verified_ids(report_flow, monkeypatch):
    from tin.web import dimension_board, workbench_setup_jobs
    client, state = report_flow
    state.app.include_router(dimension_board.router)
    drafted = {'id': 'd-verified', 'label': '合成维度', 'category': '库存', 'sections': []}
    supplied = {'id': 'd-client', 'label': '手工新增维度', 'category': '其他', 'sections': []}

    async def result(scope, job_id):
        if job_id != 'valid':
            raise HTTPException(409, '报告或授权已改变')
        return {'items': [{'part': 'dimensions', 'state': 'succeeded', 'data': {'dimensions': [drafted]}}]}

    monkeypatch.setattr(workbench_setup_jobs, 'job_result', result)
    url = '/api/sn/dimensions/from-setup/'
    body = {'revision': 0, 'dimensions': [drafted, supplied]}
    assert client.put(url + 'stale', json=body).status_code == 409
    assert client.get('/api/sn/dimensions').json()['revision'] == 0
    response = client.put(url + 'valid', json=body)
    assert response.status_code == 200, response.text
    saved = response.json()
    assert saved['revision'] == 1
    assert saved['dimensions'][0]['id'] == drafted['id']
    assert saved['dimensions'][1]['id'] != supplied['id']
    assert client.put(url + 'valid', json=body).status_code == 409


def test_layout_saves_only_valid_order_and_group_overrides(report_flow):
    client, _, dimensions, bound_chart, _ = configured_flow(report_flow)
    first, second, _ = [row['id'] for row in dimensions]
    saved = client.get('/api/sn/report-drafts?date=2023-01-09&period=weekly').json()
    draft = {key: value for key, value in saved['draft'].items()
             if key not in {'source_revision', 'workbench_id', 'reference_snapshots'}}
    original_notes, original_figures = draft['dimension_notes'], draft['figures']
    url = '/api/sn/report-drafts/' + saved['id']
    for layout in (
        {'dimension_order': ['unregistered']},
        {'dimension_order': [second, second]},
        {'figure_groups': {SID: 'unregistered'}},
        {'figure_groups': {'kb-' + '0' * 48: first}},
    ):
        assert client.patch(url, json={'revision': saved['revision'], 'draft': {**draft, 'layout': layout}}).status_code == 422
    layout = {'dimension_order': [second, first], 'figure_groups': {OTHER: second}}
    response = client.patch(url, json={'revision': saved['revision'], 'draft': {**draft, 'layout': layout}})
    assert response.status_code == 200, response.text
    result = response.json()['draft']
    assert result['layout'] == layout
    assert result['dimension_notes'] == original_notes and result['figures'] == original_figures
    payload = client.get('/sn/reports/2023-01-09/weekly.json').json()
    assert [row['id'] for row in payload['dimension_overview']] == [second, first]
    groups = present_snapshot(payload)['evidence_groups']
    assert [(group['id'], {figure['key'] for figure in group['figures']}) for group in groups[:1]] == [
        (second, {'indicator:' + OTHER, 'chart:' + bound_chart['id']})]


def test_workbench_setup_review_prefill_in_browser():
    node = shutil.which('node')
    if not node or not os.environ.get('RESEARCH_PLAYWRIGHT'):
        pytest.skip('Node and RESEARCH_PLAYWRIGHT are required for isolated browser testing')
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run([node, str(root / 'tests/workbench-setup-review-ui.cjs')],
                            cwd=root, capture_output=True, text=True, timeout=45)
    assert result.returncode == 0, result.stdout + result.stderr

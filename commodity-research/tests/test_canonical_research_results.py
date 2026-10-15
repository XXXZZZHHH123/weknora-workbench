"""Original research results remain visible through the scoped assistant."""
from datetime import date, datetime, timezone
import json

import pytest

from tests.test_knowledge_facts import canonical, FIRST, SECOND, LEGACY, KB, SERIES, signed_read
from tin.integration.identity import Identity
from tin.integration.workbench_scope import WorkbenchScope, workbench_session
from tin.judgments.service import save_version
from tin.models import DailyBrief, Strategy
from tin.models.workbench import WorkbenchArtifact
from tin.ownership import researcher_id
from tin.schemas.judgment import JudgmentIn


def save_results(identifier, user='researcher'):
    identity = Identity(user, user + '@test', user, 17, 'owner')
    scope = WorkbenchScope(identity, identifier, KB, 17,
        {'workspace_mode': 'legacy' if identifier == LEGACY else 'isolated'}, 'Bearer source-user')
    stamp = datetime(2026, 1, 2, tzinfo=timezone.utc)
    with workbench_session(scope) as session:
        save_version(session, JudgmentIn(variety='SN', author=user, written_at=date(2026, 1, 2),
            contradiction={'bull': {'claim': user + ' original thesis'},
                           'bear': {'claim': 'Synthetic alternative'}}, whitelist=[SERIES]), actor=user)
        rid = researcher_id(session)
        session.add(DailyBrief(researcher_id=rid, variety='SN', trade_date='2026-01-02',
            status='草稿', summary=user + ' original brief', generated_at=stamp))
        session.add(Strategy(researcher_id=rid, variety='SN', title=user + ' original strategy',
            kind='outright', mode='simulation', status='draft', plan={'thesis': 'Synthetic manual draft'},
            author=user, version=1, created_at=stamp, entry_fills=[], exit_fills=[]))
        session.add(WorkbenchArtifact(id=user + '-generic', module='judgments', actor=user,
            title=user + ' generic judgment', body='Human record', status='draft', source_ids=[],
            source_snapshots=[], revision=1, created_at=stamp, updated_at=stamp))
        session.commit()


def read_tool(client, identifier, operation, **parameters):
    body = json.dumps({'operation': operation, **parameters}).encode()
    return client.post('/research/api/tools/read', content=body,
        headers=signed_read(body, identity_updates={'workbench_id': identifier}))


@pytest.mark.parametrize('identifier', [FIRST, LEGACY])
def test_original_results_and_generic_records_share_tools_and_overview(canonical, identifier):
    client, headers, state, _ = canonical
    save_results(identifier)
    save_results(identifier, 'another-user')
    for operation, marker, count in [('judgment', 'researcher original thesis', 2),
                                   ('brief', 'researcher original brief', 1),
                                   ('strategies', 'researcher original strategy', 1)]:
        response = read_tool(client, identifier, operation)
        assert response.status_code == 200, response.text
        assert response.json()['data']['total'] == count
        assert marker in response.text and 'another-user' not in response.text
        assert response.json()['temporal_scope'] == 'current_manual_records'
        assert not response.json()['requested_date_applied']
        other = read_tool(client, SECOND, operation)
        assert other.status_code == 200 and other.json()['data']['total'] == 0
    result = client.get('/research/api/workbenches/' + identifier + '/overview', headers=headers)
    assert result.status_code == 200, result.text
    assert result.json()['research_counts']['judgments'] == 2
    assert result.json()['research_counts']['briefs'] == 1
    assert result.json()['research_counts']['strategies'] == 1
    assert next(task for task in result.json()['readiness']['tasks'] if task['id'] == 'research')['status'] == 'ready'
    state['ready'] = False
    result = read_tool(client, identifier, 'judgment')
    assert result.status_code == 200 and result.json()['data']['total'] == 2
    assert read_tool(client, identifier, 'indicators').json()['total'] == 0


def test_merged_result_pagination_and_new_empty_default_graph_are_honest(canonical):
    client, headers, _, _ = canonical
    assert client.post('/research/auth/session', headers=headers).status_code == 200
    assert client.get('/research/workbenches/' + FIRST + '/sn/diagram').status_code == 200
    before = client.get('/research/api/workbenches/' + FIRST + '/overview', headers=headers).json()
    assert not any(before['research_counts'].values())
    save_results(FIRST)
    first = read_tool(client, FIRST, 'judgment', limit=1).json()['data']
    second = read_tool(client, FIRST, 'judgment', limit=1, offset=1).json()['data']
    assert first['total'] == second['total'] == 2
    assert first['items'] != second['items'] and first['truncated'] and not second['truncated']

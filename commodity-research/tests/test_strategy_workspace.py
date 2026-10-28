"""Real scoped storage, explicit strategy actions, source versions and independent drafts."""
from contextlib import contextmanager
from dataclasses import replace
import json
from uuid import uuid4

from fastapi.responses import JSONResponse
from sqlalchemy import select

from tests.test_research_strategy_cards import cards, entry, URL, OTHER
from tin.integration.workbench_scope import artifact_session, current_workbench
from tin.models import Strategy, StrategyEvent
from tin.web import research_strategy


def test_selected_card_endpoint_loads_only_requested_record(cards):
    client, state = cards
    first = client.post(URL, json=entry(source_ids=[])).json()
    second_input = entry(source_ids=[])
    second_input['card']['title'] = 'Another independent fixture'
    second_input['card']['original_text'] = 'Never embed this offscreen original into the initial page.'
    second = client.post(URL, json=second_input).json()
    detail = client.get(URL + '/' + first['id'])
    assert detail.status_code == 200, detail.text
    assert detail.json()['id'] == first['id'] and detail.json()['card'] == first['card']
    scope = state['scope']
    assert client.get(URL + '/' + first['id'], headers={'x-actor':'second'}).status_code == 404
    assert client.get(URL + '/' + first['id'], headers={'x-workbench':OTHER}).status_code == 404
    with artifact_session(scope) as session:
        context = research_strategy.workspace_context(session, scope, selected_id=first['id'])
        no_card = research_strategy.workspace_context(session, scope, select_first=False)
    assert context['selected']['id'] == first['id'] and len(context['items']) == 2
    assert not {'body', 'card', 'source_snapshots', 'source_ids', 'legacy_body'} & context['items'][0].keys()
    assert second_input['card']['original_text'] not in json.dumps(context)
    assert no_card['selected'] is None and all('card' not in row for row in no_card['items'])
    assert next(row for row in context['items'] if row['id'] == second['id'])['review_date'] == '2024-04-05'


def test_strategy_requires_explicit_action_or_observation_choice_without_breaking_old_cards(cards):
    client, _ = cards
    old = client.post(URL, json=entry(source_ids=[]))
    assert old.status_code == 201 and 'response_action' not in old.json()['card']
    value = entry(source_ids=[])
    value['card']['response_action'] = '  '
    assert client.post(URL, json=value).status_code == 422
    value['acknowledge_observation'] = True
    accepted = client.post(URL, json=value)
    assert accepted.status_code == 201 and accepted.json()['status'] == 'draft'
    value['acknowledge_observation'] = False
    value['card']['response_action'] = 'After the boundary is confirmed, pause and request manual review.'
    accepted = client.post(URL, json=value)
    assert accepted.status_code == 201
    assert accepted.json()['card']['response_action'] == value['card']['response_action']
    assert not {'entry', 'target', 'stop', 'risk_budget'} & accepted.json()['card'].keys()


def test_referenced_strategy_is_independent_and_keeps_exact_source_revision(cards):
    client, state = cards
    original = client.post(URL, json=entry(source_ids=[])).json()
    clone = entry(source_ids=[])
    reference = {'kind':'strategy', 'id':'research:' + original['id'], 'revision':1}
    clone['card'].update(reference=reference, response_action='Review before acting.')
    saved = client.post(URL, json=clone)
    assert saved.status_code == 201, saved.text
    copied = saved.json()
    assert copied['id'] != original['id'] and copied['card']['reference'] == reference
    revised = entry(source_ids=[], revision=1)
    revised['card']['thesis'] = 'New source hypothesis; the earlier copied version must not move.'
    assert client.patch(URL + '/' + original['id'], json=revised).status_code == 200
    stored_copy = client.get(URL + '/' + copied['id']).json()
    assert stored_copy['card']['reference'] == reference
    assert stored_copy['card']['thesis'] == original['card']['thesis']
    # Editing the copied action still validates the historical immutable source version.
    change_copy = {**clone, 'revision':1}
    change_copy['card']['response_action'] = 'A separately revised action.'
    assert client.patch(URL + '/' + copied['id'], json=change_copy).status_code == 200
    assert client.post(URL, json=clone, headers={'x-actor':'second'}).status_code == 404
    assert client.post(URL, json=clone, headers={'x-workbench':OTHER}).status_code == 404
    bad = entry(source_ids=[])
    bad['card']['reference'] = {**reference, 'revision':99}
    assert client.post(URL, json=bad).status_code == 404
    assert client.get(URL + '/' + original['id']).json()['revision'] == 2


def test_judgment_reference_is_server_validated_and_never_forged(cards):
    from tin.judgments.service import save_version
    from tin.schemas.judgment import JudgmentIn
    from tests.test_structured_judgment_import import payload
    client, state = cards
    value = entry(source_ids=[])
    value['card']['judgment_reference'] = {'kind':'judgment', 'id':'1234567', 'revision':1}
    response = client.post(URL, json=value)
    assert response.status_code == 404, response.text
    value['card']['judgment_reference']['revision'] = True
    assert client.post(URL, json=value).status_code == 422
    assert client.get(URL).json()['total'] == 0
    scope = state['scope']
    with artifact_session(scope) as session:
        document = JudgmentIn.model_validate(payload())
        session.info.update(knowledge_facts=True,
            knowledge_facts_catalog={identifier:{'unit':'吨'} for identifier in document.whitelist})
        judgment = save_version(session, document, actor=scope.identity.user_id)
        session.commit()
        pinned = {'kind':'judgment','id':str(judgment.id),'revision':judgment.version}
    value['card']['judgment_reference'] = pinned
    saved = client.post(URL, json=value)
    assert saved.status_code == 201, saved.text
    assert saved.json()['card']['judgment_reference'] == pinned
    assert client.post(URL, json=value, headers={'x-actor':'second'}).status_code == 404


def test_legacy_unassigned_card_stays_preview_only_and_foreign_is_hidden(cards):
    from tin.web.research_references import remember_record_scope
    client, state = cards
    created = client.post(URL, json=entry(source_ids=[])).json()
    scope = state['scope']
    with artifact_session(scope) as session:
        from tin.models import SourceMapping
        for row in session.scalars(select(SourceMapping)).all():
            if (row.config or {}).get('kind') == 'artifact': session.delete(row)
        session.commit()
        legacy = replace(scope, metadata={**scope.metadata, 'workspace_mode':'legacy'})
        preview = research_strategy._read_card(session, legacy, created['id'])
        assert preview['scope_status'] == 'unassigned'
        assert not preview['can_edit'] and not preview['can_apply']
        session.info['workbench_id'] = str(uuid4())
        remember_record_scope(session, 'artifact', created['id']); session.commit()
        session.info['workbench_id'] = scope.workbench_id
        assert research_strategy.workspace_context(session, scope)['items'] == []
    assert client.get(URL + '/' + created['id']).status_code == 404
    hidden = client.get(URL).json()
    assert hidden['total'] == 0 and hidden['items'] == []
    assert client.patch(URL + '/' + created['id'], json=entry(source_ids=[], revision=1)).status_code == 404


def add_plan_routes(client):
    from tin.web.strategy import build_router
    @contextmanager
    def factory():
        with artifact_session(current_workbench.get()) as session: yield session
    class Templates:
        def TemplateResponse(self, request, template, context):
            return JSONResponse({'template': template, 'context':context})
    def shell(session, active, day, **extra): return extra
    client.app.include_router(build_router(factory, Templates(), shell))


def plan():
    return {'title':'Synthetic independent plan', 'variety':'SN', 'kind':'outright', 'mode':'simulation',
        'legs':[{'contract':'SN2611','side':'buy','quote_series_id':'SHFE.SN.2611.close'}],
        'thesis':'A fixture inference', 'alternative_explanation':'A competing inference',
        'evidence':[{'as_of':'2024-01-01T00:00:00Z','source':'Fixture note','statement':'Qualitative test evidence.'}],
        'market_expectation':'Explicit fixture expectation', 'invalidation':'Manual review if invalid',
        'catalyst':'Next fixture observation', 'horizon_days':2, 'as_of':'2024-01-01T00:00:00Z',
        'review_at':'2024-01-03T00:00:00Z', 'entry':{'low':100,'high':110,'reason':'Fixture'},
        'target':{'low':120,'high':130,'reason':'Fixture'}, 'stop':{'low':80,'high':90,'reason':'Fixture'},
        'entry_condition':'Manual confirmation', 'risk_budget':100}


def test_plan_application_makes_new_draft_without_mutating_or_executing_source(cards):
    client, state = cards
    add_plan_routes(client)
    original = client.post('/api/sn/strategies', json=plan())
    assert original.status_code == 201, original.text
    source = original.json(); before = client.get('/api/sn/strategies/' + str(source['id'])).json()
    edited = {**source['plan'], 'title':'Separately edited fixture plan', 'entry_condition':'Changed manual condition'}
    copied = client.post('/api/sn/strategies/' + str(source['id']) + '/draft', json={'base_version':source['version'],'plan':edited})
    assert copied.status_code == 201, copied.text
    value = copied.json()
    assert value['id'] != source['id'] and value['revision_of'] == source['id']
    assert value['status'] == 'draft' and value['published_at'] is None and value['entry_fills'] == []
    assert value['plan']['entry_condition'] == edited['entry_condition']
    assert client.get('/api/sn/strategies/' + str(source['id'])).json() == before
    with artifact_session(state['scope']) as session:
        event = session.scalar(select(StrategyEvent).where(StrategyEvent.strategy_id == value['id']))
        assert event.payload['reference'] == {'kind':'strategy','id':'plan:' + str(source['id']), 'revision':1, 'title':source['title']}
        assert event.payload['source_plan'] == source['plan']
        assert session.get(Strategy, source['id']).version == 1
    # This card-only fixture otherwise reuses one email for both identities.
    # Actual independent researchers have unique account emails in this schema.
    from tin.jobs.seed import seed_researcher
    second_scope = replace(state['scope'], identity=replace(state['scope'].identity,
        user_id='second', email='second-fixture@example.test'))
    with artifact_session(second_scope) as session:
        seed_researcher(session); session.commit()
    path = '/api/sn/strategies/' + str(source['id']) + '/draft'
    assert client.post(path,json={'base_version':99,'plan':edited}).status_code == 409
    assert client.post(path,json={'base_version':True,'plan':edited}).status_code == 422
    readonly = client.get('/sn/strategies/' + str(source['id']), headers={'x-can-edit':'false'})
    assert readonly.status_code == 200 and not readonly.json()['context']['strategy_can_write']
    assert client.post(path,json={'base_version':1,'plan':edited},headers={'x-actor':'second'}).status_code == 404
    assert client.post(path,json={'base_version':1,'plan':edited},headers={'x-can-edit':'false'}).status_code == 403
    assert client.post(path,json={'base_version':1,'plan':edited},headers={'x-workbench':OTHER}).status_code == 404

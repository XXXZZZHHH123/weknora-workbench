"""The large facts response is encoded once, without altering wire values."""
import datetime as dt
from decimal import Decimal
import threading

import fastapi.routing
import pytest
from fastapi.encoders import jsonable_encoder
from fastapi.responses import JSONResponse

from test_knowledge_research_facts import PATH, facts, signed


def test_facts_response_skips_recursive_fastapi_encoder_and_preserves_json(facts,monkeypatch):
    client,_,_,_,_,body=facts
    document={'rows':[{'date':dt.date(2026,2,3),'value':0,'raw_value':'零',
                       'fraction':Decimal('0.125'),'whole':Decimal('9007199254740993'),
                       'null':None,'valid':False}],
              'stamp':dt.datetime(2026,2,3,4,5,6,tzinfo=dt.timezone.utc)}
    expected=JSONResponse(jsonable_encoder(document)).body
    worker_threads=[]
    def read_document(*args):
        worker_threads.append(threading.get_ident())
        return document
    monkeypatch.setattr('structured_data.research_facts.read_facts',read_document)
    from structured_data.server import _ResearchFactsJSONResponse
    render=_ResearchFactsJSONResponse.render
    def tracked_render(self,content):
        worker_threads.append(threading.get_ident())
        return render(self,content)
    monkeypatch.setattr(_ResearchFactsJSONResponse,'render',tracked_render)
    def recursive_encoder_forbidden(*args,**kwargs):
        raise AssertionError('large research facts were recursively encoded by FastAPI')
    monkeypatch.setattr(fastapi.routing,'jsonable_encoder',recursive_encoder_forbidden)
    response=client.post(PATH,**signed(body))
    assert response.status_code==200
    assert response.content==expected
    assert response.headers['content-type']=='application/json'
    assert response.json()['rows'][0]['whole']==9007199254740993
    assert len(worker_threads)==2 and worker_threads[0]==worker_threads[1]


@pytest.mark.parametrize('invalid',[float('nan'),float('inf'),float('-inf'),Decimal('NaN'),Decimal('Infinity')])
def test_facts_response_rejects_non_finite_numbers(facts,monkeypatch,invalid):
    client,_,_,_,_,body=facts
    monkeypatch.setattr('structured_data.research_facts.read_facts',lambda *args:{'rows':[{'value':invalid}]})
    response=client.post(PATH,**signed(body))
    assert response.status_code==400
    assert 'Out of range float values' in response.json()['detail']


def test_facts_response_rejects_unknown_values_without_stringifying(facts,monkeypatch):
    client,_,_,_,_,body=facts
    monkeypatch.setattr('structured_data.research_facts.read_facts',lambda *args:{'rows':[{'value':object()}]})
    with pytest.raises(TypeError,match='JSON serializable'):
        client.post(PATH,**signed(body))

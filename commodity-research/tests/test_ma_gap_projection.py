"""Missing periods survive bounded projections without becoming zero."""
from datetime import date
from types import SimpleNamespace
from sqlalchemy import select
from sqlalchemy.orm import Session
from tin.compute.moving_average import sma_boundary
from tin.db import Base, make_engine
from tin.integration.kb_facts import install_projection
from tin.models import Observation

SID = 'kb-' + 'a' * 48

def page(include_invalid):
    origin={'dataset_id':'fixture-data','source_knowledge_id':'fixture-file','source_series_id':'source'}
    return {'version':'a'*64,'datasets':[],'indicators':[{'series_id':SID,'metric':'合成观测','unit':'kg','frequency':'日',**origin}],
        'rows':[{'series_id':SID,'date':'2031-01-01','value':1,'value_status':'valid',**origin},
            {'series_id':SID,'date':'2031-01-02','value':None,'value_status':'missing',**origin},
            {'series_id':SID,'date':'2031-01-03','value':3,'value_status':'valid',**origin}],
        'total':3,'offset':0,'next_offset':None,'truncated':False,**({'include_invalid':True} if include_invalid else {})}

def test_missing_slot_blocks_ma_instead_of_compressing_the_sequence():
    engine=make_engine('sqlite://');Base.metadata.create_all(engine)
    with engine.connect() as connection:
        info=install_projection(connection,SimpleNamespace(),pages=[page(True)])
        with Session(bind=connection) as session:
            session.info.update(info)
            rows=session.scalars(select(Observation).order_by(Observation.as_of)).all()
            assert [row.value for row in rows]==[1,None,3]
            assert [row.status for row in rows]==['正常','数据缺失','正常']
            result=sma_boundary(session,SID,date(2031,1,3),3)
            assert result['count']==3 and result['value'] is None and '缺失' in result['error']
            assert info['knowledge_facts_include_invalid'] is True
    engine.dispose()

def test_default_projection_and_cache_metadata_keys_are_unchanged():
    engine=make_engine('sqlite://');Base.metadata.create_all(engine)
    with engine.connect() as connection:
        info=install_projection(connection,SimpleNamespace(),pages=[page(False)])
        assert set(info)=={'knowledge_facts','knowledge_facts_version','knowledge_facts_catalog','knowledge_facts_datasets'}
        with Session(bind=connection) as session:
            session.info.update(info)
            assert [row.value for row in session.scalars(select(Observation).order_by(Observation.as_of))]==[1,3]
            assert sma_boundary(session,SID,date(2031,1,3),2)['value'] is None
    engine.dispose()

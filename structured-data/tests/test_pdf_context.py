"""Page context is hash-bound source evidence, never an inferred unit bind."""
from copy import deepcopy
import json
import pytest
from structured_data.pdf_tables import PDFTableStore, _hash


def raw_bundle():
    return dict(schema_version='pdf_tables_v1',source={'sha256':'a'*64,'file_name':'context.pdf'},
        parser={'engine':'fixture','version':'1'},fragments=[
            dict(id=f'table-{p}',page=p,bbox=[.1,.2,.9,.5],caption=f'Caption {p}',footnotes=[],
                 rows=[[{'text':'Item'},{'text':'Value'}],[{'text':f'A-{p}'},{'text':str(p)}]]) for p in (1,2)])


def context(page,text,**extra):
    return dict(page=page,text=text,text_parts=[text],bbox=[.1,.1,.8,.15],block_type='text',**extra)


def setup(tmp_path,raw=None,selected=('table-1',)):
    store=PDFTableStore(tmp_path/'store');draft=store.create_draft(raw or raw_bundle())
    plan=dict(bundle_sha256=draft['bundle_sha256'],fragment_ids=list(selected),
        header_rows={i:[dict(row=0,reason='Explicit reviewed header')] for i in selected},
        columns=[dict(name='item',type='text',source_columns={i:0 for i in selected}),
                 dict(name='amount',type='decimal',source_columns={i:1 for i in selected})])
    return store,draft,plan


def test_selected_page_context_retains_parts_and_does_not_bind_units(tmp_path):
    raw=raw_bundle();raw.update(page_context=[context(1,'Unit: kg. '),context(2,'Unit: lb.'),context(3,'Appendix')],page_context_coverage='complete')
    raw['page_context'][0]['text_parts']=['Unit: ','kg. ']
    original=deepcopy(raw);store,draft,plan=setup(tmp_path,raw)
    preview=store.validate_plan(draft['draft_id'],plan)
    assert preview['page_context']==[original['page_context'][0]]
    assert preview['page_context_scope']=='page_candidates_not_table_binding'
    assert preview['page_context_coverage']=='complete'
    assert not any('unit' in c for c in preview['columns'])
    assert preview['fragments'][0]['footnotes']==[]
    assert not preview['coverage']['source_document_complete'] and not preview['extraction_verified']
    preview['page_context'][0]['text']='caller mutation'
    assert store.read_draft(draft['draft_id'])['bundle']==original
    table=store.publish(draft['draft_id'],dict(plan,reviewed=True))
    assert store.read_table(table['table_id'])['page_context']==[original['page_context'][0]]
    _,_,both_plan=setup(tmp_path/'both',raw,selected=('table-1','table-2'))
    both=store.validate_plan(draft['draft_id'],both_plan)
    assert [c['page'] for c in both['page_context']]==[1,2]


def test_unanchored_context_remains_unanchored_and_warns(tmp_path):
    raw=raw_bundle();uncertain=context(None,'Unlocated unit note',requires_review=True,parser_page_idx='unknown')
    raw.update(page_context=[context(2,'Other page'),uncertain],page_context_coverage='incomplete',page_context_warnings=['Missing original page index'])
    store,draft,plan=setup(tmp_path,raw);table=store.publish(draft['draft_id'],dict(plan,reviewed=True))
    assert table['page_context']==[uncertain]
    assert table['page_context'][0]['page'] is None and table['page_context'][0]['requires_review']
    assert table['page_context_coverage']=='incomplete'
    assert table['page_context_scope']=='page_candidates_not_table_binding'
    assert table['page_context_warnings']==raw['page_context_warnings']
    assert table['columns'][1].get('unit') is None


def test_legacy_no_context_drafts_and_published_tables_are_compatible(tmp_path):
    store,draft,plan=setup(tmp_path)
    table=store.publish(draft['draft_id'],dict(plan,reviewed=True))
    assert not any(k.startswith('page_context') for k in draft['bundle'])
    assert not any(k.startswith('page_context') for k in table)
    restored=PDFTableStore(store.root)
    assert restored.read_draft(draft['draft_id'])==draft
    assert restored.read_table(table['table_id'])==table
    result=restored.query(table['table_id'],'SELECT sum(amount) FROM data')
    assert result['rows']==[['1.000000000000000000']]
    assert result['provenance']['draft_id']==draft['draft_id']


@pytest.mark.parametrize('page',[0,-1,False,True,1.0,'1',[],{}])
def test_context_invalid_pages_are_rejected(tmp_path,page):
    raw=raw_bundle();raw.update(page_context=[context(page,'text')],page_context_coverage='complete')
    with pytest.raises(ValueError,match='invalid page'):setup(tmp_path,raw)


@pytest.mark.parametrize('change',['not_list','too_many','missing_text','missing_parts','nonstring_part','inconsistent_parts','bad_bbox','missing_coverage','unknown_coverage'])
def test_context_shape_parts_and_geometry_validation(tmp_path,change):
    raw=raw_bundle();raw.update(page_context=[context(1,'original source text')],page_context_coverage='complete');item=raw['page_context'][0]
    if change=='not_list':raw['page_context']={}
    elif change=='too_many':raw['page_context']=[context(1,'')]*5001
    elif change=='missing_text':del item['text']
    elif change=='missing_parts':del item['text_parts']
    elif change=='nonstring_part':item['text_parts']=[12]
    elif change=='inconsistent_parts':item['text_parts']=['changed']
    elif change=='bad_bbox':item['bbox']=[0,0,2,1]
    elif change=='missing_coverage':del raw['page_context_coverage']
    else:raw['page_context_coverage']='verified'
    with pytest.raises(ValueError):setup(tmp_path,raw)


def test_context_utf8_block_and_total_byte_budgets(tmp_path):
    raw=raw_bundle();raw.update(page_context=[context(1,'字'*5462)],page_context_coverage='complete')
    with pytest.raises(ValueError,match='block exceeds text budget'):setup(tmp_path/'single',raw)
    raw['page_context']=[context(1,'x'*(16*1024)) for _ in range(65)]
    with pytest.raises(ValueError,match='page context exceeds text budget'):setup(tmp_path/'total',raw)
    raw['page_context']=raw['page_context'][:64]
    store,draft,_=setup(tmp_path/'boundary',raw)
    assert len(store.read_draft(draft['draft_id'])['bundle']['page_context'])==64


def test_context_hash_binding_and_explicit_unit_audit_through_query(tmp_path):
    raw=raw_bundle();raw.update(page_context=[context(1,'Unit: kg')],page_context_coverage='complete')
    store,draft,plan=setup(tmp_path,raw);plan['columns'][1]['unit']='kg'
    table=store.publish(draft['draft_id'],dict(plan,reviewed=True))
    result=store.query(table['table_id'],'SELECT _row_id,amount FROM data')
    assert result['provenance']['plan_sha256']==table['plan_sha256']
    assert result['provenance']['bundle_sha256']==draft['bundle_sha256']
    assert result['source']['sha256']==raw['source']['sha256']
    inspected=store.read_table(result['table_id'])
    assert inspected['columns'][1]['unit']=='kg' and inspected['page_context'][0]['text']=='Unit: kg'
    changed=deepcopy(raw);changed['page_context'][0].update(text='Unit: lb',text_parts=['Unit: lb'])
    other=store.create_draft(changed)
    assert other['draft_id']!=draft['draft_id']
    with pytest.raises(ValueError,match='exact raw bundle SHA256'):store.validate_plan(other['draft_id'],plan)
    path=store.root/'tables'/(table['table_id']+'.json');saved=json.loads(path.read_text())
    saved['page_context'][0]['text']='Unit: lb';saved.pop('record_sha256');saved['record_sha256']=_hash(saved)
    path.write_text(json.dumps(saved))
    with pytest.raises(ValueError,match='raw source and plan'):store.read_table(table['table_id'])

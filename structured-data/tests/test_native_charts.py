"""Source-bound chart regressions, including seasonal year traps and scope leaks."""
import hashlib
import json
from xml.sax.saxutils import escape
from zipfile import ZipFile

import pytest

from structured_data.native_charts import VERSION, reference, extract
from structured_data.store import DataStore
from structured_data.field_resolver import FieldResolver


@pytest.fixture
def chart_store(tmp_path):
    path = tmp_path / 'graphs.xlsx'
    ns = 'http://schemas.openxmlformats.org/'
    main = ns+'spreadsheetml/2006/main'; office = ns+'officeDocument/2006/relationships'
    chart = ns+'drawingml/2006/chart'; drawing = ns+'drawingml/2006/spreadsheetDrawing'
    def relationships(items):
        def relationship_type(target):
            if 'worksheets/' in target: suffix = 'worksheet'
            elif 'drawings/' in target: suffix = 'drawing'
            elif 'charts/' in target: suffix = 'chart'
            else: suffix = 'worksheet'
            return office + '/' + suffix
        return '<Relationships xmlns="'+ns+'package/2006/relationships">'+''.join(f'<Relationship Id="{key}" Type="{relationship_type(target)}" Target="{target}"/>' for key,target in items)+'</Relationships>'
    # The first curve deliberately uses a 2017 X axis for 2023 source values.
    curves = [('2023',"'源 表'!$B$2:$B$3"),('2022',"'源 表'!$B$4:$B$4"),
              ('未导入','待确认!B2:B4'),('外部','[else.xlsx]数据!B2:B4'),('命名范围','Sales')]
    ser = ''.join(f'<c:ser><c:tx><c:v>{escape(label)}</c:v></c:tx><c:cat><c:numRef><c:f>轴!A2:A3</c:f></c:numRef></c:cat><c:val><c:numRef><c:f>{escape(ref)}</c:f><c:numCache><c:pt idx="0"><c:v>999999</c:v></c:pt></c:numCache></c:numRef></c:val></c:ser>' for label,ref in curves)
    xml = f'<c:chartSpace xmlns:c="{chart}" xmlns:a="{ns}drawingml/2006/main"><c:chart><c:title><c:tx><c:rich><a:p><a:r><a:t>现货销售价格</a:t></a:r></a:p></c:rich></c:tx></c:title><c:plotArea><c:lineChart>{ser}</c:lineChart></c:plotArea></c:chart></c:chartSpace>'
    with ZipFile(path,'w') as z:
        z.writestr('xl/workbook.xml',f'<workbook xmlns="{main}" xmlns:r="{office}"><sheets><sheet name="利润图" sheetId="1" r:id="r1"/></sheets></workbook>')
        z.writestr('xl/_rels/workbook.xml.rels',relationships([('r1','worksheets/sheet1.xml')]))
        z.writestr('xl/worksheets/sheet1.xml',f'<worksheet xmlns="{main}" xmlns:r="{office}"><drawing r:id="d1"/></worksheet>')
        z.writestr('xl/worksheets/_rels/sheet1.xml.rels',relationships([('d1','../drawings/drawing1.xml')]))
        z.writestr('xl/drawings/drawing1.xml',f'<xdr:wsDr xmlns:xdr="{drawing}" xmlns:c="{chart}" xmlns:r="{office}"><xdr:twoCellAnchor><xdr:from><xdr:row>12</xdr:row></xdr:from><xdr:graphicFrame><c:chart r:id="c1"/></xdr:graphicFrame></xdr:twoCellAnchor></xdr:wsDr>')
        # Unused chart relationships must not become phantom graphs.
        z.writestr('xl/drawings/_rels/drawing1.xml.rels',relationships([('c1','../charts/chart1.xml'),('unused','../charts/chart2.xml')]))
        z.writestr('xl/charts/chart1.xml',xml);z.writestr('xl/charts/chart2.xml',xml)
    series = [dict(series_id='price',sheet='源 表',metric='销售单价',unit='元',dimensions={},header_cells=['B1'],date_mode='date')]
    observations = [dict(series_id='price',date=day,value=value,source_cell=f'B{row}',date_cell=f'A{row}',value_status='valid',raw_value=value)
                    for row,day,value in [(2,'2023-09-07',10),(3,'2023-09-08',20),(4,'2024-01-01',99),(5,'2025-01-01',1234)]]
    store=DataStore(tmp_path/'data')
    ds=store.publish(dict(source_file=str(path),sha256=hashlib.sha256(path.read_bytes()).hexdigest(),rule_version='test',series=series,observations=observations,sheets=[],excluded_regions=[],issues=[]))['dataset_id']
    store.chart_index(ds)
    return store,ds,path


def test_reference_syntax_is_local_and_bounded():
    assert reference("'O''Brien 数据'!$AA$2:$AA$9")['sheet']=="O'Brien 数据"
    for text in ['[book.xlsx]s!A1:A2','NamedRange','s!A1:B9','s!B9:B1','s!A0','s!A1:A1048577','s!A1,s!A9','INDIRECT("s!A1")']:
        assert reference(text) is None


def test_real_objects_preserve_literal_legends_and_actual_dates(chart_store):
    store,ds,path=chart_store
    assert len(extract(path))==1
    idx=store.chart_index(ds); chart=idx['charts'][0]
    assert chart['title']=='现货销售价格' and chart['anchor_row']==13
    assert [c['status'] for c in chart['curves']]==['linked','linked','pending_source','unsupported','unsupported']
    assert chart['status']=='partial'
    assert store.chart_index(ds)==idx


def test_dual_axis_binding_is_preserved_as_xml_evidence(chart_store, tmp_path):
    _, _, original_path = chart_store
    from structured_data.source_inspection import _chart_evidence

    ns = 'http://schemas.openxmlformats.org/drawingml/2006/chart'
    main = 'http://schemas.openxmlformats.org/spreadsheetml/2006/main'
    office = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships'
    drawing = 'http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing'
    chart_xml = f'''<c:chartSpace xmlns:c="{ns}" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
      <c:chart><c:plotArea>
        <c:lineChart><c:axId val="10"/><c:axId val="20"/><c:ser><c:tx><c:v>Revenue</c:v></c:tx><c:cat><c:strRef><c:f>Data!A2:A3</c:f></c:strRef></c:cat><c:val><c:numRef><c:f>Data!B2:B3</c:f></c:numRef></c:val></c:ser></c:lineChart>
        <c:lineChart><c:axId val="10"/><c:axId val="30"/><c:ser><c:tx><c:v>Volume</c:v></c:tx><c:cat><c:strRef><c:f>Data!A2:A3</c:f></c:strRef></c:cat><c:val><c:numRef><c:f>Data!C2:C3</c:f></c:numRef></c:val></c:ser></c:lineChart>
        <c:catAx><c:axId val="10"/><c:title><c:tx><c:rich><a:p><a:r><a:t>Period</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="20"/></c:catAx>
        <c:valAx><c:axId val="20"/><c:title><c:tx><c:rich><a:p><a:r><a:t>USD</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="10"/></c:valAx>
        <c:valAx><c:axId val="30"/><c:title><c:tx><c:rich><a:p><a:r><a:t>Units</a:t></a:r></a:p></c:rich></c:tx></c:title><c:crossAx val="10"/><c:dispUnits><c:builtInUnit val="thousands"/></c:dispUnits></c:valAx>
      </c:plotArea></c:chart></c:chartSpace>'''
    source_path = tmp_path / 'dual-axis.xlsx'
    with ZipFile(original_path) as source_zip, ZipFile(source_path, 'w') as target_zip:
        for name in source_zip.namelist():
            if name == 'xl/charts/chart1.xml':
                target_zip.writestr(name, chart_xml)
            else:
                target_zip.writestr(name, source_zip.read(name))

    charts = extract(source_path)
    (tmp_path / f'{VERSION}.json').write_text(json.dumps({
        'version': VERSION, 'dataset_id': tmp_path.name,
        'chart_count': len(charts), 'charts': charts,
    }), encoding='utf-8')
    chart = charts[0]
    assert chart['axis_titles'] == ['Period', 'USD', 'Units']
    assert [(axis['id'], axis['type'], axis['title']) for axis in chart['axes']] == [
        ('10', 'catAx', 'Period'), ('20', 'valAx', 'USD'), ('30', 'valAx', 'Units')]
    assert chart['axes'][2]['display_units'] == {'built_in': 'thousands', 'custom': None}
    assert [curve['axis_ids'] for curve in chart['curves']] == [['10', '20'], ['10', '30']]

    source_sha = hashlib.sha256(source_path.read_bytes()).hexdigest()
    evidence, _, _ = _chart_evidence(source_path, 'Data', [('B2:B3', (2, 2, 2, 3))], str(source_path), source_sha)
    assert len(evidence) == 1
    assert evidence[0]['curve_label'] == 'Revenue'
    assert evidence[0]['axis_ids'] == ['10', '20']
    assert evidence[0]['axes'][1]['title'] == 'USD'
    # The volume curve remains tied to the separate axis and range.
    volume, _, _ = _chart_evidence(source_path, 'Data', [('C2:C3', (3, 2, 3, 3))], str(source_path), source_sha)
    assert volume[0]['curve_label'] == 'Volume'
    assert volume[0]['axis_ids'] == ['10', '30']


def test_query_uses_source_not_cache_and_never_leaks_outside_chart(chart_store):
    store,ds,_=chart_store; view=store.chart_index(ds)['views'][0]
    q=store.query_series(ds,view['series_id'],'2023-09-07','2023-09-07')
    assert q['rows'][0]['value']==10 and q['rows'][0]['source_cell']=='B2'
    assert q['rows'][0]['date_cell']=='A2' and q['source']['sheet']=='源 表'
    assert store.query_series(ds,view['series_id'],operation='sum')['aggregate']==129
    assert store.query_series(ds,view['series_id'],'2024-01-01','2024-01-01')['rows'][0]['value']==99
    assert store.query_series(ds,view['series_id'],operation='latest')['rows'][0]['value']==99
    assert view['chart_legends']==['2023','2022']
    assert view['chart_curve_sources'][1]['label']=='2022'
    assert view['chart_curve_sources'][1]['first_date']=='2024-01-01'
    assert view['chart_curve_sources'][1]['reference']['start_row']==4
    assert store.chart_index(ds)['charts'][0]['curves'][1]['first_date']=='2024-01-01'
    assert all('不自动当作年份' in c['date_notice'] for c in store.chart_index(ds)['charts'][0]['curves'][:2])


def test_chart_scope_resolves_to_evidenced_source_and_keeps_other_scope(chart_store):
    store,ds,_=chart_store
    def model(payload):
        cols=[c for g in payload['catalog'] for c in g['columns']]
        assert len(cols)==3 and all(c.get('chart_sheet')=='利润图' for c in cols)
        chosen=next(c for c in cols if c['name']=='现货销售价格')
        return {'intent':'lookup','decision':'resolved','fields':[{'role':'v1','label':'价格','candidate_ids':[chosen['id']]}]}
    result=FieldResolver(store,model).answer('利润图中2023/9/7现货销售价格',dataset_id=ds,sheet='利润图',date='2023-09-07')
    assert result['answer']['sources']['v1']['rows'][0]['value']==10
    assert store.catalog(ds,sheet='不存在',question='利润图')==[]
    assert store.list_series(ds,sheet='利润图')['series'][0]['chart_sheet']=='利润图'
    assert '现货销售价格' in store.catalog(ds)[0]['chart_titles']


def test_list_chart_pagination_and_no_source_fallback(chart_store):
    store,ds,_=chart_store
    assert store.list_charts(ds,sheet='利润图')['total']==1
    assert not store.list_charts(ds,search='不存在')['charts']
    with pytest.raises(ValueError):store.list_charts(ds,offset=-1)
    with pytest.raises(ValueError):store.query_series(ds,'chart_invented')


def test_each_curve_has_independent_range(chart_store,monkeypatch):
    from structured_data import native_charts
    store,ds,_=chart_store
    real_extract=native_charts.extract
    def contract_charts(path):
        charts=real_extract(path)
        charts[0]['curves']=charts[0]['curves'][:2]
        for c,label in zip(charts[0]['curves'],['1901','2001']):c['label']=label
        return charts
    monkeypatch.setattr(native_charts,'extract',contract_charts)
    (store._path(ds) / f'{native_charts.VERSION}.json').unlink()
    idx=store.chart_index(ds);curves=idx['charts'][0]['curves']
    assert all(c['status']=='linked' for c in curves)
    assert curves[0]['series_id']!=curves[1]['series_id']
    assert store.query_series(ds,curves[0]['series_id'],operation='sum')['aggregate']==30
    assert store.query_series(ds,curves[1]['series_id'],operation='sum')['aggregate']==99
    assert store.query_series(ds,curves[0]['series_id'],'2024-01-01','2024-01-01')['status']=='no_data'
    scoped=store.catalog(ds,sheet='利润图',question='查询1901曲线')
    # Literal labels remain evidence for the model; the question cannot hide
    # the other curve or convert either code into a calendar year.
    assert len(scoped)==3
    assert {tuple(v['chart_legends']) for v in scoped}=={('1901','2001'),('1901',),('2001',)}
    assert store.catalog(ds,sheet='利润图',question='不要1901曲线，要其他来源')==scoped

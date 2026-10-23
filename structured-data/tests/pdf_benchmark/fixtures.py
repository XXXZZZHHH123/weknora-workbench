"""Authored synthetic inputs and gold expectations; never derived from OCR.

All row/column indexes are zero based. Empty strings are actual authored blanks.
These fixtures are isolated acceptance material, never business query branches.
"""
from copy import deepcopy


def table(id, caption, rows, *, headers=(0,), unit=None, spans=(), top=690, widths=None, font_size=11, row_height=30, logical_group=None):
    return dict(id=id, caption=caption, rows=rows, header_rows=list(headers), unit=unit,
                spans=[dict(row=r, column=c, rowspan=rs, colspan=cs) for r,c,rs,cs in spans],
                top=top, widths=widths, font_size=font_size, row_height=row_height,
                logical_group=logical_group or id)


def definitions():
    return deepcopy([
        dict(id='continued_three_pages', title='Three-page continuation: repeated then absent header', pages=[
            dict(tables=[table('continued-p1','Movement register (kg)',[['Item','Opening','Closing'],['A-01','120.5','118.25'],['A-02','-4.5',''],['A-03','0','8.75']],unit='kg',logical_group='movement')]),
            dict(tables=[table('continued-p2','Movement register (kg), continued',[['Item','Opening','Closing'],['A-04','16','14.5'],['A-05','','-2.25']],unit='kg',logical_group='movement')]),
            dict(tables=[table('continued-p3','Movement register (kg), continued',[['A-06','7.25','6'],['A-07','3','1.5']],headers=(),unit='kg',logical_group='movement')]),
        ], expected_logical_groups=[['continued-p1','continued-p2','continued-p3']]),
        dict(id='split_text_row', title='Cross-page textual continuation with unambiguous numeric source', pages=[
            dict(tables=[table('split-p1','Warehouse movement (kg)',[['Warehouse','Quantity'],['Main depot','11.25'],['Northern distribution','-4.50']],unit='kg',top=150,logical_group='warehouses')]),
            dict(tables=[table('split-p2','Warehouse movement (kg), continued',[['annex warehouse',''],['South depot','9.75']],headers=(),unit='kg',top=730,logical_group='warehouses')]),
        ], expected_logical_groups=[['split-p1','split-p2']],
            boundary_operations=[dict(upper=['split-p1',2],lower=['split-p2',0],cells=[dict(column=0,operation='concat',separator=' '),dict(column=1,operation='take_upper')])]),
        dict(id='merged_headers', title='Merged multirow headers with real blank numeric cell', pages=[
            dict(tables=[table('multihead','Regional measured output',[
                ['Region','Output','','Note'],['','Q1 (t)','Q2 (t)',''],['North','12.50','','Audited'],['South','-3.25','8.75','Provisional']],
                headers=(0,1),spans=((0,0,2,1),(0,1,1,2),(0,3,2,1)),widths=[130,120,120,140])]),
        ]),
        dict(id='body_spans_refusal', title='Merged body cells require refusal of ordinary scalar mapping', pages=[
            dict(tables=[table('bodyspans','Grouped measurement schedule',[
                ['Group','Item','Value'],['Shared group','A','12'],['','B','7'],['Combined A+B','','19']],
                spans=((1,0,2,1),(3,0,1,2)),widths=[170,170,170])]),
        ], publication_expectation='reject_merged_data_regions'),
        dict(id='landscape_numeric', title='Wide landscape table with literal number formats and annotations', pages=[
            dict(landscape=True,tables=[table('wide','Literal formats: values and annotations must remain distinct',[
                ['Code','Gross','Ratio','Change','Dash','Blank','Footnote','Squared','Text','Balance'],
                ['W-01','1,234.50','12.5%','−8.25','—','','4*','10^{2}','not numeric','-0.50'],
                ['W-02','2,000','0%','+3.00','-','','6*','3^{2}','N/A','0.00']],
                widths=[65,78,65,72,48,48,74,70,110,90],font_size=8,row_height=38,
                top=450)],notes=['* Footnote markers annotate a value; they are not multiplicative operators.','Superscripts in Squared are mathematical exponents; no numeric flattening is allowed.'])]),
        dict(id='chinese_native_scan', title='中文混合原生文字与倾斜扫描表格', pages=[
            dict(tables=[table('cn-native','仓库数量记录（吨）',[
                ['仓库','入库量','出库量'],['华东一号库','12.50','-3.25'],['华南二号库','','8.75']],unit='吨',top=690,widths=[190,160,160])],
                scan_table=table('cn-scan','扫描库存记录（件）',[
                    ['项目','期初','期末'],['甲类材料','105','102'],['乙类材料','-6','']],unit='件',headers=(0,),widths=[190,160,160]))
        ]),
        dict(id='formula_structures', title='Fraction, square root, summation and matrix', pages=[
            dict(tables=[table('formula-ref','Formula reference table',[['ID','Description'],['F1','fraction'],['F2','root'],['F3','sum'],['F4','matrix']],top=235,row_height=24)],formulas=[
                dict(id='F1',latex=r'\frac{a+b}{c-d}',kind='fraction',x=95,y=660),
                dict(id='F2',latex=r'\sqrt{x^{2}+y^{2}}',kind='root',x=315,y=660),
                dict(id='F3',latex=r'\sum_{i=1}^{n}i=\frac{n(n+1)}{2}',kind='sum',x=95,y=480),
                dict(id='F4',latex=r'\begin{bmatrix}1&-2\\3&4\end{bmatrix}',kind='matrix',x=355,y=480),
            ])
        ]),
        dict(id='different_units_blank_control', title='Adjacent same-shaped tables with incompatible units and an authored blank row', pages=[
            dict(tables=[table('kg-table','Independent load measurement (kg)',[['Item','Quantity (kg)'],['Control-A','12.00'],['',''],['Control-B','-3.50']],unit='kg')]),
            dict(tables=[table('lb-table','Independent load measurement (lb)',[['Item','Quantity (lb)'],['Control-A','20.00'],['Control-B','5.25']],unit='lb')]),
        ], forbidden_merges=[['kg-table','lb-table']],blank_row_controls=[dict(table_id='kg-table',row=2,kind='authored_blank_row')]),
    ])

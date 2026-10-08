"""Source-checked mixed-layout profile; suggestions remain pending user review."""
from copy import deepcopy
import re
from openpyxl.utils import get_column_letter, column_index_from_string

PROFILE_VERSION = 'cotton-auction-regions-v1'


def auction_profile(ws):
    if ws.title != '2018年抛储':
        return None
    rows = list(ws.iter_rows(max_row=154, max_col=39, values_only=True))
    def v(r,c): return rows[r-1][c-1] if len(rows)>=r else None
    if v(4,1)!='日期' or v(4,33)!='国储理论库存' or v(16,34)!='成交量（吨）' or v(17,33)!='新疆棉' or v(19,33)!='总计':
        return None
    from .scanner import complete_date
    if not complete_date(v(15,33)) or not complete_date(v(147,1)):
        return None
    def metric(c,r):
        title=str(v(r,c))
        unit=re.search(r'[（(](吨|元/吨|元|%|家)[）)]',title)
        return {'column':get_column_letter(c),'metric':title,'unit':unit.group(1) if unit else None,
                'header_cells':[f'{get_column_letter(c)}{r}']}
    blocks=[
        {'name':'日度主表','date_column':'A','date_mode':'date','header_rows':[4],
         'data_start_row':5,'data_end_row':147,'columns':[metric(c,4) for c in range(2,33) if v(4,c)]},
        {'name':'国储理论库存','date_column':'A','date_mode':'date','header_rows':[4],
         'data_start_row':90,'data_end_row':147,'columns':[metric(33,4)]},
        {'name':'按产地划分的成交小表','date_mode':'shared_cell','date_cell':'AG15','category_column':'AG','category_name':'类别',
         'header_rows':[16],'data_start_row':17,'data_end_row':19,'columns':[metric(c,16) for c in range(34,40)]}
    ]
    regions=[('A4:AF147','included','日度主表；每行使用A列日期。'),
             ('AG90:AG147','included','国储理论库存；使用同一行A列日期，不包含上方的小表或零散计算。'),
             ('AG15:AM19','included','独立成交小表；共用AG15日期，AG17:AG19区分新疆棉、地产棉和总计。'),
             ('AG5:AM10','needs_review','独立小表，未找到可靠所属日期；不使用左侧A列日期。'),
             ('AG57:AL59','needs_review','独立小表，所属日期和行类别不明确，暂不导入。'),
             ('AG22:AG22','needs_review','零散数值缺少明确指标口径，暂不导入。'),
             ('AG28:AH28','needs_review','独立计算结果，不能当作国储库存。'),
             ('AH107:AH107','needs_review','独立比例计算，缺少明确指标说明。'),
             ('D154:F154','needs_review','底部无日期的计算区域，暂不导入。')]
    from openpyxl.utils import range_boundaries
    details=[]
    for area,status,reason in regions:
        c1,r1,c2,r2=range_boundaries(area)
        # Small previews include the local table headers, not only workbook's top-left corner.
        preview=[]
        for rn in range(r1,min(r2,r1+5)+1):
            row=[]
            for cn in range(c1,min(c2,c1+7)+1):
                value=v(rn,cn)
                row.append({'cell':f'{get_column_letter(cn)}{rn}', 'value':value.isoformat() if hasattr(value,'isoformat') else value})
            preview.append(row)
        details.append({'range':area,'status':status,'reason':reason,'preview':preview})
    return {'version':PROFILE_VERSION,'blocks':blocks,'regions':details}


def apply_profile(sheet, profile):
    sheet['layout']='mixed_regions'
    sheet['status']='needs_review'
    sheet['mapping']={'sheet':sheet['name'],'fingerprint':sheet['fingerprint'],'blocks':deepcopy(profile['blocks'])}
    sheet['regions']=profile['regions']
    sheet['region_profile']=profile['version']
    sheet['reasons']=['发现主表与独立小表混排。已生成3个可预览区块；标为待核对的区域不参与导入，日期不明处不会套用主表日期。']


def validate_region_mapping(mapping, profile):
    """Prevent old whole-column mappings from silently assigning wrong dates/metrics."""
    if not profile:return
    for b in mapping['blocks']:
        start,end=b['data_start_row'],b.get('data_end_row')
        for c in b['columns']:
            col=column_index_from_string(c['column'])
            if col<=32:
                valid=(start>=5 and end is not None and end<=147 and b.get('date_mode')=='date' and b.get('date_column')=='A')
            elif col==33:
                valid=(start>=90 and end is not None and end<=147 and b.get('date_mode')=='date' and b.get('date_column')=='A')
            else:
                valid=(34<=col<=39 and start>=17 and end is not None and end<=19 and b.get('date_mode')=='shared_cell' and b.get('date_cell')=='AG15' and b.get('category_column')=='AG')
            if not valid:
                raise ValueError(f'2018年抛储包含独立小表，{c["column"]}列当前范围或日期来源不可靠。请使用更新后的三个区块；主表到AF147，库存仅AG90:AG147，右侧成交小表共用AG15日期。日期不明区域需另行核实，不能整列套用A列日期。')

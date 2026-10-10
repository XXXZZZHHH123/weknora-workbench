"""One source-bound answer for UI, API and exports; no model-authored numbers."""


def summarize(answer):
    def fmt(v):return format(v,'.10g') if isinstance(v,(int,float)) else str(v)
    lines=[]
    if answer.get('intent')=='metadata':
        lines.append('原表共'+str(answer['count'])+'种：'+'、'.join(g['metric'] for g in answer['groups']))
        for unit,names in answer.get('unit_groups',{}).items():lines.append(unit+'：'+'、'.join(names))
        for g in answer['groups']:
            if g.get('definition'):lines.append(g['metric']+'（概念说明）：'+g['definition'])
            for s in g['sources']:lines.append(g['metric']+'；来源 '+answer['file']+' / '+s['sheet']+' / '+','.join(s['header_cells']))
    for role,source in answer.get('sources',{}).items():
        s=source['series'];unit=s.get('unit') or '原表未注明单位'
        lines.append(s['metric']+'（'+unit+'）')
        if source.get('aggregate') is not None:lines.append('统计值：'+fmt(source['aggregate']))
        rows=source.get('window_rows') or (source.get('rows',[]) if source.get('rows_are')!='source_examples_not_all_inputs' else [])
        if not rows and source.get('status')=='no_data':lines.append('指定范围没有有效记录，不采用相邻日期替代。')
        for r in rows[:200]:
            lines.append((r.get('date') or r.get('period') or '')+'：'+(fmt(r['value']) if r['value'] is not None else '无有效数值')+'；来源 '+source['source']['file']+' / '+s['sheet']+' / '+r['source_cell']+'；日期单元格 '+str(r.get('date_cell') or '无'))
        if any(r.get('formula') and r.get('value')==0 for r in rows):lines.append('原表公式保存的零值不能据此证明真实成交价为零。')
        if source.get('missing_count'):lines.append('缺失或错误记录 '+str(source['missing_count'])+' 条，不补零。')
    if 'rounded_value' in answer:lines.append('计算结果：'+answer['rounded_value']+' '+answer.get('unit',''))
    for o in answer.get('outputs',[]):lines.append(o['label']+'：'+o['rounded_value']+' '+o['unit'])
    a=answer.get('analysis')
    if a:
        lines.append(a['label'])
        for k in ('value','change','max_drawdown_percent','sample_count','target_date'):
            if k in a:lines.append({'value':'结果','change':'首尾差','max_drawdown_percent':'最大回撤（%）','sample_count':'有效样本数','target_date':'预测日期'}[k]+'：'+fmt(a[k]))
        for k,label in [('first','首个记录'),('last','最后记录'),('last_observation','最后有效历史记录')]:
            if k in a:lines.append(label+'：'+a[k]['date']+' / '+fmt(a[k]['value'])+' / '+a[k]['source_cell'])
        if a.get('notice'):lines.append(a['notice'])
    for key in ('explanation','notice'):
        if answer.get(key):lines.append(answer[key])
    return '\n'.join(lines)

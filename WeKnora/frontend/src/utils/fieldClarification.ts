/** Only structured results from the dedicated field tools may open a dialog. */
export interface Candidate { series_id: string; sheet: string; metric: string; unit?: string; reason?: string; header_cells?: string[]; dimensions?: Record<string, unknown>; chart_sheet?: string }
export interface Field { role: string; label: string; candidates: Candidate[] }
export interface Clarification { kind: string; status: string; resolution_id: string; question: string; original_question?: string; source_tool?: string; requires_note?: boolean; fields: Field[]; answer?: any }

export function canConfirmFields(plan: Clarification | null, selections: Record<string, string>, note: string): boolean {
  if (!plan) return false;
  if (note.trim()) return true;
  const selectable = plan.fields.filter(f => f.candidates.length > 0);
  const complete = selectable.length > 0 && selectable.every(f => f.candidates.some(c => c.series_id === selections[f.role]));
  // A pending calculation must not prevent the user from resolving an actual field choice first.
  return complete && (!plan.requires_note || plan.fields.some(f => f.candidates.length > 1));
}

export function fieldResolution(events: any[]): Clarification | null {
  let latest: Clarification | null = null;
  for (const event of events || []) {
    if (event.type !== 'tool_call' || event.pending || event.success === false || !/^(?:mcp__)?(?:resolve_fields|confirm_fields)(?:_[a-z0-9]+)?$/.test(event.tool_name || '')) continue;
    const chunks = (event.tool_data?.content_items || []).filter((x: any) => x.type === 'text').map((x: any) => x.text);
    chunks.push(event.tool_data?.output, event.output);
    for (const chunk of chunks) {
      if (typeof chunk !== 'string' || chunk.length > 100000) continue;
      try {
        const data = JSON.parse(chunk.slice(chunk.indexOf('{')));
        if (data.kind !== 'structured_field_resolution' || !/^[a-f0-9]{24}$/.test(data.resolution_id) || !['needs_clarification', 'resolved', 'no_data'].includes(data.status)) continue;
        if (!Array.isArray(data.fields) || data.fields.length > 8 || typeof data.question !== 'string') continue;
        if (!data.fields.every((f: any) => /^v[1-8]$/.test(f.role) && typeof f.label === 'string' && Array.isArray(f.candidates) && f.candidates.length <= 5 && f.candidates.every((c: any) => typeof c.series_id === 'string' && typeof c.sheet === 'string' && typeof c.metric === 'string'))) continue;
        latest = {...data, source_tool: event.tool_name};
        break;
      } catch { /* Other tool text is not a clarification request. */ }
    }
  }
  return latest;
}

export function confirmationText(plan: Clarification, selections: Record<string, string>, note: string): string {
  if (!canConfirmFields(plan, selections, note)) throw new Error('请选择字段或补充说明');
  const selected = Object.fromEntries(plan.fields.flatMap(f => f.candidates.some(c => c.series_id === selections[f.role]) ? [[f.role, selections[f.role]]] : []));
    return '字段确认提交：' + JSON.stringify({resolution_id: plan.resolution_id, source_tool: plan.source_tool, selections: selected, note: note.trim(), summary: plan.fields.flatMap(f => {
      const c = f.candidates.find(c => c.series_id === selected[f.role]);
      return c ? [`${f.label}：“${c.sheet}”的“${c.metric}”${dimensionLabel(c.dimensions) ? '（'+dimensionLabel(c.dimensions)+'）' : ''}`] : [];
    }).join('\n')});
}

/** Display-only summary of our generated confirmation envelopes, including saved history.
 * The stored message and the query sent to the model remain unchanged.
 */
export function confirmationDisplayText(content: string): string {
  if (content.startsWith('字段确认提交：')) {
    try {
      const data = JSON.parse(content.slice('字段确认提交：'.length));
      if (typeof data.summary === 'string' && typeof data.note === 'string') return [data.summary.trim() ? `已确认：\n${data.summary}` : '', data.note.trim() ? `已补充：${data.note}` : ''].filter(Boolean).join('\n');
    } catch { /* Keep ordinary user messages intact. */ }
  }
  const note = /^字段确认：关于“[\s\S]*”，我的补充说明是：([\s\S]*)。请依据这条补充重新确认字段，仍有歧义时继续问我。$/.exec(content);
  if (note) return `已补充：${note[1]}`;
  const selection = /^字段确认：\n([\s\S]+)\n请使用我确认的字段继续回答刚才的问题；如计算口径仍不明确，请继续问我。$/.exec(content);
  if (selection) return '已确认：\n' + selection[1].split('\n').map(line =>
    line.replace('：选择工作表“', '：“').replace(/（单位：[\s\S]*；表头位置：[\s\S]*）$/, '')
  ).join('\n');
  return content;
}

export function fieldClarification(events: any[]): Clarification | null {
  if (structuredFieldIssue(events)) return null;
  const latest = fieldResolution(events);
  return latest?.status === 'needs_clarification' ? latest : null;
}
export function structuredFieldAnswer(events: any[]): Clarification | null {
  if (structuredFieldIssue(events)) return null;
  const latest = fieldResolution(events);
  if (latest?.status === 'needs_clarification') return null;
  if (latest && ['resolved','no_data'].includes(latest.status) && latest.answer && typeof latest.answer.sources === 'object') return latest;
  // A follow-up can reuse the already identified series and call query_series
  // directly. Render the same row-bound evidence rather than an LLM paraphrase.
  let result: Clarification | null = null;
  for (const event of events || []) {
    if (event.type !== 'tool_call' || event.pending || !/^(?:mcp__)?query_series(?:_[a-z0-9]+)?$/.test(event.tool_name || '')) continue;
    result = null;
    if (event.success === false) continue;
    const chunks = [...(event.tool_data?.content_items || []).filter((x:any)=>x.type === 'text').map((x:any)=>x.text), event.tool_data?.output, event.output];
    for (const chunk of chunks) {
      if (typeof chunk !== 'string' || chunk.length > 100000) continue;
      try {
        const data=JSON.parse(chunk.slice(chunk.indexOf('{')));
        if (!['max','min'].includes(data.filters?.operation) || data.rows_are !== 'extremum_observations' || !Array.isArray(data.rows) || !data.source?.sheet || !data.series?.series_id) continue;
        if (!data.rows.every((r:any)=>typeof r.source_cell==='string' && r.value===data.aggregate && typeof r.value==='number')) continue;
        result={kind:'structured_query_answer',status:'resolved',resolution_id:'',question:'',fields:[],answer:{intent:'lookup',operation:data.filters.operation,start_date:data.filters.start_date,end_date:data.filters.end_date,sources:{v1:data}}};
        break;
      } catch { /* Only the structured query contract is displayed. */ }
    }
  }
  return result;
}

export function dimensionLabel(dimensions?: Record<string, unknown>): string {
  return Object.entries(dimensions || {}).filter(([key, value]) => key !== 'block' && ['string','number'].includes(typeof value)).map(([,value]) => String(value)).join(' · ');
}

/** Surface structured failures even when the outer model produces an empty final reply. */
export function structuredFieldIssue(events: any[]): string | null {
  let issue: string | null = null;
  for (const event of events || []) {
    if(event.type !== 'tool_call' || event.pending || !/^(?:mcp__)?(?:resolve_fields|confirm_fields)(?:_[a-z0-9]+)?$/.test(event.tool_name || '')) continue;
    if(event.success === false) {
      const error = String(event.error || event.tool_data?.error || '');
      issue = /指定范围|工作表|目录过大/.test(error)
        ? '结构化查询未完成：工具传入的工作表范围无效，或字段目录超出处理范围。系统没有取得数值；这不表示原文件缺少数据。'
        : '结构化查询未完成。工具调用失败，尚未取得可验证的数值。请展开调用详情查看原因。';
      continue;
    }
    const chunks = (event.tool_data?.content_items || []).filter((x:any)=>x.type==='text').map((x:any)=>x.text);
    chunks.push(event.tool_data?.output,event.output);
    for(const chunk of chunks) {
      if(typeof chunk!=='string' || chunk.length>100000)continue;
      try {
        const data=JSON.parse(chunk.slice(chunk.indexOf('{')));
        if(data.kind==='structured_tool_notice' && typeof data.message==='string')issue=data.message.slice(0,1000);
        if(data.kind==='structured_field_resolution')issue=data.execution_error || null;
      } catch { /* Not our structured payload. */ }
    }
  }
  return issue;
}

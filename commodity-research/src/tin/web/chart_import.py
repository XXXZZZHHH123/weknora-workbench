"""Review native-chart suggestions and explicitly apply selected worksheets."""
from fastapi import APIRouter, HTTPException
from sqlalchemy import select

from tin.compute.chart_import import group_suggestions, propose_chart
from tin.integration.native_charts import native_chart_page
from tin.integration.workbench_scope import artifact_session
from tin.schemas.chart import ChartDefinition, NativeApplyInput, NativePreviewInput
from tin.web.chart_data import catalog_fields, cutoff, read_series
from tin.web.charts import artifact_query, create_row, require_scope, serialize

router = APIRouter(prefix='/api/sn/charts/import/native')


def suggestions_for(scope, charts, *, overrides=None):
    identifiers = [curve.series_id for chart in charts for curve in chart.curves
                   if curve.status == 'linked' and curve.series_id]
    fields = catalog_fields(scope, identifiers)
    # Latest dates are real observations from the indicator page projection,
    # never the native chart's cached values or the workbook's display names.
    series = read_series(list(fields), cutoff(), limit=1)
    latest = {sid: row['points'][-1]['date'] for sid, row in series.items()
              if row['points'] and row['points'][-1]['value'] is not None}
    return [propose_chart(chart, fields, latest,
                         start_override=(overrides or {}).get((chart.knowledge_id, chart.sheet))) for chart in charts]


@router.post('/preview')
def preview(body: NativePreviewInput):
    scope = require_scope()
    page = native_chart_page(scope, offset=body.offset, limit=body.limit)
    suggestions = suggestions_for(scope, page.charts)
    with artifact_session(scope) as session:
        refs = {ChartDefinition.model_validate_json(row.body).origin.ref for row in session.scalars(artifact_query())}
    for item in suggestions:
        item['already_imported'] = item['chart_key'] in refs
    return {'sheets': group_suggestions(suggestions), 'total': page.total, 'offset': page.offset,
            'next_offset': page.next_offset, 'page_count': len(page.charts)}


def selected_native_charts(scope, selections):
    charts, offset, total, version = {}, 0, None, None
    while True:
        page = native_chart_page(scope, offset=offset, limit=200)
        if total is None:
            total, version = page.total, page.version
        if page.total != total or page.version != version:
            raise HTTPException(409, '原表图表目录在分页读取期间已变化，请重新预览')
        for chart in page.charts:
            selection = selections.get((chart.knowledge_id, chart.sheet))
            if selection is None or selection.chart_keys is not None and chart.chart_key not in selection.chart_keys:
                continue
            if chart.chart_key in charts and chart != charts[chart.chart_key]:
                raise HTTPException(409, '原表图表在读取期间已变化，请重新预览')
            charts[chart.chart_key] = chart
        if page.next_offset is None:
            break
        offset = page.next_offset
    for key, selection in selections.items():
        selected = {chart.chart_key for chart in charts.values() if (chart.knowledge_id, chart.sheet) == key}
        if not selected or selection.chart_keys is not None and set(selection.chart_keys) - selected:
            raise HTTPException(409, '确认的工作表或图表已不在当前来源，请重新预览')
    return list(charts.values())


@router.post('/apply')
def apply(body: NativeApplyInput):
    scope = require_scope(write=True)
    selections = {(sheet.knowledge_id, sheet.sheet): sheet for sheet in body.sheets}
    if len(selections) != len(body.sheets):
        raise HTTPException(422, '请勿重复确认同一工作表')
    charts = selected_native_charts(scope, selections)
    suggestions = suggestions_for(scope, charts, overrides={key: value.start for key, value in selections.items()})
    created, skipped, rejected = [], [], []
    with artifact_session(scope) as session:
        # All source reads finish before the write lock. Serialize ref checks and
        # inserts in SQLite, the existing workbench artifact store, so two apply
        # requests cannot create the same origin concurrently.
        if session.get_bind().dialect.name == 'sqlite':
            session.connection().exec_driver_sql('BEGIN IMMEDIATE')
        else:
            from tin.models.workbench import WorkbenchStorageScope
            session.execute(select(WorkbenchStorageScope).where(
                WorkbenchStorageScope.workbench_id == scope.workbench_id).with_for_update()).all()
        refs = {ChartDefinition.model_validate_json(row.body).origin.ref: row.id
                for row in session.scalars(artifact_query())}
        for item in suggestions:
            ref = item['chart_key']
            if ref in refs:
                skipped.append({'chart_key': ref, 'id': refs[ref], 'reason': 'already_imported'})
                continue
            if not item['convertible']:
                rejected.append({'chart_key': ref, 'reason': item['reason'], 'missing_curves': item['missing_curves']})
                continue
            definition = ChartDefinition.model_validate(item['definition'])
            snapshots = [{'kind': 'native_chart', 'chart_key': ref, 'axes': item['axes'], 'curves': item['source_curves']}]
            row = create_row(session, scope, definition, snapshots=snapshots)
            refs[ref] = row.id
            created.append(serialize(row, definition=definition))
        session.commit()
    return {'created': created, 'skipped': skipped, 'unconvertible': rejected,
            'sheets': group_suggestions(suggestions)}

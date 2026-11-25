"""Preview and save native charts by worksheet or by directory page."""
import hashlib

from fastapi import APIRouter, HTTPException
from sqlalchemy import select

from tin.compute.chart_import import group_suggestions, propose_chart
from tin.integration.native_charts import native_chart_page
from tin.integration.workbench_scope import artifact_session
from tin.schemas.chart import ChartDefinition, NativeApplyInput, NativeApplyPageInput, NativePreviewInput
from tin.web.chart_data import catalog_fields, catalog_fields_with_version, cutoff, read_series
from tin.web.charts import artifact_query, create_row, require_scope, serialize
from tin.web.report_chart_definitions import duplicate_signature

native_router = APIRouter(prefix='/api/sn/charts/import/native')


def _identifiers(charts):
    return [curve.series_id for chart in charts for curve in chart.curves
            if curve.status == 'linked' and curve.series_id]


def suggestions_for(scope, charts, *, overrides=None):
    return _suggestions(charts, catalog_fields(scope, _identifiers(charts)), overrides=overrides)


def _suggestions(charts, fields, *, overrides=None):
    # Latest dates are real observations from the indicator page projection,
    # never the native chart's cached values or the workbook's display names.
    series = read_series(list(fields), cutoff(), limit=1)
    latest = {sid: row['points'][-1]['date'] for sid, row in series.items()
              if row['points'] and row['points'][-1]['value'] is not None}
    return [propose_chart(chart, fields, latest,
                         start_override=(overrides or {}).get((chart.knowledge_id, chart.sheet))) for chart in charts]


def _page_suggestions(scope, *, offset, limit, expected_version=None):
    page = native_chart_page(scope, offset=offset, limit=limit)
    identifiers = _identifiers(page.charts)
    version, fields = page.version, None
    if version is None:
        # The structured service's unversioned native page uses the same
        # authorized workbook snapshots and sheet confirmations as its catalog.
        # Reuse the version from the field lookup, independent of requested IDs.
        fields, catalog_version = catalog_fields_with_version(scope, identifiers)
        version = hashlib.sha256(f'{catalog_version}:{page.total}'.encode()).hexdigest()
    if expected_version is not None and expected_version != version:
        raise HTTPException(409, '原表图表目录已变化，请重新开始全部导入')
    if fields is None:
        fields = catalog_fields(scope, identifiers)
    return page, _suggestions(page.charts, fields), version


@native_router.post('/preview')
def preview(body: NativePreviewInput):
    scope = require_scope()
    page, suggestions, version = _page_suggestions(scope, offset=body.offset, limit=body.limit)
    with artifact_session(scope) as session:
        refs = {ChartDefinition.model_validate_json(row.body).origin.ref for row in session.scalars(artifact_query())}
    for item in suggestions:
        item['already_imported'] = item['chart_key'] in refs
    return {'sheets': group_suggestions(suggestions), 'total': page.total, 'offset': page.offset,
            'next_offset': page.next_offset, 'page_count': len(page.charts), 'version': version}


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


def original_native_chart(scope, chart_key):
    """Resolve a saved origin only through the current authorized directory."""
    found, offset, total, version = None, 0, None, None
    while True:
        page = native_chart_page(scope, offset=offset, limit=200)
        if total is None:
            total, version = page.total, page.version
        if page.total != total or page.version != version:
            raise HTTPException(409, '原表图表目录在分页读取期间已变化，请重新转换')
        for chart in page.charts:
            if chart.chart_key != chart_key:
                continue
            if found is not None and chart != found:
                raise HTTPException(409, '原表图表在读取期间已变化，请重新转换')
            found = chart
        if page.next_offset is None:
            break
        offset = page.next_offset
    if found is None:
        raise HTTPException(409, '原表图表已不在当前来源，请核对来源授权')
    return found


def _save_suggestions(scope, suggestions):
    created, skipped, rejected = [], [], []
    with artifact_session(scope) as session:
        # All source reads finish before the write lock. Serialize duplicate
        # checks and inserts so simultaneous native imports cannot save the same chart.
        if session.get_bind().dialect.name == 'sqlite':
            session.connection().exec_driver_sql('BEGIN IMMEDIATE')
        else:
            from tin.models.workbench import WorkbenchStorageScope
            session.execute(select(WorkbenchStorageScope).where(
                WorkbenchStorageScope.workbench_id == scope.workbench_id).with_for_update()).all()
        refs, signatures = {}, {}
        for row in session.scalars(artifact_query()):
            definition = ChartDefinition.model_validate_json(row.body)
            refs[definition.origin.ref] = row.id
            signatures.setdefault(duplicate_signature(definition), row.id)
        for item in suggestions:
            ref = item['chart_key']
            if ref in refs:
                skipped.append({'chart_key': ref, 'id': refs[ref], 'reason': 'already_imported'})
                continue
            if not item['convertible']:
                rejected.append(item)
                continue
            definition = ChartDefinition.model_validate(item['definition'])
            signature = duplicate_signature(definition)
            if signature in signatures:
                skipped.append({'chart_key': ref, 'id': signatures[signature], 'reason': 'same_indicators'})
                continue
            snapshots = [{'kind': 'native_chart', 'chart_key': ref, 'axes': item['axes'], 'curves': item['source_curves']}]
            row = create_row(session, scope, definition, snapshots=snapshots)
            refs[ref] = row.id
            signatures[signature] = row.id
            created.append(serialize(row, definition=definition))
        session.commit()
    return created, skipped, rejected


@native_router.post('/apply')
def apply(body: NativeApplyInput):
    scope = require_scope(write=True)
    selections = {(sheet.knowledge_id, sheet.sheet): sheet for sheet in body.sheets}
    if len(selections) != len(body.sheets):
        raise HTTPException(422, '请勿重复确认同一工作表')
    charts = selected_native_charts(scope, selections)
    suggestions = suggestions_for(scope, charts, overrides={key: value.start for key, value in selections.items()})
    created, skipped, rejected = _save_suggestions(scope, suggestions)
    return {'created': created, 'skipped': skipped,
            'unconvertible': [{key: item[key] for key in ('chart_key', 'reason', 'missing_curves')} for item in rejected],
            'sheets': group_suggestions(suggestions)}


@native_router.post('/apply-page')
def apply_page(body: NativeApplyPageInput):
    scope = require_scope(write=True)
    page, suggestions, version = _page_suggestions(scope, offset=body.offset, limit=body.limit,
                                                   expected_version=body.version)
    created, skipped, rejected = _save_suggestions(scope, suggestions)
    keys = ('chart_key', 'title', 'sheet', 'source_file', 'reason', 'missing_curves')
    return {'created_count': len(created), 'skipped_count': len(skipped), 'skipped': skipped,
            'unconvertible': [{key: item[key] for key in keys} for item in rejected],
            'next_offset': page.next_offset, 'total': page.total, 'version': version}


# Keep the existing native-chart endpoints and workflow independent.
from tin.web.report_chart_import import router as report_router
router = APIRouter()
router.include_router(native_router)
router.include_router(report_router)

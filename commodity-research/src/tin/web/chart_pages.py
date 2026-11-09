"""HTML shells for chart.v1. Data and write decisions stay in the existing API."""
from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import RedirectResponse
from urllib.parse import urlencode

from tin.integration.identity import research_url
from tin.integration.workbench_scope import current_workbench
from tin.web.charts import require_scope


def page_config(request, chart_id=None):
    scope = current_workbench.get()
    config = {'enabled': True, 'id': chart_id, 'can_write': False, 'date': request.query_params.get('date'),
              'write_reason': '当前账号没有研究工作台编辑权限'}
    try:
        require_scope()
    except HTTPException as error:
        return {**config, 'enabled': False, 'reason': error.detail, 'status': error.status_code}
    config['can_write'] = scope.can_edit
    if config['can_write']:
        config['write_reason'] = ''
    return config


def build_router(page_session, shell, templates):
    router = APIRouter()

    def render(request, template, chart_id=None):
        scope = current_workbench.get()
        config = page_config(request, chart_id)
        if not config['enabled']:
            return templates.TemplateResponse(request, template,
                {'nav': 'indicators', 'ticker': [], 'names': {}, 'd': None,
                 'restricted_view': bool(scope and scope.restricted), 'chart_page': config},
                status_code=config['status'])
        # Only the shell is projected here. Curves are fetched by the bounded chart API.
        with page_session('indicators', series_ids=[], points_per_series=1) as session:
            return templates.TemplateResponse(request, template, shell(session, 'indicators', chart_page=config))

    @router.get('/sn/charts')
    def charts_page(request: Request):
        query = [(key, value) for key, value in request.query_params.multi_items() if key != 'tab']
        return RedirectResponse(research_url('/sn/indicators') + '?' + urlencode([('tab', 'charts'), *query]), status_code=307)

    # The literal import path must precede the dynamic ID path.
    @router.get('/sn/charts/import')
    def import_page(request: Request):
        return render(request, 'chart_import.html')

    @router.get('/sn/charts/{chart_id}')
    def chart_page(request: Request, chart_id: str):
        return render(request, 'chart_detail.html', chart_id)

    return router

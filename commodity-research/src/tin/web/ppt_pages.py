"""Personal PPT HTML shells; source access and writes stay in the PPT API."""
from fastapi import APIRouter, HTTPException, Request

from tin.integration.workbench_scope import current_workbench
from tin.web.ppt import require_scope


def build_router(page_session, shell, templates):
    router = APIRouter()

    def render(request, template, template_id=None):
        scope = current_workbench.get()
        config = {'enabled': True, 'id': template_id, 'can_write': False,
                  'write_reason': '当前账号没有研究工作台编辑权限，无法上传、修改、删除或生成PPT'}
        try:
            require_scope()
        except HTTPException as error:
            config.update(enabled=False, reason=error.detail)
            return templates.TemplateResponse(request, template,
                {'nav': 'ppt', 'ticker': [], 'names': {}, 'd': None,
                 'restricted_view': bool(scope and scope.restricted), 'ppt_page': config},
                status_code=error.status_code)
        config['can_write'] = scope.can_edit
        if config['can_write']:
            config['write_reason'] = ''
        with page_session('indicators', series_ids=[], points_per_series=1) as session:
            return templates.TemplateResponse(request, template, shell(session, 'ppt', ppt_page=config))

    @router.get('/sn/ppt')
    def ppt_page(request: Request):
        return render(request, 'ppt.html')

    @router.get('/sn/ppt/{template_id}/export')
    def ppt_export_page(request: Request, template_id: str):
        return render(request, 'ppt_export.html', template_id)

    @router.get('/sn/ppt/{template_id}')
    def ppt_detail_page(request: Request, template_id: str):
        return render(request, 'ppt_detail.html', template_id)

    return router

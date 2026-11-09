"""The disabled PPT feature rejects direct URLs before business reads or writes."""
from types import SimpleNamespace

from fastapi import FastAPI
from fastapi.testclient import TestClient
import pytest

from tin.config import Settings, settings
from tin.web import ppt
from tin.web.ppt_pages import build_router


@pytest.mark.parametrize('method,path', [
    ('GET', '/api/sn/ppt-templates?limit=invalid'),
    ('POST', '/api/sn/ppt-templates'),
    ('GET', '/api/sn/ppt-templates/sources'),
    ('POST', '/api/sn/ppt-templates/from-source'),
    ('GET', '/api/sn/ppt-templates/absent'),
    ('PATCH', '/api/sn/ppt-templates/absent/source'),
    ('PATCH', '/api/sn/ppt-templates/absent/bindings'),
    ('DELETE', '/api/sn/ppt-templates/absent'),
    ('POST', '/api/sn/ppt-templates/absent/export'),
    ('GET', '/api/sn/ppt-templates/absent/exports/absent'),
    ('GET', '/sn/ppt'),
    ('GET', '/sn/ppt/absent'),
    ('GET', '/sn/ppt/absent/export'),
    ('GET', '/sn/reports/ppt/absent'),
    ('GET', '/sn/reports/ppt/absent/export'),
])
def test_disabled_ppt_routes_return_404_without_business_access(monkeypatch, method, path):
    assert Settings.model_fields['ppt_enabled'].default is False
    monkeypatch.setattr(settings, 'ppt_enabled', False)

    def unexpected(*args, **kwargs):
        pytest.fail('Disabled PPT routes must not read sources, touch storage, or render a PPT page')

    for name in ('current_workbench', 'artifact_session', 'storage_root', 'save_original',
                 'read_original', 'export_path', 'parse_pptx'):
        monkeypatch.setattr(ppt, name, unexpected)
    for name in ('list_ppt_sources', 'source_inventory', 'get_ppt_source', 'read_ppt_source'):
        monkeypatch.setattr(ppt.ppt_sources, name, unexpected)
    app = FastAPI()
    app.include_router(ppt.router)
    app.include_router(build_router(unexpected, unexpected, SimpleNamespace(TemplateResponse=unexpected)))
    # Even malformed JSON and query parameters must stay behind the disabled gate.
    with TestClient(app) as client:
        response = client.request(method, path, content=b'not-json', headers={'content-type': 'application/json'})
    assert response.status_code == 404, response.text
    assert response.json() == {'detail': 'PPT功能暂未开放'}

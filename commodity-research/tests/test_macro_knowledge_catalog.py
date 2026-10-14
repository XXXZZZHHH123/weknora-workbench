from fastapi.testclient import TestClient
from sqlalchemy import func, select

from tin.export.indicators import catalog
from tin.integration.auth import bootstrap_html
from tin.models import Indicator
from tin.web.app import app


def test_macro_catalog_reads_real_metadata_without_registering_sources(session, monkeypatch):
    monkeypatch.setattr('tin.web.app.SessionLocal', lambda: session)
    before = session.scalar(select(func.count()).select_from(Indicator))
    expected = [row for row in catalog(session) if row['macro']]
    response = TestClient(app).get('/api/sn/indicators/catalog?scope=macro')
    assert response.status_code == 200
    assert response.json()['indicators'] == expected
    assert expected and all(row['macro'] for row in response.json()['indicators'])
    assert session.scalar(select(func.count()).select_from(Indicator)) == before
    assert TestClient(app).get('/api/sn/indicators/catalog?scope=invalid').status_code == 400


def test_indicator_bootstrap_uses_encoded_local_target_and_preserves_source_priority():
    response = bootstrap_html(indicator='用户指标/<script>')
    html = response.body.decode()
    assert '/sn/indicators/' in html and '%2F%3Cscript%3E' in html
    assert '用户指标/<script>' not in html
    assert '/files?source=' in bootstrap_html(source='a' * 64, indicator='ignored').body.decode()
    assert '/sn/indicators/' not in bootstrap_html(indicator='bad\nvalue').body.decode()

"""Actual OOXML style output must remain readable by the research chart client."""
import importlib.util
from pathlib import Path
from types import SimpleNamespace
from xml.etree import ElementTree as ET

import httpx
from pydantic import ValidationError
import pytest

from tin.compute.chart_import import propose_chart
from tin.integration.native_charts import NativeStyle, native_chart_page
from tests.chart_fixtures import FIELDS, native_chart, native_curve


@pytest.fixture(scope='module')
def source_parser():
    # Load the producer's real stdlib-only parser without requiring its separate
    # service dependencies or adding the other component's tests to sys.path.
    path = Path(__file__).resolve().parents[2] / 'structured-data/src/structured_data/native_charts.py'
    spec = importlib.util.spec_from_file_location('native_chart_style_producer', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def read_page(monkeypatch, style):
    payload = {'charts': [native_chart(curves=[native_curve(style=style)]),
                          native_chart(chart_key='nc-' + '2' * 32)],
               'total': 2, 'offset': 0, 'next_offset': None}

    def transport(method, path, body, **kwargs):
        assert (method, path, body) == ('POST', '/api/research/native-charts', {'offset': 0, 'limit': 200})
        assert kwargs == {'return_error_response': True}
        return httpx.Response(200, json=payload, request=httpx.Request(method, 'http://fixture.invalid' + path))

    monkeypatch.setattr('tin.provenance.bridge.structured_request', transport)
    return native_chart_page(SimpleNamespace(restricted=False, scope_version=2,
                                             workbench_id='fixture', sources_revision=1))


@pytest.mark.parametrize('emu,expected', [(0, None), (1, None), (63, None), (64, 0.01), (12700, 1.0)])
def test_ooxml_width_round_trip_keeps_the_page_and_other_styles(source_parser, monkeypatch, emu, expected):
    ns = source_parser.NS
    series = ET.fromstring(f'''<c:ser xmlns:c="{ns['c']}" xmlns:a="{ns['a']}">
        <c:spPr><a:ln w="{emu}"><a:solidFill><a:srgbClr val="12ABCD"/></a:solidFill>
        <a:prstDash val="dot"/></a:ln></c:spPr></c:ser>''')
    style = source_parser._series_style(series)
    assert style == {'color': '#12ABCD', 'width_pt': expected, 'dash': 'dot'}
    page = read_page(monkeypatch, style)
    assert page.total == len(page.charts) == 2
    result = propose_chart(page.charts[0].model_dump(mode='json'), FIELDS)
    assert result['convertible']
    assert result['definition']['curves'][0]['style'] == {
        'color': '#12ABCD', 'width': expected, 'dash': 'dot'}


@pytest.mark.parametrize('width', [0, 0.0])
def test_previous_index_zero_width_is_unspecified_without_losing_other_styles(monkeypatch, width):
    page = read_page(monkeypatch, {'color': '#12ABCD', 'width_pt': width, 'dash': 'dash'})
    assert page.charts[0].curves[0].style.model_dump() == {
        'color': '#12ABCD', 'width_pt': None, 'dash': 'dash'}
    assert page.charts[1].curves[0].style.width_pt == 2.25


@pytest.mark.parametrize('width', [-1, float('nan'), float('inf'), float('-inf')])
def test_negative_and_nonfinite_widths_remain_invalid(width):
    with pytest.raises(ValidationError):
        NativeStyle.model_validate({'width_pt': width})

"""PPT downloads use real report scope, saved prose and ordered figure keys."""
from copy import deepcopy
from io import BytesIO
import json
from urllib.parse import unquote

from PIL import Image
from pptx import Presentation
import pytest

from tin.config import settings
from tin.web import report_ppt
from tin.web.report_view import present_snapshot
from tests.chart_fixtures import SID, OTHER
from tests.test_chart_api import chart_client  # noqa: F401
from tests.test_chart_references import reference_client, create  # noqa: F401
from tests.test_report_chart_flow import report_flow  # noqa: F401


DAY = '2023-01-03'
URL = f'/api/sn/reports/{DAY}/weekly/ppt'
SECTIONS = dict.fromkeys(('summary', 'research', 'dashboard', 'watch'), True)


def png(width=640, height=360):
    result = BytesIO()
    Image.new('RGB', (width, height), 'white').save(result, format='PNG')
    return result.getvalue()


def slide_text(deck):
    return '\n'.join(shape.text for slide in deck.slides for shape in slide.shapes
                     if shape.has_text_frame)


@pytest.fixture
def ppt_client(report_flow):
    client, state = report_flow
    state.app.include_router(report_ppt.router)
    saved = client.post('/fixture/judgment', json={
        'variety': 'SN', 'author': 'first', 'written_at': '2023-01-02',
        'contradiction': {'text': '服务端保存的主要矛盾',
            'bull': {'claim': '服务端保存的支持观点',
                     'evidence': [{'desc': '服务端保存的依据', 'series_id': SID}]},
                'bear': {'claim': '服务端保存的反对观点',
                         'evidence': [{'desc': '服务端保存的反向依据', 'series_id': OTHER}]}},
    })
    assert saved.status_code == 200, saved.text
    draft = {'format': 'research-report-draft.v1', 'period': 'weekly', 'date': DAY,
        'title': '服务端保存的报告标题',
        'figures': [{'series_id': SID, 'caption': '服务端保存的图说甲'},
                    {'series_id': OTHER, 'caption': '服务端保存的图说乙'}],
        'sections': {key: {'visible': True, 'text': '服务端研究员按语'} for key in SECTIONS},
        'references': []}
    created = client.post('/api/sn/report-drafts', json={'draft': draft})
    assert created.status_code == 201, created.text
    state.ppt_draft = draft
    state.ppt_revision = created.json()['revision']
    state.ppt_id = created.json()['id']
    manifest = client.get(f'/sn/reports/{DAY}/weekly.json')
    assert manifest.status_code == 200, manifest.text
    state.ppt_keys = [row['key'] for row in manifest.json()['report_figures']]
    assert state.ppt_keys == ['indicator:' + SID, 'indicator:' + OTHER]
    return client, state


def export(client, state, *, options=None, revision='saved', keys=None,
           images=None, failures=(), headers=None, extra=(), url=URL):
    actual_keys = state.ppt_keys if keys is None else keys
    if images is None:
        failed_keys = {row['key'] for row in failures}
        images = [png() for key in actual_keys if key not in failed_keys]
    values = [
        ('options', (None, json.dumps(options or {'sections': SECTIONS, 'figures_per_slide': 2}))),
        ('revision', (None, json.dumps(state.ppt_revision if revision == 'saved' else revision))),
        ('keys', (None, json.dumps(actual_keys))),
        *[('failures', (None, json.dumps(row))) for row in failures],
        *[('images', (f'figure-{index}.png', value, 'image/png')) for index, value in enumerate(images)],
        *extra,
    ]
    return client.post(url, files=values, headers=headers or {})


def test_success_rebuilds_saved_report_and_downloads_utf8_filename(ppt_client):
    client, state = ppt_client
    response = export(client, state)
    assert response.status_code == 200, response.text
    assert response.headers['content-type'].startswith(
        'application/vnd.openxmlformats-officedocument.presentationml.presentation')
    disposition = response.headers['content-disposition']
    assert disposition.startswith('attachment;') and "filename*=UTF-8''" in disposition
    filename = unquote(disposition.split("filename*=UTF-8''", 1)[1])
    assert filename.endswith('.pptx') and '周报' in filename and DAY in filename
    deck = Presentation(BytesIO(response.content))
    text = slide_text(deck)
    assert '服务端保存的报告标题' in text
    assert '服务端保存的主要矛盾' in text
    assert '服务端研究员按语' in text
    assert '服务端保存的图说甲' in text and '服务端保存的图说乙' in text
    assert 'fixture.xlsx' in text and '测试表' in text
    assert len(deck.slides) >= 5
    assert len(response.content) > 1000


@pytest.mark.parametrize('headers,status', [
    ({'x-restricted': 'true'}, 403),
    ({'x-local': 'true'}, 409),
])
def test_scope_denial(ppt_client, headers, status):
    client, state = ppt_client
    assert export(client, state, headers=headers).status_code == status


def test_report_reader_without_edit_permission_can_export(ppt_client):
    client, state = ppt_client
    response = export(client, state, headers={'x-role': 'viewer', 'x-can-edit': 'false'})
    assert response.status_code == 200, response.text
    assert '服务端保存的报告标题' in slide_text(Presentation(BytesIO(response.content)))


@pytest.mark.parametrize('change', ['missing', 'extra', 'order', 'revision', 'no_revision'])
def test_changed_manifest_or_revision_is_conflict(ppt_client, change):
    client, state = ppt_client
    keys, revision = state.ppt_keys[:], 'saved'
    if change == 'missing':
        keys.pop()
    elif change == 'extra':
        keys.append('indicator:unbound')
    elif change == 'order':
        keys.reverse()
    elif change == 'revision':
        revision = state.ppt_revision + 1
    else:
        revision = None
    response = export(client, state, keys=keys, revision=revision)
    assert response.status_code == 409, response.text
    assert '报告内容已变化，请刷新后重试' in response.json()['detail']


@pytest.mark.parametrize('images,status', [
    ([], 422),
    ([png()], 422),
    ([png(), png(), png()], 422),
    ([b'not a PNG', png()], 422),
    ([b'\x89PNG\r\n\x1a\n' + b'broken image', png()], 422),
    ([png(4001, 1), png()], 422),
    ([png(1, 4001), png()], 422),
    ([png() + b'x' * (4 * 1024 * 1024), png()], 413),
])
def test_png_and_count_validation(ppt_client, images, status):
    client, state = ppt_client
    response = export(client, state, images=images)
    assert response.status_code == status, response.text


def test_failed_figure_keeps_placeholder_and_ignores_client_prose(ppt_client):
    client, state = ppt_client
    response = export(client, state, failures=[{
        'key': state.ppt_keys[0], 'reason': '客户端伪造结论绝不应进入报告',
    }])
    assert response.status_code == 200, response.text
    deck = Presentation(BytesIO(response.content))
    text = slide_text(deck)
    assert '客户端伪造结论绝不应进入报告' not in text
    assert '服务端保存的图说甲' in text and '服务端保存的图说乙' in text
    assert '图表' in text


@pytest.mark.parametrize('failures', [
    [{'key': 'indicator:foreign', 'reason': '不可读'}],
    [{'key': 'same', 'reason': '不可读'}, {'key': 'same', 'reason': '不可读'}],
])
def test_failures_must_be_unique_current_keys(ppt_client, failures):
    client, state = ppt_client
    response = export(client, state, failures=failures)
    assert response.status_code == 422, response.text


@pytest.mark.parametrize('options', [
    {'sections': SECTIONS, 'figures_per_slide': 3},
    {'sections': SECTIONS, 'figures_per_slide': True},
    {'sections': SECTIONS, 'figures_per_slide': 2, 'title': '客户端替换标题'},
    {'sections': {**SECTIONS, 'summary': 'true'}, 'figures_per_slide': 2},
    {'sections': {**SECTIONS, 'new_section': True}, 'figures_per_slide': 2},
])
def test_options_are_strict_and_cannot_supply_prose(ppt_client, options):
    client, state = ppt_client
    assert export(client, state, options=options).status_code == 422


def test_multipart_cannot_supply_report_text(ppt_client):
    client, state = ppt_client
    response = export(client, state, extra=[('title', (None, '客户端替换的报告标题'))])
    assert response.status_code == 422, response.text


def test_withdrawn_indicator_source_discards_previously_rendered_png(ppt_client):
    client, state = ppt_client
    # The browser may already have rendered this image before permission/data
    # availability changes. The stable draft and keys alone cannot authorize it.
    del state.fields[SID]
    response = export(client, state)
    assert response.status_code == 200, response.text
    deck = Presentation(BytesIO(response.content))
    pictures = [shape.name for slide in deck.slides for shape in slide.shapes
                if shape.name.startswith('Report chart ')]
    assert 'Report chart indicator:' + SID not in pictures
    assert 'Report chart indicator:' + OTHER in pictures
    text = slide_text(deck)
    assert '图表读取失败' in text and '服务端保存的图说甲' in text


def test_chart_with_one_withdrawn_curve_discards_previously_rendered_png(ppt_client):
    client, state = ppt_client
    chart = create(client, title='多曲线来源核验图', curves=[
        {'series_id': SID, 'label': '仍可读曲线', 'kind': 'line', 'axis': 'left'},
        {'series_id': OTHER, 'label': '已撤回曲线', 'kind': 'line', 'axis': 'left'},
    ])
    saved = client.post('/fixture/judgment', json={
        'variety': 'SN', 'author': 'first', 'written_at': '2023-01-02',
        'contradiction': {
            'bull': {'claim': '已保存观点', 'evidence': [{'desc': '依据甲', 'series_id': SID}]},
            'bear': {'claim': '已保存反方', 'evidence': [{'desc': '依据乙', 'series_id': OTHER}]}},
        'chart_references': [{'kind': 'chart', 'id': chart['id'], 'revision': 1}],
    })
    assert saved.status_code == 200, saved.text
    current = client.get(f'/sn/reports/{DAY}/weekly.json')
    assert current.status_code == 200, current.text
    state.ppt_keys = [row['key'] for row in current.json()['report_figures']]
    assert state.ppt_keys == ['chart:' + chart['id'], 'indicator:' + SID, 'indicator:' + OTHER]
    del state.fields[OTHER]
    response = export(client, state)
    assert response.status_code == 200, response.text
    deck = Presentation(BytesIO(response.content))
    pictures = [shape.name for slide in deck.slides for shape in slide.shapes
                if shape.name.startswith('Report chart ')]
    assert 'Report chart chart:' + chart['id'] not in pictures
    assert 'Report chart indicator:' + OTHER not in pictures
    assert pictures == ['Report chart indicator:' + SID]
    text = slide_text(deck)
    assert '多曲线来源核验图' in text and '图表读取失败' in text


def test_chart_with_changed_axis_units_discards_previously_rendered_png(ppt_client):
    client, state = ppt_client
    chart = create(client, title='单位变化核验图', curves=[
        {'series_id': SID, 'label': '原单位曲线', 'kind': 'line', 'axis': 'left'},
        {'series_id': OTHER, 'label': '单位变化曲线', 'kind': 'line', 'axis': 'left'},
    ])
    saved = client.post('/fixture/judgment', json={
        'variety': 'SN', 'author': 'first', 'written_at': '2023-01-02',
        'contradiction': {
            'bull': {'claim': '已保存观点', 'evidence': [{'desc': '依据甲', 'series_id': SID}]},
            'bear': {'claim': '已保存反方', 'evidence': [{'desc': '依据乙', 'series_id': OTHER}]}},
        'chart_references': [{'kind': 'chart', 'id': chart['id'], 'revision': 1}],
    })
    assert saved.status_code == 200, saved.text
    current = client.get(f'/sn/reports/{DAY}/weekly.json')
    assert current.status_code == 200, current.text
    state.ppt_keys = [row['key'] for row in current.json()['report_figures']]
    state.fields[OTHER]['unit'] = '元'
    response = export(client, state)
    assert response.status_code == 200, response.text
    deck = Presentation(BytesIO(response.content))
    pictures = [shape.name for slide in deck.slides for shape in slide.shapes
                if shape.name.startswith('Report chart ')]
    assert 'Report chart chart:' + chart['id'] not in pictures
    assert 'Report chart indicator:' + SID in pictures
    assert 'Report chart indicator:' + OTHER in pictures
    text = slide_text(deck)
    assert '单位变化核验图' in text and '图表读取失败' in text
    assert '单位' in text


def test_only_selected_visible_sections_are_exported(ppt_client):
    client, state = ppt_client
    options = {'sections': dict.fromkeys(SECTIONS, False), 'figures_per_slide': 4}
    response = export(client, state, options=options, keys=[], images=[])
    assert response.status_code == 200, response.text
    deck = Presentation(BytesIO(response.content))
    assert len(deck.slides) == 2
    text = slide_text(deck)
    assert '服务端保存的报告标题' in text and '说明' in text
    assert '服务端保存的主要矛盾' not in text
    assert '服务端保存的图说甲' not in text


def test_hidden_sections_cannot_be_enabled_by_export(ppt_client):
    client, state = ppt_client
    draft = deepcopy(state.ppt_draft)
    draft['sections']['dashboard']['visible'] = False
    changed = client.patch('/api/sn/report-drafts/' + state.ppt_id,
                         json={'draft': draft, 'revision': state.ppt_revision})
    assert changed.status_code == 200, changed.text
    state.ppt_revision = changed.json()['revision']
    response = export(client, state)
    assert response.status_code in {409, 422}, response.text


def test_frozen_export_lists_chart_without_loading_or_accepting_png(ppt_client, monkeypatch):
    client, state = ppt_client
    view = present_snapshot({'as_of': DAY, 'observations': [],
        'period': {'kind': 'weekly', 'start': '2023-01-02', 'end': DAY}})
    view.update(workbench_name='合成冻结研究台', as_of=DAY, frozen=True,
        generated_at='2023-01-03T16:00:00+08:00', report_title='冻结归档报告',
        report_figures=[{'key': 'chart:frozen', 'kind': 'chart', 'title': '冻结图表标题',
                         'caption': '引用于：研判', 'unit': '吨', 'as_of': DAY}])
    monkeypatch.setattr(report_ppt, 'read_report_view', lambda *args, **kwargs: deepcopy(view))
    before = len(state.requests)
    response = export(client, state, keys=['chart:frozen'], revision=None, images=[])
    assert response.status_code == 200, response.text
    assert len(state.requests) == before, '冻结归档不重新读取来源或图表'
    text = slide_text(Presentation(BytesIO(response.content)))
    assert '冻结图表标题' in text and '引用于：研判' in text
    assert '历史归档不重新绘图' in text
    uploaded = export(client, state, keys=['chart:frozen'], revision=None, images=[png()])
    assert uploaded.status_code == 422, uploaded.text


def test_actual_archive_loader_does_not_reopen_chart_sources(ppt_client, monkeypatch, tmp_path):
    from tin.web import chart_references

    client, state = ppt_client
    monkeypatch.setattr(settings, 'weknora_api_url', '')
    monkeypatch.setattr(settings, 'exports_dir', tmp_path)
    archived = {'variety': 'SN', 'as_of': DAY, 'observations': [],
        'generated_at': '2023-01-03T16:00:00+08:00',
        'judgment': {'contradiction': {'text': '历史保存的主要矛盾'},
            'chart_references': [{'id': 'archive-only', 'revision': 1, 'kind': 'chart',
                'title': '历史冻结图表', 'definition': {'curves': [{'series_id': SID}]}}]}}
    folder = tmp_path / 'SN'
    folder.mkdir()
    (folder / f'{DAY}.json').write_text(json.dumps(archived, ensure_ascii=False), encoding='utf-8')

    def forbidden(*args, **kwargs):
        pytest.fail('冻结归档不能重读图表定义或图表数据')

    monkeypatch.setattr(chart_references, 'reference_definition', forbidden)
    monkeypatch.setattr(chart_references, 'render_chart_reference', forbidden)
    before = len(state.requests)
    response = export(client, state, url=f'/api/sn/reports/{DAY}/daily/ppt',
                      keys=['chart:archive-only'], revision=None, images=[])
    assert response.status_code == 200, response.text
    assert len(state.requests) == before
    text = slide_text(Presentation(BytesIO(response.content)))
    assert '历史保存的主要矛盾' in text and '历史冻结图表' in text
    assert '历史归档不重新绘图' in text and '引用于：研判' in text

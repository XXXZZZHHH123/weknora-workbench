"""A board day carried by navigation never turns a page into a raw error response."""
from tests.test_knowledge_facts import FIRST, canonical, page  # noqa: F401


def test_pages_render_a_carried_day_before_their_own_observations(canonical):
    client, headers, _, _ = canonical
    for path in ('/sn', '/sn/indicators', '/sn/judgment'):
        response = client.get(page(FIRST, path) + '?date=2000-01-03', headers=headers)
        assert response.status_code == 200, (path, response.text[:200])
        assert 'text/html' in response.headers['content-type']
        assert '还没有可展示的指标数据' not in response.text


def test_a_malformed_day_is_still_rejected(canonical):
    client, headers, _, _ = canonical
    assert client.get(page(FIRST, '/sn/indicators') + '?date=2000-13-40', headers=headers).status_code == 400



def _empty_notice(*, core_items, has_core_charts=False, has_selectable_items=False):
    from types import SimpleNamespace
    from tin.web import app as web
    workbench = SimpleNamespace(view_modules=('market', 'indicators'))
    return web.templates.env.get_template('_market_empty.html').render(
        research_workbench=lambda: workbench, research_url=lambda path: path, restricted_view=False,
        has_core_charts=has_core_charts,
        market_catalog={'core_items': core_items, 'has_selectable_items': has_selectable_items})


def test_overview_without_core_focus_asks_for_a_selection_instead_of_reporting_no_observations():
    # A knowledge-base workbench reads observations only for its core focus; an empty selection
    # is not evidence that the catalogue has no confirmed fields.
    html = _empty_notice(core_items=[])
    assert '尚未选择核心关注' in html and '选择与排序' in html
    # An empty workbench still gets the source-preparation guidance.
    assert '请在「资料与来源」准备并确认本知识库资料' in html
    available = _empty_notice(core_items=[], has_selectable_items=True)
    assert '尚未选择核心关注' in available
    assert '还没有可用指标时' not in available
    assert '还没有可展示的指标数据' not in html
    selected = _empty_notice(core_items=[{'kind': 'indicator', 'id': 'kb-x'}])
    assert '当前还没有可展示的指标数据' in selected
    assert '向下查看已关注的图表' in _empty_notice(core_items=[{'kind': 'chart', 'id': 'c'}], has_core_charts=True)

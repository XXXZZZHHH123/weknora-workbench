"""A board day carried by navigation never turns a page into a raw error response."""
from tests.test_knowledge_facts import FIRST, canonical, page  # noqa: F401


def test_pages_render_a_carried_day_before_their_own_observations(canonical):
    client, headers, _, _ = canonical
    for path in ('/sn', '/sn/indicators', '/sn/judgment'):
        response = client.get(page(FIRST, path) + '?date=2000-01-03', headers=headers)
        assert response.status_code == 200, (path, response.text[:200])
        assert 'text/html' in response.headers['content-type']
        assert '尚无已确认观测' not in response.text


def test_a_malformed_day_is_still_rejected(canonical):
    client, headers, _, _ = canonical
    assert client.get(page(FIRST, '/sn/indicators') + '?date=2000-13-40', headers=headers).status_code == 400

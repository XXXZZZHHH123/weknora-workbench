"""Keep the ASGI request loop alive for detached jobs without starting app services."""
from contextlib import contextmanager
from time import monotonic, sleep

from anyio.from_thread import start_blocking_portal
import pytest


@contextmanager
def detached_loop(client):
    if client.portal is not None:
        yield
        return
    with start_blocking_portal() as portal:
        client.portal = portal
        try:
            yield
        finally:
            client.portal = None


def finished_job(client, path, headers=None):
    deadline = monotonic() + 5
    while monotonic() < deadline:
        response = client.get(path, headers=headers)
        assert response.status_code == 200, response.text
        if response.json()['state'] not in {'queued', 'running'}:
            return response.json(), client.get(path + '/result', headers=headers)
        sleep(.02)
    pytest.fail('background job did not finish')

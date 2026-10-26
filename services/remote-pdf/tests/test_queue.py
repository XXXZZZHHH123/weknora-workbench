import asyncio
import json
import sys
import time
from pathlib import Path
import pytest
import httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from app import create_app
from store import Store, QueueFull

@pytest.fixture
def anyio_backend():
    return 'asyncio'


TOKEN = 'test-token-only-not-a-real-secret'
PDF = b'%PDF-1.4\nsynthetic queue fixture'


def settings(tmp_path, **kwargs):
    return dict(data_dir=str(tmp_path), token=TOKEN, max_upload_bytes=1024,
                max_job_output_bytes=2048, max_storage_bytes=1024*1024,
                disk_reserve_bytes=1, extractor={'model': 'fixture'}) | kwargs


def headers(scope='tenant1', **kwargs):
    return {'Authorization': 'Bearer '+TOKEN, 'X-Parse-Scope': scope} | kwargs


async def submit(client, content=PDF, scope='tenant1', owner=None):
    return await client.post('/v1/jobs', files={'file': ('x.pdf', content, 'application/pdf')},
                             headers=headers(scope, **({'X-Parse-Owner': owner} if owner else {})))


async def wait(client, job_id, target='completed', scope='tenant1'):
    for _ in range(100):
        result = (await client.get('/v1/jobs/'+job_id, headers=headers(scope))).json()
        if result['status'] == target:
            return result
        await asyncio.sleep(.02)
    raise AssertionError(result)


async def fixture_parser(source, output, *, config, page_semaphore, progress, cancelled):
    await progress({'stage':'extracting','completed_pages':0,'total_pages':1})
    async with page_semaphore:
        await asyncio.sleep(.01)
    return {'schema':'remote_pdf_v1','pages':[{'page_number':1}], 'bundle':{'tables':[]}}


def test_store_scope_limits_fairness_restart(tmp_path):
    store = Store(tmp_path/'jobs.db', global_limit=4, owner_limit=2)
    a, _ = store.insert('scope', 'alice', 'a', 'p', 1)
    b, _ = store.insert('scope', 'alice', 'b', 'p', 1)
    with pytest.raises(QueueFull):
        store.insert('scope','alice','c','p',1)
    c, _ = store.insert('other', 'bob', 'a','p',1)
    assert store.get(a['id'], 'other') is None
    assert store.insert('scope','alice','a','p',1)[1]
    assert store.take()['id'] == a['id']
    assert store.take()['id'] == c['id']
    store.progress(a['id'], {'page':3}, 12.5)
    store.recover()
    assert store.get(a['id'])['status'] == 'queued'
    assert store.get(a['id'])['execution_used'] == 12.5
    store.close()


@pytest.mark.anyio
async def test_auth_scope_dedupe_results(tmp_path):
    app = create_app(settings(tmp_path), fixture_parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        assert (await client.post('/v1/jobs', files={'file':('x.pdf',PDF)})).status_code == 401
        assert (await client.post('/v1/jobs', files={'file':('x.pdf',PDF)},headers={'Authorization':'Bearer '+TOKEN})).status_code == 400
        first = (await submit(client)).json()
        second = (await submit(client)).json()
        assert first['id'] == second['id'] and second['reused']
        assert first['lease_id'] != second['lease_id']
        assert (await client.get('/v1/jobs/'+first['id'],headers=headers('tenant2'))).status_code == 404
        assert (await client.get('/v1/jobs/'+first['id']+'/result',headers=headers('tenant2'))).status_code == 404
        other = (await submit(client, scope='tenant2')).json()
        assert other['id'] != first['id']
        await wait(client, first['id'])
        assert (await client.get('/v1/jobs/'+first['id']+'/result',headers=headers())).json()['schema'] == 'remote_pdf_v1'


@pytest.mark.anyio
async def test_lease_cancel_reused_job_isolation(tmp_path):
    entered, released = asyncio.Event(), asyncio.Event()
    async def parser(*args, **kwargs):
        entered.set()
        try:
            await asyncio.Event().wait()
        finally:
            released.set()
    app = create_app(settings(tmp_path), parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        a = (await submit(client)).json()
        b = (await submit(client)).json()
        await entered.wait()
        url = '/v1/jobs/'+a['id']
        assert (await client.delete(url,headers=headers())).status_code == 404
        assert (await client.delete(url,headers=headers('tenant2', **{'X-Parse-Lease':a['lease_id']}))).status_code == 404
        first = await client.delete(url,headers=headers(**{'X-Parse-Lease':a['lease_id']}))
        assert first.json()['status'] == 'running' and not released.is_set()
        last = await client.delete(url,headers=headers(**{'X-Parse-Lease':b['lease_id']}))
        assert last.json()['status'] == 'cancelled' and released.is_set()
        reused = (await submit(client)).json()
        assert reused['status'] == 'cancelled'  # Automatic submissions never restart paid work.
        retried = await client.post(url+'/retry',headers=headers(**{'X-Parse-Retry-Key':'manual-retry-1'}))
        assert retried.status_code == 202 and retried.json()['status'] == 'queued'


@pytest.mark.anyio
async def test_queue_pressure_and_body_bounds(tmp_path):
    async def parser(*args, **kwargs):
        await asyncio.Event().wait()
    app = create_app(settings(tmp_path, document_concurrency=1,global_outstanding=2,owner_outstanding=1), parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        assert (await submit(client)).status_code == 202
        assert (await submit(client, PDF+b'2')).status_code == 429
        assert (await submit(client, PDF+b'2',owner='bob')).status_code == 202
        assert (await submit(client, PDF+b'3',scope='tenant2')).status_code == 429
        assert (await submit(client)).json()['reused']
        assert (await submit(client,b'not pdf')).status_code == 422
        assert (await submit(client,PDF+b'x'*1024)).status_code == 413
        # Multipart payloads are capped before Starlette writes the full body to its spool.
        assert (await submit(client,PDF+b'x'*(2*1024*1024))).status_code == 413
        assert not list(tmp_path.glob('upload-*'))


@pytest.mark.anyio
async def test_queue_execution_timeout_separate_and_failure_sanitized(tmp_path):
    calls = 0
    async def parser(*args, **kwargs):
        nonlocal calls
        calls += 1
        await asyncio.Event().wait()
    app = create_app(settings(tmp_path, document_concurrency=1,queue_timeout_seconds=.1,execution_timeout_seconds=.4),parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        first = (await submit(client)).json()
        await wait(client,first['id'],'running')
        queued = (await submit(client,PDF+b'2')).json()
        q = await wait(client,queued['id'],'failed')
        assert q['error']['code'] == 'QUEUE_TIMEOUT'
        f = await wait(client,first['id'],'failed')
        assert f['error']['code'] == 'EXECUTION_TIMEOUT'
        assert calls == 1
        assert (await submit(client)).json()['status'] == 'failed'


@pytest.mark.anyio
async def test_restart_resumes_checkpoint_budget(tmp_path):
    entered = asyncio.Event()
    async def parser(source, output, **kwargs):
        (output/'checkpoint.txt').write_text('page1')
        entered.set()
        await asyncio.Event().wait()
    cfg = settings(tmp_path)
    app = create_app(cfg, parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        job = (await submit(client)).json()
        await entered.wait()
        await asyncio.sleep(.03)
    async def resumed(source, output, **kwargs):
        assert (output/'checkpoint.txt').read_text() == 'page1'
        return {'recovered':True}
    app2 = create_app(cfg, resumed)
    assert app2.state.worker.store.get(job['id'])['execution_used'] > 0
    async with app2.router.lifespan_context(app2), httpx.AsyncClient(transport=httpx.ASGITransport(app=app2),base_url='http://test') as client:
        await wait(client,job['id'])
        assert (await client.get('/v1/jobs/'+job['id']+'/result',headers=headers())).json() == {'recovered':True}


@pytest.mark.anyio
async def test_profile_change_does_not_resume_under_new_model(tmp_path):
    entered = asyncio.Event()
    async def parser(*args, **kwargs):
        entered.set()
        await asyncio.Event().wait()
    app = create_app(settings(tmp_path), parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        job = (await submit(client)).json()
        await entered.wait()
    app2 = create_app(settings(tmp_path,extractor={'model':'new-model'}), fixture_parser)
    async with app2.router.lifespan_context(app2), httpx.AsyncClient(transport=httpx.ASGITransport(app=app2),base_url='http://test') as client:
        failed = await wait(client,job['id'],'failed')
        assert failed['error']['code'] == 'PARSER_PROFILE_CHANGED'
        new = (await submit(client)).json()
        assert new['id'] != job['id']


@pytest.mark.anyio
async def test_global_page_semaphore_bounds_concurrent_documents(tmp_path):
    active, peak = 0, 0
    async def parser(source, output, *, page_semaphore, **kwargs):
        async def page():
            nonlocal active,peak
            async with page_semaphore:
                active += 1
                peak = max(peak,active)
                await asyncio.sleep(.03)
                active -= 1
        await asyncio.gather(*(page() for _ in range(4)))
        return {}
    app = create_app(settings(tmp_path,document_concurrency=2,page_concurrency=2), parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        jobs = [(await submit(client,PDF+str(i).encode())).json() for i in range(2)]
        await asyncio.gather(*(wait(client,j['id']) for j in jobs))
        assert peak == 2 and active == 0


@pytest.mark.anyio
async def test_provider_error_never_leaks_credentials(tmp_path):
    async def parser(*args, **kwargs):
        raise RuntimeError('SECRET_KEY https://private-provider/account request text')
    app = create_app(settings(tmp_path),parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        job = (await submit(client)).json()
        failed = await wait(client,job['id'],'failed')
        assert failed['error']['code'] == 'REMOTE_PARSE_FAILED'
        assert 'SECRET' not in json.dumps(failed) and 'private-provider' not in json.dumps(failed)


def test_single_scheduler_lock(tmp_path):
    app = create_app(settings(tmp_path),fixture_parser)
    with pytest.raises(RuntimeError,match='Only one'):
        create_app(settings(tmp_path),fixture_parser)
    asyncio.run(app.state.worker.close())


@pytest.mark.anyio
async def test_disk_admission_and_expired_results(tmp_path):
    app = create_app(settings(tmp_path,max_storage_bytes=4096),fixture_parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        assert (await submit(client)).status_code == 507
    store = Store(tmp_path/'expiry.db',retention=.01)
    job,_ = store.insert('scope','owner','digest','profile',1)
    store.finish(job['id'],'completed')
    time.sleep(.02)
    assert store.get(job['id'],'scope') is None
    assert store.expired() == [job['id']]
    store.close()


def test_scope_aggregate_limit_and_retry_replaces_old_leases(tmp_path):
    store = Store(tmp_path/'scope.db', global_limit=10, owner_limit=4, scope_limit=2)
    a,_ = store.insert('scope','alice','a','p',1)
    store.insert('scope','bob','b','p',1)
    with pytest.raises(QueueFull):
        store.insert('scope','charlie','c','p',1)
    old = store.lease(a['id'])
    store.finish(a['id'],'failed')
    _, new, _ = store.retry(a['id'], 'manual-key', 'alice')
    assert store.detach(a['id'],new) == 0
    assert store.detach(a['id'],old) == 0
    store.close()


@pytest.mark.anyio
async def test_running_output_quota_cancels_request(tmp_path):
    released = asyncio.Event()
    async def parser(source, output, **kwargs):
        (output/'oversized.json').write_bytes(b'x'*4096)
        try:
            await asyncio.Event().wait()
        finally:
            released.set()
    app = create_app(settings(tmp_path,max_job_output_bytes=1024),parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        job = (await submit(client)).json()
        failed = await wait(client,job['id'],'failed')
        assert failed['error']['code'] == 'PARSER_STORAGE_EXHAUSTED'
        assert released.is_set()


@pytest.mark.anyio
async def test_manual_retry_key_is_durable_idempotent_and_owner_scoped(tmp_path):
    calls = 0
    async def fail(*args, **kwargs):
        nonlocal calls
        calls += 1
        raise RuntimeError('provider failed')
    cfg = settings(tmp_path)
    app = create_app(cfg,fail)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        initial = (await submit(client)).json()
        await wait(client,initial['id'],'failed')
        url = '/v1/jobs/'+initial['id']+'/retry'
        retry_headers = headers(**{'X-Parse-Retry-Key':'server-generated-action-1'})
        assert (await client.post(url,headers=headers())).status_code == 400
        assert (await client.post(url,headers=headers('other',**{'X-Parse-Retry-Key':'server-generated-action-1'}))).status_code == 404
        first = (await client.post(url,headers=retry_headers)).json()
        duplicate = (await client.post(url,headers=retry_headers)).json()
        assert first['status'] == 'queued' and not first['reused']
        assert duplicate['reused'] and duplicate['lease_id'] == first['lease_id']
        await wait(client,initial['id'],'failed')
        failed_again = (await client.post(url,headers=retry_headers)).json()
        assert failed_again['status'] == 'failed' and failed_again['reused']
        assert failed_again['lease_id'] == first['lease_id']
        assert calls == 2
        wrong_owner = await client.post(url,headers=retry_headers|{'X-Parse-Owner':'other-user'})
        assert wrong_owner.status_code == 404
    # The action ledger survives process restart, including failures.
    app2 = create_app(cfg,fail)
    async with app2.router.lifespan_context(app2), httpx.AsyncClient(transport=httpx.ASGITransport(app=app2),base_url='http://test') as client:
        duplicate = (await client.post(url,headers=retry_headers)).json()
        assert duplicate['reused'] and duplicate['status'] == 'failed'
        assert duplicate['lease_id'] == first['lease_id']
        await asyncio.sleep(.15)
        assert calls == 2


@pytest.mark.anyio
async def test_retry_new_key_drops_stale_leases_and_repeat_key_does_not_reactivate(tmp_path):
    calls = 0
    entered, released = asyncio.Event(), asyncio.Event()
    async def parser(*args, **kwargs):
        nonlocal calls
        calls += 1
        if calls == 1:
            raise RuntimeError('failed first time')
        entered.set()
        try:
            await asyncio.Event().wait()
        finally:
            released.set()
    app = create_app(settings(tmp_path),parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        initial = (await submit(client)).json()
        stale_consumer = (await submit(client)).json()
        await wait(client,initial['id'],'failed')
        url = '/v1/jobs/'+initial['id']
        retry_headers = headers(**{'X-Parse-Retry-Key':'explicit-retry'})
        retried = (await client.post(url+'/retry',headers=retry_headers)).json()
        await entered.wait()
        assert (await client.post(url+'/retry',headers=headers(**{'X-Parse-Retry-Key':'different-action'}))).status_code == 409
        # Neither old first-run lease may keep the new run alive.
        cancelled = await client.delete(url,headers=headers(**{'X-Parse-Lease':retried['lease_id']}))
        assert cancelled.json()['status'] == 'cancelled' and released.is_set()
        same = (await client.post(url+'/retry',headers=retry_headers)).json()
        assert same['status'] == 'cancelled' and same['reused']
        assert same['lease_id'] == retried['lease_id']
        await asyncio.sleep(.15)
        assert calls == 2
        assert app.state.worker.store.detach(initial['id'],stale_consumer['lease_id']) == 0


def test_retry_transaction_capacity_failure_does_not_consume_action_key(tmp_path):
    store = Store(tmp_path/'retry.db',global_limit=1)
    failed,_ = store.insert('scope','alice','a','p',1)
    store.finish(failed['id'],'failed')
    blocking,_ = store.insert('scope','bob','b','p',1)
    with pytest.raises(QueueFull):
        store.retry(failed['id'],'manual-action','alice')
    assert store.get(failed['id'])['status'] == 'failed'
    store.finish(blocking['id'],'completed')
    job,lease,reused = store.retry(failed['id'],'manual-action','alice')
    assert job['status'] == 'queued' and not reused and lease
    store.close()


@pytest.mark.anyio
async def test_progress_merges_partial_events_and_never_regresses(tmp_path):
    async def parser(source, output, *, progress, **kwargs):
        await progress({'stage':'extracting','completed_pages':3,'total_pages':8})
        await progress({'stage':'verifying','page':5})
        snapshot = json.loads(app.state.worker.store.db.execute('SELECT progress FROM jobs').fetchone()[0])
        assert snapshot['completed_pages'] == 3 and snapshot['total_pages'] == 8
        await progress({'stage':'page_complete','completed_pages':2,'total_pages':8})
        snapshot = json.loads(app.state.worker.store.db.execute('SELECT progress FROM jobs').fetchone()[0])
        assert snapshot['completed_pages'] == 3
        await progress({'stage':'complete','complete_pages':8})
        return {}
    app = create_app(settings(tmp_path),parser)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url='http://test') as client:
        job = (await submit(client)).json()
        complete = await wait(client,job['id'])
        assert complete['progress']['completed_pages'] == 8
        assert complete['progress']['total_pages'] == 8


def test_ocr_runtime_profile_changes_with_engine_or_language_data(tmp_path):
    from types import SimpleNamespace
    from app import ocr_runtime_profile
    for lang in ('chi_sim','eng'):
        (tmp_path/(lang+'.traineddata')).write_bytes(lang.encode())
    version = 'tesseract 5.3.0'
    def runner(command,**kwargs):
        assert kwargs['check'] and kwargs['timeout'] == 10
        return SimpleNamespace(stdout=version if command[-1] == '--version' else f'List of available languages in "{tmp_path}/" (2):\nchi_sim\neng\n')
    config = {'fallback_ocr':True,'ocr_languages':'chi_sim+eng'}
    original = ocr_runtime_profile(config,runner)
    assert set(original['traineddata_sha256']) == {'chi_sim','eng'}
    (tmp_path/'chi_sim.traineddata').write_bytes(b'new language model')
    changed = ocr_runtime_profile(config,runner)
    assert changed != original and changed['version'] == original['version']
    version = 'tesseract 5.4.0'
    assert ocr_runtime_profile(config,runner) != changed
    assert ocr_runtime_profile({'fallback_ocr':False},lambda *a,**k:pytest.fail('must not invoke OCR when disabled')) is None


@pytest.mark.anyio
async def test_original_page_image_scope_identity_and_render_cache(tmp_path, monkeypatch):
    import fitz
    import hashlib
    from remote_pdf import extractor
    doc = fitz.open()
    doc.new_page().insert_text((20, 30), 'Source page image fixture')
    content = doc.tobytes()
    doc.close()
    async def parsed(source, output, **kwargs):
        return {'schema_version': 'remote_pdf_v1', 'source': {'sha256': hashlib.sha256(content).hexdigest(), 'page_count': 1},
                'pages': [{'page': 1, 'text': 'Source page image fixture'}], 'bundle': {}}
    calls = []
    render = extractor._render
    def capture(*args):
        calls.append(args[1])
        return render(*args)
    monkeypatch.setattr(extractor, '_render', capture)
    app = create_app(settings(tmp_path, max_upload_bytes=100000, max_job_output_bytes=1000000,
        max_storage_bytes=10000000), parsed)
    async with app.router.lifespan_context(app), httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://test') as client:
        job = (await client.post('/v1/jobs', files={'file':('source.pdf',content)}, headers=headers())).json()
        await wait(client, job['id'])
        route = '/v1/jobs/' + job['id'] + '/pages/1/image'
        assert (await client.get(route)).status_code == 401
        assert (await client.get(route, headers=headers('other-tenant'))).status_code == 404
        response = await client.get(route, headers=headers())
        assert response.status_code == 200, response.text
        assert response.headers['x-source-sha256'] == hashlib.sha256(content).hexdigest()
        assert response.headers['x-pdf-page'] == '1'
        assert response.content.startswith(b'\x89PNG\r\n\x1a\n')
        assert (await client.get(route, headers=headers())).content == response.content
        assert calls == [1]
        assert (await client.get(route.replace('/1/image','/2/image'), headers=headers())).status_code == 404
        (tmp_path/'jobs'/job['id']/'input.pdf').write_bytes(b'%PDF-changed')
        assert (await client.get(route, headers=headers())).status_code == 409

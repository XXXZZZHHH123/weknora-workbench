# Remote PDF parsing worker

Explicit engine `remote_vlm`, using a configured vision chat model. This is not a MinerU-compatible model endpoint. Native document ingestion and structured-table drafts share this service to reuse the same authorized source bytes and parser profile.

## Deployment boundary

Use the development network and a separate persistent directory. This compose file never deploys or restarts WeKnora or production. One Uvicorn process owns each data directory (an OS lock prevents duplicate schedulers); horizontal replicas must not mount this same SQLite database. A future multi-node deployment needs a shared transactional scheduler.

Create the private data directory and its `tmp` child, writable by UID 10001. Mount private settings using `REMOTE_PDF_SETTINGS_FILE` and never commit credentials. Settings example (placeholder values):

```json
{
  "token": "replace-with-a-random-private-service-token",
  "document_concurrency": 2,
  "page_concurrency": 2,
  "global_outstanding": 16,
  "scope_outstanding": 8,
  "owner_outstanding": 4,
  "max_upload_bytes": 104857600,
  "max_uploads": 4,
  "queue_timeout_seconds": 1800,
  "execution_timeout_seconds": 1800,
  "retention_seconds": 86400,
  "max_storage_bytes": 5368709120,
  "max_job_output_bytes": 268435456,
  "disk_reserve_bytes": 536870912,
  "extractor": {
    "endpoint": "https://your-configured-provider.example/v1/chat/completions",
    "api_key": "private-model-credential",
    "model": "your-configured-vision-model",
    "dpi": 180,
    "verification_dpi": 220,
    "max_tokens": 16384,
    "request_timeout_seconds": 180,
    "max_retries": 2,
    "repair_attempts": 1,
    "verify_tables": true
  }
}
```

Set `REMOTE_PDF_IMAGE`, `REMOTE_PDF_SETTINGS_FILE`, `REMOTE_PDF_DATA_DIR`; build/start with this compose file only after reviewing the intended environment. The HTTP port defaults to localhost 18735. Restrict service token access to trusted WeKnora backend processes. No API credential is accepted from browser-provided request data.

## Protocol

All job APIs require `Authorization: Bearer <service token>` and **`X-Parse-Scope` derived by the trusted upstream from the authenticated tenant/permission context**. Optional `X-Parse-Owner` is a verified user ID for admission/fair scheduling; it must not be copied from an untrusted arbitrary field. The upstream must enforce document read/parse permission before submitting or reading a result; this internal service trusts that authorization boundary. Do not expose it directly as a browser API.

- `GET /health`: unauthenticated liveness and schema, no job data.
- `POST /v1/jobs`: multipart `file`, returns 202 `{id,status,progress,error,reused,lease_id}`. A result/error is reused only for identical source SHA256, trusted scope, effective parser configuration and extractor code. Different owners within an authorized scope may share work. The private cache profile includes the effective extractor defaults, prompt/version, renderer version and a credential digest (never a plaintext key); rotating credentials invalidates reuse.
- `GET /v1/jobs/{id}`: scoped state, no file paths/provider secrets.
- `GET /v1/jobs/{id}/result`: completed `remote_pdf_v1` artifact; otherwise 409. The artifact contains pages, source identity, a `pdf_tables_v1` bundle and quality findings. A successful parse is not automatic approval of extracted numbers.
- `DELETE /v1/jobs/{id}` with **`X-Parse-Lease: <lease_id>`**: detach this caller. Only the last active consumer cancels shared work. Invalid job/scope/lease returns 404. Cancellation propagates through async HTTP requests; it does not wait for CPU inference in another server. Clients must detach on cancellation/deadline. Never include leases in URL logs.
- `POST /v1/jobs/{id}/retry`: **explicit human retry only**, requires `X-Parse-Retry-Key` (a unique server-generated token for this manual action, 1–128 printable ASCII characters) and returns `{...,lease_id,reused}`. The durable ledger binds the token digest to the job and verified owner: repeating the same key returns the same lease and current state even after another failure, without a second execution or budget reset. A new key may retry only a failed/cancelled job; reusing a key under another owner returns 404. Retries retain checkpoints but reset the execution budget. Native automatic retry loops must submit normally and reuse the failed state, not call this endpoint. Changed parser profiles require source resubmission.

Terminal statuses: `completed`, `failed`, `cancelled`; active: `queued`, `running`. Errors have sanitized `{code,message}`. Admission uses 429 + Retry-After; invalid/oversize source uses 422/413; exhausted storage uses 507. Deterministic job failures remain failed until explicit retry or retention expiry. Caller queue deadlines should exceed the service queue budget; execution deadlines are tracked independently.

## Capacity and recovery

Defaults run 2 documents and at most 2 model calls across all pages/jobs. Owner scheduling prefers the owner least recently served, with per-owner and per-scope outstanding limits before the global cap. It is fair admission/scheduling, not preemption: a newly arriving owner waits for an active document to release a worker.

Uploads are capped in the ASGI receive path before multipart spooling, with at most 4 simultaneous uploads, then streamed from spool to a bounded temporary file and hashed. Admission reserves output budget and worst-case concurrent-upload space; output growth and filesystem reserve are monitored while parsing. These application limits do not replace a filesystem quota: a single render/write between checks can briefly exceed the soft output threshold. Use a dedicated volume quota for a strict host-disk boundary. Terminal result/source/checkpoint files are retained for 24 hours then removed by the scheduler.

SQLite WAL/FULL records state. Restart requeues active jobs, retains consumed execution time and page checkpoints, and rejects an old job if model/config/code changed. Completed result files are atomically renamed before marking the job completed. Graceful shutdown cancels requests and requeues work; forced shutdown may repeat at most the in-flight page call because no provider-side idempotency is assumed. Retention does not delete active jobs. Cancelled/failed jobs do not restart automatically, even if a client repeats its upload.

Verify with `python -m pytest services/remote-pdf/tests/test_queue.py` from the repository's dedicated tools container. Tests use fake providers and synthetic bytes; they do not prove model accuracy, production capacity, or live multi-user acceptance.

## WeKnora integration

The native engine is named `remote_vlm` (Remote vision PDF). Set server-owned
`WEKNORA_REMOTE_PDF_URL` and `WEKNORA_REMOTE_PDF_TOKEN` on the Go app and allow the
service hostname in its existing SSRF allowlist. Select this engine for PDF in
the intended knowledge base or document processing settings. Other engines retain
their existing behavior. The source upload and result reads still enforce the
normal knowledge permissions.

For the structured service set `PDF_TABLE_PARSER_ENGINE=remote_vlm`,
`PDF_TABLE_PARSER_URL` and `PDF_TABLE_PARSER_API_KEY` to the same service.
Both clients derive tenant scope from verified authorization, so the same file
and parser profile share an extraction. Native document tasks persist the trusted
initiating user for fair scheduling. Manual reparse has an idempotency key distinct
from automatic task retries. The structured review page also offers explicit retry
for a failed task, after rechecking source permissions and bytes.

The review workflow supports vertical continuations and explicit horizontal row
alignment. It conserves every selected source row and column; a plan must not mix
horizontal and vertical assembly. Repeated source keys can be checked exactly;
missing entity labels require a separate human alignment explanation. Known
uncertain cells need explicit source-backed corrections and issue-by-issue review.
The final confirmation checkbox alone cannot bypass those requirements.

Set `extractor.fallback_ocr=true` to enable explicit local OCR text fallback when
the vision provider refuses a page or is unavailable after bounded retries.
The refusal remains in page metadata. Such pages retain original PNG, OCR text,
word/line geometry and confidence; they are always marked for review and do not
claim that tables or formulas are absent. No alternate model is silently selected.
`request_options` may explicitly select a provider-supported reasoning switch;
it cannot override destinations, credentials, messages or the selected model.

Full page processing, agreeing model transcriptions, and verified table accuracy
are separate outcomes. A real acceptance run found a unanimous wrong number, and
providers can duplicate tables into the purported non-table text field. Therefore
native indexing uses table titles and column headers for discovery on table pages,
plus separately extracted formulas with a review warning. It omits both raw page
text and cell-valued Markdown on those pages, even when model transcriptions agree.
The complete original transcription stays in the review artifact. Exact table
answers must use source-reviewed, published structured data. Ordinary text pages
retain text indexing and source-page citations. This deliberately trades immediate
numeric recall for a verifiable table approval boundary.

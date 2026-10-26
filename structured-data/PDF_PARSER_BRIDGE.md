# Structured PDF parser bridge

`structured_data.pdf_parser_bridge.parse_pdf(path, source=None, config=None)` reads a local PDF and returns a `pdf_tables_v1` bundle for the PDF table store. Production callers should omit `config`; the bridge then reads only server environment variables. `source` may contain the stored `file_name`, `knowledge_id`, and an optional SHA-256 that must match the file bytes. It cannot select a parser endpoint or pass credentials.

Configure one trusted self-hosted endpoint with:

| Variable | Meaning | Default |
| --- | --- | --- |
| `PDF_TABLE_PARSER_ENGINE` | `mineru_v1`, `mineru_legacy`, or `paddleocr_vl` | required |
| `PDF_TABLE_PARSER_URL` | Base URL of the trusted parser service | required |
| `PDF_TABLE_PARSER_API_KEY` | Optional MinerU V1 service key | empty |
| `PDF_TABLE_PARSER_TIER` | MinerU V1 tier: `flash`, `basic`, `standard`, or `advanced` | service default |
| `PDF_TABLE_PARSER_OCR_MODE` | MinerU OCR mode: `auto`, `txt`, or `ocr` | `auto` |
| `PDF_TABLE_PARSER_TIMEOUT_SECONDS` | Request / MinerU job deadline, capped at 7200 seconds | 600 |
| `PDF_TABLE_PARSER_MAX_FILE_BYTES` | Input limit, capped at 100 MiB | 50 MiB |
| `PDF_TABLE_PARSER_MAX_RESPONSE_BYTES` | JSON and retained raw artifact limit, capped at 64 MiB | 32 MiB |
| `PDF_TABLE_PARSER_MAX_ARCHIVE_BYTES` | MinerU ZIP download and expanded-size limit, capped at 128 MiB | 64 MiB |
| `PDF_TABLE_PARSER_MAX_POLLS` | Optional explicit MinerU job poll limit, capped at 144001 | `ceil(timeout / poll_interval) + 1` |
| `PDF_TABLE_PARSER_POLL_INTERVAL_SECONDS` | Poll interval, capped at 30 seconds | 10 |

The bridge disables redirects and environment HTTP proxies for parser requests. MinerU upload URLs must resolve to the configured endpoint's exact origin; it never fetches parser-returned artifact URLs. MinerU V1 artifacts are read in memory with archive entry count and expanded-size limits; the bridge does not extract archive paths onto disk. PaddleOCR-VL uses `/layout-parsing` with table merging disabled so page-local layout blocks remain attributable.

MinerU V1 uses one absolute deadline covering upload, queueing, parsing, and artifact download. The automatic poll budget covers that configured deadline; an explicit poll limit can stop waiting earlier. For example, a 3600-second timeout and 20-second interval yield 181 polls when no explicit limit is configured. If waiting fails, times out, or is canceled after the bridge receives a job ID, it attempts `DELETE /v1/parse/jobs/{job_id}` with a separate five-second cleanup budget. Cleanup errors never replace the original failure, and observed terminal jobs are not canceled again. Cancellation is best effort; a lost job-creation response cannot provide an ID to cancel. No completed-result cache is implied by upload deduplication.

An acknowledged `canceled` job does not prove that inference stopped or CPU/memory resources were released. In MinerU 4.0.12, the local llama.cpp backend uses the synchronous document analysis path in a worker thread. Its `run_sync` cancellation handler waits for that thread to finish; it cannot interrupt the synchronous document/window loop. A canceled job can therefore continue processing later windows before cleanup finishes. Check actual worker activity before starting another expensive run; restarting the dedicated parser is an operational action that also discards its in-memory job/file metadata.

Each normalized table fragment carries its parser-supplied page, an optional normalized page box, optional caption and footnotes, and a rectangular cell grid. Merged cells keep `rowspan` / `colspan` on the anchor cell and covered positions are explicit empty cells, never duplicate values. Unknown or out-of-range geometry is omitted rather than clamped. If the parser returns a table without page provenance alongside attributable tables, it is preserved in `ambiguities`, the bundle is marked `requires_review`, and completeness is `unknown`; if no attributable table remains, normalization fails with a review-required error. Markdown-only table text is never promoted to structured cells.

The top-level `raw` field retains bounded parser JSON, MinerU structured layout, Markdown, and source HTML through `raw_html` on fragments. Formula objects retain LaTeX and parser-supplied page/bbox when present. Parser version stays `null` if the service does not report it. `parser_availability()` reports only whether configuration exists and the engine name; it does not disclose endpoint details or secrets.

Run the fixture contract tests from the test container:

```sh
wkdev tool python3 -m pytest -q structured-data/tests/test_pdf_parser_bridge.py
```

Fixture tests exercise response normalization and safety boundaries; they are not OCR/model acceptance. A separate CPU smoke (when model files are available) must use synthetic documents and must not be described as accuracy acceptance.

# WeKnora v0.9.0 integration

PR #42 updates the vendored WeKnora source from the existing `3e22822c` baseline
to upstream release `f17ee8a8` (v0.9.0). The generated draft contained 84 conflicted
files. Resolution favors the workbench's research and evidence requirements,
while adopting compatible official fixes and capabilities.

## Decisions

| Area | Selected behavior and reason |
| --- | --- |
| Numerical and document answers | Retain source-bound calculations, physical-page reads, original-figure verification, independent review, and buffered verified terminal delivery. Official streaming and collaboration must not publish an unreviewed draft. |
| PDF and PowerPoint extraction | Retain numeric source text when flattened text cannot establish geometry, source-cell/page locators, original chart images, bounded media conversion, and shared heavy-parser slots. Adopt official conversion diagnostics, spreadsheet memory improvements, and compatible PDF recovery. |
| Uploads and research scope | Retain unified PDF/Excel processing, review/publication gates, source-only restrictions, and research authorization. Adopt official bounded polling concurrency and preserve ingestion stage/attention metadata. XLSM and ODS remain outside platform imports until the same review/publication boundary supports them. |
| Model and source protocols | Adopt official scheme-shaped source handles and compatible provider/output-budget improvements. Retain tool-specific JSON-path policies, quoted SQL substitution, opaque structured payloads, model selection isolation, and rate-limit backoff/signals. |
| Conversation recovery | Adopt official bounded reconnect, background notices, suspension, and collaboration handling. Preserve actual human requests and evidence during retry, plus local review and artifact-delivery checks. |
| Interface | Retain business branding, projects, research workbenches, accessible source actions, modal focus, and source preview safeguards. Integrate the official assistant and automation entry with the existing navigation. Folder labels use the official recursive document total so a folder with documents in subfolders does not appear empty; the tooltip retains its direct count. Embedded assistant model changes and scoped research model changes must not overwrite the main-chat selection. |
| Personal memory | Retain fail-closed caller workspace binding. An execution tenant is not an authenticated caller and must not become a fallback personal-memory scope. |
| Runtime help | Bundle only the official numbered Markdown help pages under `internal/productdocs/bundled`. Keep website code, assets, and build dependencies excluded. The documentation tool distinguishes upstream help from this customized instance's actual settings. |
| Container build | Retain the workbench's early native-parser dependency layers and persistent downloaded Go modules. Combine the research signing-key runtime copy with the official socket-group handling before dropping privileges. |

## Migration lineage

Existing migration files and numbers remain fixed. Incoming official migration
files are appended after the deployed workbench lineage:

- PostgreSQL: official 119–134 becomes workbench 132–147; existing versions through 131 stay intact.
- SQLite: official 37–51 becomes workbench 50–64; existing versions through 49 stay intact.

Tests cover real SQLite upgrades and disposable PostgreSQL upgrades, retaining
feedback, conversations, research workbenches, and the source-sharing permission
boundary. Renumbering must also preserve earlier compatibility repairs and
rollback tests.

## Additional evidence-boundary repair

Numeric evidence must exclude the engine's leading runtime-context and tool-routing
envelopes. Dates, resource identifiers, and routing counts are not human-provided
operands. The real human request and executed source results retain their existing
numeric semantics. A regression checks this independently of question vocabulary.

## Verification scope

Local checks on the resolved source:

- Frontend: 2,623 tests passed, one skipped; all three TypeScript configurations and the production Vite build passed.
- Go: the application build and the complete agent suite passed; source-context/tool, caller-memory, container/router/model, and data-source/session compatibility regressions were checked. Full test-program compilation is checked separately from execution.
- Database: the complete migration suite passed against SQLite and a disposable PostgreSQL server, including deployed-lineage upgrades, sentinel preservation, and repeated execution. All 364 migration files already on main were compared byte for byte and remain unchanged.
- DocReader: 542 tests and 261 subtests passed, 14 skipped. One unchanged WebKit proxy integration test was excluded because the tools container lacks the required system libraries.
- Deployment and upstream synchronization: 59 tests passed; conflict metadata validation and whitespace checks passed.

Local browser fixture startup could not complete with the available Chromium/system libraries. Three build-memory guard tests require `/usr/bin/python3`, which is absent from the local tools environment; the other 42 passed. These checks require GitHub Ubuntu CI confirmation. Browser-launcher lifecycle tests passed (8 tests).

GitHub checks must apply to the latest PR commit. These local checks do not establish production, real-provider, or business-data end-to-end acceptance. This integration does not merge main or deploy production, and it does not establish that the historical findings in `QUESTION_HARDCODING_AUDIT.md` have all been resolved.

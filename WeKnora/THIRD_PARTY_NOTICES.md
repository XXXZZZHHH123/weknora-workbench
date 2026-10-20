# Third-party notices and corresponding source

This file supplements the third-party notices in `LICENSE`. The MIT license
for WeKnora's own code does not replace the licenses of third-party components.
Keep this file, `LICENSE`, and the `licenses/` directory with redistributed
backend and desktop packages. `scripts/copy-licenses.sh` adds checksum-verified
source archives at packaging time; they are not stored in this Git repository.
Container distributions include the complete bundle in `/app`;
macOS applications include them in `Contents/Resources`.
Windows installers place them next to the installed executable.

## Go MySQL Driver

- Component: `github.com/go-sql-driver/mysql`, version `v1.10.0`.
- Used in backend and desktop binaries, including the Doris MySQL protocol driver.
- License: Mozilla Public License 2.0, reproduced in
  [`licenses/go-sql-driver-mysql-MPL-2.0.txt`](licenses/go-sql-driver-mysql-MPL-2.0.txt).
- Copyright: The Go-MySQL-Driver Authors; the original per-file notices and
  `AUTHORS` are preserved in the accompanying source archive.
- Modifications by WeKnora: none. Dialer configuration is in separate WeKnora files.
- Corresponding source, available under MPL-2.0, is included in binary releases
  as `licenses/sources/mysql-v1.10.0.zip`. Source repository users can obtain it from the
  [Go module proxy](https://proxy.golang.org/github.com/go-sql-driver/mysql/@v/v1.10.0.zip)
  and [upstream repository](https://github.com/go-sql-driver/mysql/tree/v1.10.0).

## go-m1cpu

- Component: `github.com/shoenig/go-m1cpu`, version `v0.1.6`.
- Used by the macOS backend/desktop dependency chain through gopsutil. It is
  absent from Linux backend build dependencies. Its source is included in all
  notice bundles for consistent packaging; this does not imply Linux linkage.
- License: Mozilla Public License 2.0, reproduced in
  [`licenses/go-m1cpu-MPL-2.0.txt`](licenses/go-m1cpu-MPL-2.0.txt).
- Attribution: the go-m1cpu project and its contributors. The complete original
  source and notices are preserved in the accompanying archive.
- Modifications by WeKnora: none.
- Corresponding source, available under MPL-2.0, is included in binary releases
  as `licenses/sources/go-m1cpu-v0.1.6.zip`. Source repository users can obtain it from the
  [Go module proxy](https://proxy.golang.org/github.com/shoenig/go-m1cpu/@v/v0.1.6.zip)
  and [upstream repository](https://github.com/shoenig/go-m1cpu/tree/v0.1.6).

## OpenCC dictionary data

- Files: `internal/textconv/data/TSPhrases.txt` and `TSCharacters.txt`, copied
  unchanged from `github.com/longbridgeapp/opencc` version `v0.3.13`.
- Attribution: the OpenCC and longbridge/opencc contributors.
- License: Apache-2.0, reproduced in
  [`licenses/OpenCC-Apache-2.0.txt`](licenses/OpenCC-Apache-2.0.txt).
- Source: [versioned dictionary directory](https://github.com/longbridgeapp/opencc/tree/v0.3.13/dictionary)
  and the two text files distributed with WeKnora's source.
- Only dictionary data is retained. WeKnora uses its own standard-library lookup
  implementation; the upstream Go converter, `liuzl/da`, and GPL-licensed
  `cedar-go` code are not included.

## Build-only component

The Windows installer template in `cmd/desktop/build/windows/installer/project.nsi`
is based on Wails v2.12.0's MIT-licensed default template, with local changes to
install this notice/source bundle. Its license and copyright are reproduced in
[`licenses/Wails-MIT.txt`](licenses/Wails-MIT.txt).

`cbindgen` 0.29.4 (MPL-2.0) generates the AnyDoc C ABI header during the Rust
build. Its code/tool executable is not copied into the runtime image or release
packages by the current build recipes. Generating the header does not copy the
tool's implementation into the product. If distributing build environments or
the tool itself, retain its license and provide its corresponding source too.

## Maintaining this bundle

When upgrading either MPL module, update the version, license, and SHA-256 pin in
`licenses/sources/modules.tsv` together. Archives are unmodified Go module proxy
ZIPs, including their original copyright notices. Verify them with
`go mod download -json` and the module's `go.sum` entry before updating.
`scripts/check-license-bundle.sh` checks the pins and notices without downloading
dependencies. `scripts/copy-licenses.sh DESTINATION` obtains the pinned archives
through Go's configured `GOPROXY` and module cache, verifies their SHA-256 hashes,
and includes them in the destination bundle. Offline packaging requires those
module archives to be present in the Go module cache beforehand. To verify a
packaged source directory, run `scripts/check-license-bundle.sh PATH/TO/licenses/sources`.

## Open Claude UI

- Source: [Caho1/open-claude-ui](https://github.com/Caho1/open-claude-ui),
  reference commit `02d021fceaf2a907f9e63a8125621bdda1b85d60`.
- Attribution: Copyright (c) 2026 Caho1.
- License: MIT, reproduced in
  [`licenses/Open-Claude-UI-MIT.txt`](licenses/Open-Claude-UI-MIT.txt).
- CDS typography, color, surface, motion and interaction design references are adapted to
  WeKnora's existing Vue/TDesign components and iframe workspaces. No React
  runtime, model backend, proprietary font, audio, or Anthropic binary assets
  are included from that project.

## Noto Serif SC (brand wordmark subset)

- Copyright: Copyright 2017-2024 Adobe; Noto Serif SC is distributed under the SIL Open Font License 1.1.
- Source: [Google Fonts Noto Serif SC](https://github.com/google/fonts/tree/main/ofl/notoserifsc); the self-hosted WOFF2 is a four-glyph, weight-600 subset obtained from the [Google Fonts CSS API](https://fonts.googleapis.com/css2?family=Noto+Serif+SC:wght@600&text=%E4%B8%AD%E5%93%B2%E7%89%A9%E4%BA%A7&display=swap).
- Full license: [`frontend/src/assets/fonts/OFL.txt`](frontend/src/assets/fonts/OFL.txt).
- Asset source and SHA-256: [`frontend/src/assets/fonts/README.md`](frontend/src/assets/fonts/README.md).

## Shared workspace typography

- Inter is self-hosted in [`frontend/public/fonts`](frontend/public/fonts), with its SIL Open Font License in `Inter-LICENSE.txt`.
- [Noto Sans SC](https://github.com/google/fonts/tree/main/ofl/notosanssc), copyright 2014–2021 Adobe, supplies variable-weight interface text; [Noto Serif SC](https://github.com/google/fonts/tree/main/ofl/notoserifsc), copyright 2017–2024 Adobe, supplies weight-600 titles and weight-400 reading text.
- These Google Fonts assets use the SIL Open Font License 1.1. Complete upstream Unicode ranges are retained as locally hosted WOFF2 chunks, loaded only when required by visible text. There are no runtime Google Fonts requests.
- Licenses, source URLs and asset hashes are included beside the files in `frontend/public/fonts/noto-sans-sc`, `noto-serif-sc` and `noto-serif-reading-sc`. All HTML entry points register them through `frontend/public/fonts/workspace-typography.css`.

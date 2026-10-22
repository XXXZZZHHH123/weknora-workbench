Workbench Chart CJK Regular is a static, merged derivative of the 101 Noto Sans SC
WOFF2 subsets already vendored at `frontend/public/fonts/noto-sans-sc/`. It is
licensed under SIL OFL 1.1; see OFL.txt. Original copyright: Adobe 2014–2021. The
reserved name “Source” is not used for this derivative's family/PostScript name.

`build_font.py` instantiates weight 400, removes unused Unicode layout tables,
merges all source subsets, renames the face, and fixes font timestamps. It needs
fonttools[woff]==4.61.1 only during asset regeneration; it makes no network
requests. Run it from any working directory with the tooling installed.
The Go application embeds the resulting TTF and has no system-font dependency.

The face contains 13,635 codepoints, including the repository's Chinese coverage,
Latin labels, digits and punctuation. Rendering checks every displayed glyph and
rejects unsupported characters rather than producing tofu or replacing them with
unrelated glyphs. This face does not claim coverage of every Unicode CJK extension.

Generated file: WorkbenchChartCJK-Regular.ttf, 4,480,252 bytes.
SHA256: 7ed36fedc015694e961e094ba58dbb2897178c0be10882e47d4363c461f5e276

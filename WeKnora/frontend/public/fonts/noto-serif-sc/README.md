# Shared brand and title font

Noto Serif SC, weight 600, Google Fonts version v36: the same family, weight and version as the existing wordmark font.

The complete upstream Unicode ranges are retained, so arbitrary project, library and workbench names can use the font. Browsers request only the needed local WOFF2 chunks. All entry points load `workspace-typography.css`, which registers this directory's `font.css` together with the regular 400 face in `../noto-serif-reading-sc/` under the same `WeKnora Noto Serif SC` family. No runtime Google Fonts request is needed.

- Upstream CSS: https://fonts.googleapis.com/css2?family=Noto+Serif+SC:wght@600&display=swap
- Upstream metadata: https://github.com/google/fonts/blob/main/ofl/notoserifsc/METADATA.pb
- License: SIL Open Font License 1.1, included in `OFL.txt`.
- The existing four-character wordmark subset remains as a small asset for the brand itself.

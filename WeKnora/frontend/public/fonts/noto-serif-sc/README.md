# Shared brand and title font

Noto Serif SC, weight 600, Google Fonts version v36: the same family, weight and version as the existing wordmark font.

The complete upstream Unicode ranges are retained, so arbitrary project, library and workbench names can use the font. Browsers request only the needed local WOFF2 chunks. Font registration is in `src/assets/theme/brand-font.css`; no runtime Google Fonts request is needed.

- Upstream CSS: https://fonts.googleapis.com/css2?family=Noto+Serif+SC:wght@600&display=swap
- Upstream metadata: https://github.com/google/fonts/blob/main/ofl/notoserifsc/METADATA.pb
- License: SIL Open Font License 1.1, included in `OFL.txt`.
- The existing four-character wordmark subset remains as a small asset for the brand itself.

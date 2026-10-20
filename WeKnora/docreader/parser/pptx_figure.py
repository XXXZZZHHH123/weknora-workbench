"""Read one explicitly selected original figure without re-indexing its deck."""
import hashlib
import io
import json
import re
import zipfile

from docreader.models.document import Document
from docreader.parser.pptx_chart_geometry import estimate_emf_chart_trends
from docreader.parser.pptx_media import (_read_member, _slide_parts, _slide_pictures,
                                       _trend_geometry_markdown, extract_emf_labels)

FIGURE_GEOMETRY_ENGINE = "source_figure_geometry_v1"


def read_original_figure(content: bytes, file_type: str, options: dict) -> Document:
    if file_type.lower().lstrip(".") != "pptx" or not 0 < len(content) <= 32 * 1024 * 1024:
        raise ValueError("Unsupported original figure container or size")
    part = options.get("source_part", "")
    if not re.fullmatch(r"ppt/media/[A-Za-z0-9_.-]+\.emf", part, flags=re.IGNORECASE):
        raise ValueError("Invalid original figure part")
    page = int(options.get("source_page", "0"))
    with zipfile.ZipFile(io.BytesIO(content)) as archive:
        pages = _slide_parts(archive)
        if not 1 <= page <= len(pages):
            raise ValueError("Original figure page is outside the deck")
        targets = {target for _, target, _ in _slide_pictures(archive, pages[page - 1])}
        if part not in targets:
            raise ValueError("Original figure does not belong to the selected physical page")
        raw = _read_member(archive, part, 16 * 1024 * 1024)
    geometry = estimate_emf_chart_trends(raw)
    if not geometry:
        raise ValueError("Original figure geometry is unsupported or ambiguous")
    metadata = {"source_page": str(page), "source_part": part,
        "source_file_sha256": hashlib.sha256(content).hexdigest(),
        "source_media_sha256": hashlib.sha256(raw).hexdigest()}
    requested = json.loads(options.get('series_json') or '[]')
    if requested is None:
        requested = []
    if not isinstance(requested, list) or len(requested) > 32 or any(not isinstance(x, str) for x in requested):
        raise ValueError('Invalid model-selected legend identifiers')
    if not requested:
        return Document(content='Explicit original legend selection is required before numeric geometry.', metadata={
            **metadata, 'evidence_version': 'source_figure_selection_options_v1',
            'available_series_json': json.dumps([row['legend'] for row in geometry['series']], ensure_ascii=False)})
    if requested:
        labels = {row['legend'] for row in geometry['series']}
        if not set(requested) <= labels:
            # Navigation evidence only: never substitute a curve or return
            # numeric geometry for an invalid model-selected identifier.
            return Document(content='Original legend identifiers; invalid selection must be corrected.', metadata={
                **metadata, 'evidence_version': 'source_figure_selection_options_v1',
                'available_series_json': json.dumps([row['legend'] for row in geometry['series']], ensure_ascii=False)})
        geometry['source_series_count'] = geometry['series_count']
        geometry['series_selection'] = list(dict.fromkeys(requested))
        geometry['omitted_series'] = [row['legend'] for row in geometry['series'] if row['legend'] not in requested]
        geometry['series'] = [row for row in geometry['series'] if row['legend'] in requested]
        geometry['series_count'] = len(geometry['series'])
    # Only the explicitly selected traces participate. This is deterministic
    # ordering of drawn values, not a choice of business comparison scope or a
    # same-date ranking. Keep overlap uncertainty and each trace's x position.
    traces = geometry['series']
    ordered = sorted(traces, key=lambda row: row['last']['approximate_value'], reverse=True)
    geometry['selected_endpoint_value_order'] = {
        'scope': 'Explicitly selected traces, each at its own last drawn point; not a same-date or same-period ranking.',
        'selected_count': len(traces),
        'source_series_count': geometry['source_series_count'],
        'same_x_position': len({row['last']['x_fraction'] for row in traces}) == 1,
        'rows': [{
            'legend': row['legend'], 'color': row['color'],
            'approximate_value': row['last']['approximate_value'],
            'estimate_low': row['last']['estimate_low'], 'estimate_high': row['last']['estimate_high'],
            'x_fraction': row['last']['x_fraction'],
            'descending_value_rank': 1 + sum(other['last']['approximate_value'] > row['last']['approximate_value'] for other in traces),
            'possible_rank_min': 1 + sum(other['last']['estimate_low'] > row['last']['estimate_high'] for other in traces if other is not row),
            'possible_rank_max': 1 + sum(other['last']['estimate_high'] >= row['last']['estimate_low'] for other in traces if other is not row),
        } for row in ordered],
    }
    return Document(content=_trend_geometry_markdown(geometry, page, part), metadata={
        **metadata, "evidence_version": FIGURE_GEOMETRY_ENGINE,
        "geometry_json": json.dumps(geometry, ensure_ascii=False),
        "source_labels_json": json.dumps(extract_emf_labels(raw), ensure_ascii=False),
    })

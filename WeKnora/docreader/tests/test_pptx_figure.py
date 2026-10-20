import hashlib
import io
import json
import unittest
import zipfile

from docreader.parser.pptx_figure import read_original_figure
from docreader.tests.test_pptx_media import fixture
from docreader.tests.test_pptx_chart_geometry import chart


def original_deck():
    out = io.BytesIO()
    with zipfile.ZipFile(io.BytesIO(fixture())) as source, zipfile.ZipFile(out, "w") as target:
        for name in source.namelist():
            target.writestr(name, chart(short_curve=True, horizontal_labels=True)
                            if name == "ppt/media/chart-a.emf" else source.read(name))
    return out.getvalue()


class TestOriginalFigure(unittest.TestCase):
    def test_explicit_figure_uses_display_order_relationships_and_original_hashes(self):
        source = original_deck()
        result = read_original_figure(source, "pptx", {"source_page": "1", "source_part": "ppt/media/chart-a.emf", "series_json": '["Alpha","Beta","Gamma"]'})
        self.assertEqual(result.metadata['source_file_sha256'], hashlib.sha256(source).hexdigest())
        self.assertEqual(result.metadata['source_page'], '1')
        self.assertEqual(result.images, {})
        geometry = json.loads(result.metadata['geometry_json'])
        self.assertEqual(geometry['series_count'], 3)
        self.assertNotIn('endpoint_rank', geometry['series'][-1])
        self.assertIn('Start', result.content)
        comparison = geometry['selected_endpoint_value_order']
        self.assertEqual([row['legend'] for row in comparison['rows']], ['Alpha', 'Gamma', 'Beta'])
        self.assertEqual([row['descending_value_rank'] for row in comparison['rows']], [1, 2, 3])
        self.assertFalse(comparison['same_x_position'])
        self.assertIn('not a same-date', comparison['scope'])

    def test_other_page_unreferenced_part_external_and_traversal_fail_closed(self):
        source = original_deck()
        for page, part in [('2', 'ppt/media/chart-a.emf'), ('1', 'ppt/media/unrelated.emf'),
                           ('0', 'ppt/media/chart-a.emf'), ('99', 'ppt/media/chart-a.emf'),
                           ('1', 'ppt/media/../private.emf'), ('1', 'https://example.invalid/a.emf')]:
            with self.subTest(page=page, part=part), self.assertRaises(ValueError):
                read_original_figure(source, 'pptx', {'source_page': page, 'source_part': part})

    def test_unsupported_or_ambiguous_geometry_never_becomes_numeric_evidence(self):
        with self.assertRaises(ValueError):
            read_original_figure(fixture(), 'pptx', {'source_page':'1', 'source_part':'ppt/media/chart-a.emf'})

    def test_model_selected_real_legend_preserves_source_scope_and_literal_axis_intervals(self):
        result = read_original_figure(original_deck(), 'pptx', {'source_page':'1', 'source_part':'ppt/media/chart-a.emf', 'series_json':'["Gamma"]'})
        geometry = json.loads(result.metadata['geometry_json'])
        self.assertEqual(geometry['source_series_count'], 3)
        self.assertEqual(geometry['series_count'], 1)
        self.assertEqual(geometry['omitted_series'], ['Alpha', 'Beta'])
        interval = geometry['series'][0]['last']['x_axis_interval']
        self.assertEqual((interval['left_label'], interval['right_label']), ('Start', 'Middle'))
        self.assertTrue(0 < interval['fraction_between_labels'] < 1)
        self.assertIn('图例 Gamma', result.content)
        comparison = geometry['selected_endpoint_value_order']
        self.assertEqual(comparison['selected_count'], 1)
        self.assertEqual(comparison['source_series_count'], 3)
        self.assertEqual([row['legend'] for row in comparison['rows']], ['Gamma'])
        self.assertEqual(comparison['rows'][0]['descending_value_rank'], 1)
        self.assertEqual(comparison['rows'][0]['possible_rank_min'], 1)
        self.assertEqual(comparison['rows'][0]['possible_rank_max'], 1)

    def test_unknown_or_malformed_model_legend_selection_is_not_substituted(self):
        result = read_original_figure(original_deck(), 'pptx', {'source_page':'1', 'source_part':'ppt/media/chart-a.emf', 'series_json':'["unknown"]'})
        self.assertEqual(result.metadata['evidence_version'], 'source_figure_selection_options_v1')
        self.assertEqual(json.loads(result.metadata['available_series_json']), ['Alpha', 'Beta', 'Gamma'])
        self.assertNotIn('geometry_json', result.metadata)
        self.assertNotIn('约值', result.content)
        missing = read_original_figure(original_deck(), 'pptx', {'source_page':'1', 'source_part':'ppt/media/chart-a.emf'})
        self.assertEqual(missing.metadata['evidence_version'], 'source_figure_selection_options_v1')
        self.assertNotIn('geometry_json', missing.metadata)
        for selection in ['["Gamma",42]', '{}']:
            with self.subTest(selection=selection), self.assertRaises(ValueError):
                read_original_figure(original_deck(), 'pptx', {'source_page':'1', 'source_part':'ppt/media/chart-a.emf', 'series_json':selection})


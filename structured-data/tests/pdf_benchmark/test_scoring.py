"""Tests of the benchmark's refusal to turn transport/loss into a pass."""
from copy import deepcopy
import importlib.util
from pathlib import Path
import unittest

spec=importlib.util.spec_from_file_location('pdf_benchmark_score',Path(__file__).with_name('score.py'))
score=importlib.util.module_from_spec(spec);spec.loader.exec_module(score)


class ScoringTests(unittest.TestCase):
    def fixture(self):
        gold=dict(id='authored',sha256='a'*64,tables=[dict(id='source',page=1,bbox=[.1,.1,.9,.5],
            rows=[['Name','Value'],['A','-1,234.50'],['',''],['B','12.5%']],column_count=2,spans=[],authored_blank_rows=[2])],
            formulas=[dict(id='F1',page=1,bbox=[.1,.6,.5,.8],latex=r'\frac{a+b}{c-d}')])
        bundle=dict(source={'sha256':'a'*64},fragments=[dict(id='actual',page=1,bbox=[.1,.1,.9,.5],
            rows=[[{'text':v} for v in row] for row in gold['tables'][0]['rows']])],
            formulas=[dict(page=1,bbox=[.1,.6,.5,.8],latex=r'\frac{a+b}{c-d}')])
        return gold,bundle

    def test_exact_authored_values_and_formula_are_separate_from_acceptance(self):
        gold,bundle=self.fixture();result=score.score_fixture(gold,bundle)
        self.assertTrue(result['all_table_cells_exact']);self.assertTrue(result['formula_structure_exact'])
        self.assertEqual(result['numeric_literal_accuracy'],1)
        self.assertFalse(result['acceptance_complete']);self.assertFalse(result['logical_review_executed'])

    def test_dropped_or_added_blank_row_never_scores_as_exact(self):
        for mode in ('drop','extra'):
            gold,bundle=self.fixture()
            if mode=='drop':bundle['fragments'][0]['rows'].pop(2)
            else:bundle['fragments'][0]['rows'].insert(3,[{'text':''},{'text':''}])
            result=score.score_fixture(gold,bundle)
            self.assertFalse(result['table_structure_exact']);self.assertFalse(result['all_table_cells_exact'])
            self.assertFalse(result['tables'][0]['blank_rows_exact'])

    def test_sign_comma_percentage_and_missing_value_are_not_repaired_for_scores(self):
        for text in ('1234.50','-1234.50','-1,234.5','0',''):
            gold,bundle=self.fixture();bundle['fragments'][0]['rows'][1][1]['text']=text
            result=score.score_fixture(gold,bundle)
            self.assertLess(result['numeric_literal_accuracy'],1)
            self.assertFalse(result['all_table_cells_exact'])

    def test_formula_flattening_cannot_hide_behind_correct_table(self):
        gold,bundle=self.fixture();bundle['formulas'][0]['latex']='a+b/c-d'
        result=score.score_fixture(gold,bundle)
        self.assertTrue(result['all_table_cells_exact']);self.assertFalse(result['formula_structure_exact'])

    def test_wrong_page_or_source_hash_fails(self):
        gold,bundle=self.fixture();bundle['fragments'][0]['page']=2
        self.assertFalse(score.score_fixture(gold,bundle)['table_structure_exact'])
        bundle['source']['sha256']='b'*64
        with self.assertRaises(ValueError):score.score_fixture(gold,bundle)

    def test_span_loss_is_a_structural_failure_even_if_text_matches(self):
        gold,bundle=self.fixture();gold['tables'][0]['spans']=[dict(row=1,column=0,rowspan=2,colspan=1)]
        result=score.score_fixture(gold,bundle)
        self.assertFalse(result['table_structure_exact']);self.assertFalse(result['all_table_cells_exact'])

    def test_annotation_canonicalization_never_flattens_a_power_to_number(self):
        self.assertEqual(score.cell_text('10<sup>2</sup>'),score.cell_text('10²'))
        self.assertNotEqual(score.cell_text('10²'),score.cell_text('102'))
        self.assertNotEqual(score.cell_text('12.5%'),score.cell_text('12.5'))


if __name__=='__main__':unittest.main()

"""Strict row/column scoring against authored gold, independent of inference."""
from __future__ import annotations

import html
import re


_SUPER = str.maketrans('⁰¹²³⁴⁵⁶⁷⁸⁹⁻⁺', '0123456789-+')
_NUMERIC_LITERAL = re.compile(r'[+\-−]?(?:\d[\d,]*)(?:\.\d+)?%?\Z')


def cell_text(value):
    if isinstance(value, dict):
        value = value.get('text', '')
    text = html.unescape(str(value))
    text = re.sub(r'<su(?:p|per)>(.*?)</su(?:p|per)>', r'^{\1}', text, flags=re.I)
    text = re.sub(r'<sub>(.*?)</sub>', r'_{\1}', text, flags=re.I)
    text = re.sub(r'[⁰¹²³⁴⁵⁶⁷⁸⁹⁻⁺]+', lambda m: '^{'+m.group().translate(_SUPER)+'}', text)
    text = text.replace('−', '-')
    if text.startswith('$') and text.endswith('$'):
        text = text.strip('$')
    return ' '.join(text.split())


def formula_text(text):
    text = str(text).strip().strip('$')
    text = text.replace(r'\left', '').replace(r'\right', '').replace('−', '-')
    return re.sub(r'\s+', '', text)


def iou(a, b):
    if not isinstance(a, list) or not isinstance(b, list) or len(a) != 4 or len(b) != 4:
        return 0
    width=max(0,min(a[2],b[2])-max(a[0],b[0]));height=max(0,min(a[3],b[3])-max(a[1],b[1]))
    overlap=width*height
    union=max(0,a[2]-a[0])*max(0,a[3]-a[1])+max(0,b[2]-b[0])*max(0,b[3]-b[1])-overlap
    return overlap/union if union else 0


def pair_by_page_geometry(expected, actual):
    """Pair by authored geometry/page, never by maximizing OCR cell matches."""
    remaining=set(range(len(actual)))
    pairs=[]
    for golden in expected:
        candidates=[i for i in remaining if actual[i].get('page') == golden['page']]
        if not candidates:
            pairs.append((golden,None));continue
        with_boxes=[i for i in candidates if actual[i].get('bbox')]
        index=max(with_boxes,key=lambda i:iou(golden.get('bbox'),actual[i]['bbox'])) if with_boxes else min(candidates)
        remaining.remove(index);pairs.append((golden,actual[index]))
    return pairs,[actual[i] for i in sorted(remaining)]


def span_set(rows):
    return {(ri,ci,c.get('rowspan',1),c.get('colspan',1)) for ri,row in enumerate(rows) for ci,c in enumerate(row)
            if isinstance(c,dict) and (c.get('rowspan',1)>1 or c.get('colspan',1)>1)}


def score_fixture(gold, bundle):
    if bundle.get('source',{}).get('sha256') != gold['sha256']:
        raise ValueError('Normalized bundle does not bind the authored source PDF SHA256')
    pairs,extra=pair_by_page_geometry(gold['tables'],bundle.get('fragments',[]))
    tables=[]
    total=matched=numeric_total=numeric_matched=0
    for expected,actual in pairs:
        expected_rows=expected['rows'];actual_rows=actual.get('rows',[]) if actual else []
        mismatches=[];exact=0
        for ri,row in enumerate(expected_rows):
            for ci,value in enumerate(row):
                got=actual_rows[ri][ci] if ri<len(actual_rows) and ci<len(actual_rows[ri]) else None
                equal=got is not None and cell_text(got)==cell_text(value)
                total+=1;matched+=equal;exact+=equal
                numeric=bool(_NUMERIC_LITERAL.fullmatch(value))
                numeric_total+=numeric;numeric_matched+=numeric and equal
                if not equal:
                    mismatches.append(dict(row=ri,column=ci,expected=value,actual=None if got is None else got.get('text') if isinstance(got,dict) else str(got)))
        expected_spans={(s['row'],s['column'],s['rowspan'],s['colspan']) for s in expected['spans']}
        actual_spans=span_set(actual_rows)
        shape_exact=(len(actual_rows)==len(expected_rows) and all(len(row)==expected['column_count'] for row in actual_rows))
        blanks_expected=expected['authored_blank_rows']
        blanks_actual=[ri for ri,row in enumerate(actual_rows) if row and all(cell_text(c)=='' for c in row)]
        table=dict(gold_table_id=expected['id'],fragment_id=actual.get('id') if actual else None,
                   expected_page=expected['page'],actual_page=actual.get('page') if actual else None,
                   expected_rows=len(expected_rows),actual_rows=len(actual_rows),shape_exact=shape_exact,
                   expected_columns=expected['column_count'],actual_row_widths=sorted({len(r) for r in actual_rows}),
                   cells_exact=exact,cells_expected=sum(len(r) for r in expected_rows),mismatches=mismatches,
                   span_geometry_exact=expected_spans==actual_spans,expected_spans=sorted(expected_spans),actual_spans=sorted(actual_spans),
                   authored_blank_rows=blanks_expected,actual_blank_rows=blanks_actual,blank_rows_exact=blanks_expected==blanks_actual,
                   bbox_iou=iou(expected.get('bbox'),actual.get('bbox')) if actual else 0,
                   exact=shape_exact and not mismatches and expected_spans==actual_spans and blanks_expected==blanks_actual)
        tables.append(table)
    formula_actual=list(bundle.get('formulas',[]))
    for fragment in bundle.get('fragments',[]):
        formula_actual.extend(fragment.get('formulas',[]))
    formula_pairs,extra_formulas=pair_by_page_geometry(gold.get('formulas',[]),formula_actual)
    formulas=[dict(id=g['id'],expected_page=g['page'],expected_latex=g['latex'],
                   actual_latex=a.get('latex') if a else None,
                   exact=a is not None and formula_text(g['latex'])==formula_text(a.get('latex','')))
              for g,a in formula_pairs]
    structural_exact=len(extra)==0 and all(t['shape_exact'] and t['span_geometry_exact'] and t['blank_rows_exact'] for t in tables)
    return dict(fixture_id=gold['id'],source_sha256=gold['sha256'],transport_completed=True,
                expected_table_count=len(gold['tables']),actual_table_count=len(bundle.get('fragments',[])),
                table_structure_exact=structural_exact,all_table_cells_exact=all(t['exact'] for t in tables) and not extra,
                cells_exact=matched,cells_expected=total,cell_accuracy=matched/total if total else None,
                numeric_literals_exact=numeric_matched,numeric_literals_expected=numeric_total,
                numeric_literal_accuracy=numeric_matched/numeric_total if numeric_total else None,
                extra_fragments=[dict(id=f.get('id'),page=f.get('page')) for f in extra],tables=tables,
                formulas_expected=len(formulas),formulas_exact=sum(f['exact'] for f in formulas),
                formula_structure_exact=all(f['exact'] for f in formulas) and not extra_formulas,
                formulas=formulas,extra_formula_count=len(extra_formulas),
                expected_logical_groups=gold.get('expected_logical_groups',[]),forbidden_merges=gold.get('forbidden_merges',[]),
                publication_expectation=gold.get('publication_expectation'),
                logical_review_executed=False,store_publication_executed=False,acceptance_complete=False,
                notice='Strict authored extraction checks only. HTTP success is not recognition correctness; model continuation decisions, manual review, store publication and final QA are not executed by this harness.')

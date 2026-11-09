"""Synthetic chart inputs and expected-source identities, confined to tests."""
from copy import deepcopy
from datetime import date, timedelta

SID = 'kb-' + 'a' * 48
OTHER = 'kb-' + 'b' * 48
THIRD = 'kb-' + 'c' * 48
KEY = 'nc-' + '1' * 32


def definition(**changes):
    return {'format': 'chart.v1', 'title': '测试图', 'group': '测试组', 'note': '',
            'curves': [{'series_id': SID, 'label': '测试曲线', 'kind': 'line', 'axis': 'left'}],
            'time': {'mode': 'recent', 'points': 120}, **changes}


def native_curve(**changes):
    return {'label': '任意图例', 'chart_type': 'lineChart', 'axis': 'primary', 'series_id': SID,
            'status': 'linked', 'reason': '', 'first_date': '2020-01-04', 'last_date': '2020-12-31', 'unit': '件',
            'descriptor': {'sheet': '测试表', 'metric': '真实字段', 'column': 'M', 'start_row': 3, 'end_row': 368},
            'style': {'color': '#C00000', 'width_pt': 2.25, 'dash': 'solid'}, **changes}


def native_chart(**changes):
    return {'chart_key': KEY, 'knowledge_id': 'fixture-document', 'source_file': 'fixture.xlsx',
            'sheet': '测试表', 'title': '名字没有判定意义', 'axes': [], 'curves': [native_curve()], **changes}


def yearly_native(start='01-04', end='12-31', **changes):
    curves = [native_curve(label=label, first_date=f'{year}-{start}', last_date=f'{year}-{end}')
              for label, year in zip(('早段', '中段', '晚段'), (2020, 2021, 2022))]
    return native_chart(curves=curves, **changes)


def segment_fixture(transform='raw'):
    starts = [date(2020, month, 1) for month in range(1, 5)]
    template = {'name': '隔离样例', 'kind': 'segments', 'transform': transform,
                'segments': [{'label': f'段{index}', 'start': start.isoformat(),
                              'end': (start + timedelta(days=9)).isoformat()} for index, start in enumerate(starts)]}
    observations = [(start + timedelta(days=offset), (index + 1) * 10 + offset)
                    for index, start in enumerate(starts) for offset in range(10)]
    return template, observations


FIELDS = {SID: {'series_id': SID, 'metric': '字段甲', 'unit': '件'},
          OTHER: {'series_id': OTHER, 'metric': '字段乙', 'unit': '件'},
          THIRD: {'series_id': THIRD, 'metric': '字段丙', 'unit': '元'}}


def copied_fields():
    return deepcopy(FIELDS)

"""Bounded, factual progress for workbook tasks; phases are never weighted."""
from __future__ import annotations


STAGES = {
    'scan': '识别工作表结构',
    'review': '逐块检查待确认工作表',
    'parse': '逐表校验并解析数据',
    'preview': '检查选定区块的全部数据',
    'publish': '保存结构化数据',
    'index': '准备查询索引',
    'batch': '逐表校验并导入',
    'ai': '逐个辅助核对待确认区块',
}


def progress_value(value):
    """Discard arbitrary child text and accept only bounded work-unit counts."""
    if not isinstance(value, dict) or set(value) - {'phase', 'completed', 'total', 'stage'}:
        raise ValueError('工作表进度信息无效。')
    phase = value.get('phase')
    if not isinstance(phase, str) or phase not in STAGES:
        raise ValueError('工作表进度阶段无效。')
    result = {'phase': phase, 'stage': STAGES[phase]}
    if 'completed' in value or 'total' in value:
        completed, total = value.get('completed'), value.get('total')
        if (type(completed) is not int or type(total) is not int
                or not 0 <= completed <= total <= 1_000_000 or total == 0):
            raise ValueError('工作表进度计数无效。')
        result.update(completed=completed, total=total)
    return result


def report_progress(callback, phase, *, completed=None, total=None):
    if callback is None:
        return
    value = {'phase': phase}
    if completed is not None or total is not None:
        value.update(completed=completed, total=total)
    callback(progress_value(value))

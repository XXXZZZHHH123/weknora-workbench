"""Per-workbench research dimensions: shared contract for the dimension page,
judgment dimension tags and the one-page report.

Each workbench keeps its own ordered list of dimensions. Without a saved list
the six general dimensions of the research framework apply, with fixed ids, so
tags written before any customization stay valid. Every dimension carries one
general category (a framework group or 其他) that gives models and cross-variety
summaries a stable meaning; labels and descriptions are the researcher's words.
Ids are stable: renaming never changes them, and a deleted id is reported as
such by callers instead of being silently remapped.
"""
from copy import deepcopy
import re

from fastapi import HTTPException

from tin.web.research_framework import RESEARCH_GROUPS, dimension_label

DIMENSIONS_KEY = 'workbench-research-dimensions'
CATEGORIES = RESEARCH_GROUPS + ('其他',)
MAX_DIMENSIONS = 12
DIMENSION_ID = re.compile(r'[a-z][a-z0-9_-]{1,31}')

_DEFAULT_IDS = dict(zip(RESEARCH_GROUPS, ('price', 'supply', 'demand', 'inventory', 'balance', 'macro')))
_DEFAULT_DESCRIPTIONS = {
    '核心标的': '国内外价格、现货升贴水、期限结构（近远月价差）与内外比价。',
    '供给': '产量、进口、开工与供应端变化，具体对象由资料决定。',
    '需求': '下游开工、订单、消费与出口，具体对象由资料决定。',
    '库存': '交易所仓单、社会库存、企业库存等各类库存口径。',
    '平衡': '供给量、消费量、供需差与库存变化等平衡表口径。',
    '宏观': '可观测的外部变量：指数、利率、汇率、相关商品价格等。',
}


def default_dimensions():
    return [{'id': _DEFAULT_IDS[group], 'label': dimension_label(group), 'category': group,
             'description': _DEFAULT_DESCRIPTIONS[group]} for group in RESEARCH_GROUPS]


def validate_dimension_list(dimensions):
    """Check the shared fields; callers may keep extra per-dimension data (e.g. sections)."""
    if not isinstance(dimensions, list) or not 1 <= len(dimensions) <= MAX_DIMENSIONS:
        raise HTTPException(409, f'研究维度配置无效：需要 1–{MAX_DIMENSIONS} 个维度')
    seen_ids, seen_labels = set(), set()
    for item in dimensions:
        if (not isinstance(item, dict) or not isinstance(item.get('id'), str)
                or not DIMENSION_ID.fullmatch(item['id'])
                or not isinstance(item.get('label'), str) or not 1 <= len(item['label'].strip()) <= 24
                or item.get('category') not in CATEGORIES
                or not isinstance(item.get('description', ''), str) or len(item.get('description', '')) > 200):
            raise HTTPException(409, '研究维度配置无效：维度需要编号、名称（≤24字）、通用类别与说明（≤200字）')
        label = item['label'].strip()
        if item['id'] in seen_ids or label in seen_labels:
            raise HTTPException(409, '研究维度的编号和名称不能重复')
        seen_ids.add(item['id'])
        seen_labels.add(label)
    return dimensions


def read_dimensions(session):
    """The workbench's dimensions in order (defaults when none were saved).

    Returns full saved entries (callers that own extra fields read them here);
    use summaries() when only the shared contract is needed.
    """
    from tin.models import SourceMapping
    from tin.web.restricted_views import mapping_key
    row = session.get(SourceMapping, mapping_key(session, DIMENSIONS_KEY))
    config = row.config if row is not None else None
    if not isinstance(config, dict) or 'dimensions' not in config:
        return default_dimensions()
    return deepcopy(validate_dimension_list(config['dimensions']))


def summaries(dimensions):
    return [{key: item.get(key, '') for key in ('id', 'label', 'category', 'description')}
            for item in dimensions]


def checked_dimension(value, dimensions):
    """A tag is kept only when it names a current dimension id; no inference."""
    return value if isinstance(value, str) and value in {item['id'] for item in dimensions} else None


def stale_dimension_reason(value, dimensions, labels=None):
    if value is None or checked_dimension(value, dimensions) is not None:
        return None
    known = {item['id']: item['label'] for item in default_dimensions()}
    known.update(labels or {})
    return f'维度「{known.get(value, value)}」已不在本研究台的研究维度中，请改选或设为未标注'


def dimension_prompt(dimensions):
    """Describe this workbench's own dimensions to a model, in order."""
    lines = ['\n【本研究台的研究维度】按下列维度归类，只能使用这里列出的维度编号：']
    for item in summaries(dimensions):
        category = dimension_label(item['category']) if item['category'] in RESEARCH_GROUPS else '其他'
        description = ('：' + item['description']) if item['description'] else ''
        lines.append(f"- {item['id']}（{item['label']}，通用类别：{category}）{description}")
    return '\n'.join(lines)

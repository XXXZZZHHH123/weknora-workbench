"""Built-in report structures; these templates never select or bind indicators."""
from copy import deepcopy
from uuid import uuid4

from fastapi import HTTPException

from tin.web.research_dimensions import default_dimensions, validate_dimension_list


GENERAL_TEMPLATE_ID = 'general-six'
TEMPLATE_DATA = (
    {
        'id': GENERAL_TEMPLATE_ID,
        'name': '通用六维度',
        'applicability': '适用于各类品种，从通用研究框架开始整理。',
        'dimensions': [{**dim, 'sections': []} for dim in default_dimensions()],
    },
    {
        'id': 'textile-chain',
        'name': '纺织产业链',
        'applicability': '适用于棉花、棉纱、纺织相关品种。',
        'dimensions': [
            {'label': '价格与价差', 'category': '核心标的',
             'description': '观察郑棉、美棉与现货价格，以及内外、月间、基差和进口利润口径。',
             'sections': ['郑棉/美棉价格', '内外价差', '月间价差', '现货基差', '进口利润']},
            {'label': '美棉供给', 'category': '供给',
             'description': '跟踪美国棉花供需报告、种植天气、出口销售、库存与未点价情况。',
             'sections': ['USDA 报告', '种植意向与天气', '出口销售', '美棉库存与未点价']},
            {'label': '国内供给与进口', 'category': '供给',
             'description': '观察国内棉资源进口及巴西、印度、澳大利亚等主要出口国的出口情况。',
             'sections': ['棉资源进口', '主要出口国（巴西、印度、澳大利亚）出口']},
            {'label': '纺纱需求', 'category': '需求',
             'description': '跟踪纱价、纺纱利润、国内纱厂负荷与成品库存及海外纱厂负荷。',
             'sections': ['纱价与纺纱利润', '纱厂负荷与成品库存', '海外纱厂负荷']},
            {'label': '织布与终端需求', 'category': '需求',
             'description': '观察织布生产与利润、国内纺服消费和出口，以及美国服装销售与进口。',
             'sections': ['布厂负荷', '坯布价格与利润', '国内纺服零售与出口', '美国服装销售与进口']},
            {'label': '库存与仓单', 'category': '库存',
             'description': '跟踪郑棉仓单、流通库存以及工商业库存等不同库存口径。',
             'sections': ['郑棉仓单', '流通库存', '工商业库存']},
            {'label': '替代品', 'category': '其他',
             'description': '观察涤纶与粘胶产业链的价格、生产和需求资料。',
             'sections': ['涤纶产业链', '粘胶产业链']},
            {'label': '资金与宏观', 'category': '宏观',
             'description': '跟踪基金持仓与汇率等资金和外部变量。',
             'sections': ['基金持仓', '汇率']},
        ],
    },
)


def template_list():
    """Load a fresh, validated copy, rejecting any non-structural data."""
    templates, seen = [], set()
    for source in TEMPLATE_DATA:
        template = deepcopy(source)
        if (set(template) != {'id', 'name', 'applicability', 'dimensions'}
                or not isinstance(template['id'], str) or not template['id'].strip() or template['id'] in seen
                or not isinstance(template['name'], str) or not template['name'].strip()
                or not isinstance(template['applicability'], str) or not template['applicability'].strip()
                or not isinstance(template['dimensions'], list)):
            raise ValueError('维度模板需要唯一编号、名称、适用说明和结构列表')
        seen.add(template['id'])
        validation = []
        for index, dim in enumerate(template['dimensions']):
            allowed = {'label', 'category', 'description', 'sections'}
            if template['id'] == GENERAL_TEMPLATE_ID:
                allowed.add('id')
            if not isinstance(dim, dict) or set(dim) != allowed:
                raise ValueError('维度模板只能包含名称、通用类别、说明和环节名，不能包含指标绑定')
            sections = dim['sections']
            if (not isinstance(sections, list) or len(sections) > 8
                    or any(not isinstance(label, str) or not 1 <= len(label.strip()) <= 40 for label in sections)
                    or len(set(sections)) != len(sections)):
                raise ValueError('维度模板环节需要唯一名称，每维最多8个环节')
            validation.append({**dim, 'id': dim.get('id', f'template-{index}')})
        validate_dimension_list(validation)
        if template['id'] == GENERAL_TEMPLATE_ID and ([
                {key: value for key, value in dim.items() if key != 'sections'}
                for dim in template['dimensions']] != default_dimensions()
                or any(dim['sections'] for dim in template['dimensions'])):
            raise ValueError('通用六维度必须沿用默认框架和固定编号')
        templates.append(template)
    return templates


def get_template(template_id):
    for template in template_list():
        if template['id'] == template_id:
            return template
    raise HTTPException(404, '维度模板不存在')


def merge_template(template_id, previous):
    """Build an unsaved draft, retaining existing sections, items and unmatched dimensions.

    Matches use explicit dimension ids or exact names only; template prose never
    classifies an indicator. Temporary drafts may exceed the saved-config limits.
    """
    template = get_template(template_id)
    remaining = deepcopy(previous)
    matches = [None] * len(template['dimensions'])
    # Reserve all id matches before matching names, so an earlier template
    # label cannot consume another dimension's stable, referenced id.
    for index, source in enumerate(template['dimensions']):
        match = next((dim for dim in remaining if source.get('id') == dim['id']), None)
        if match is not None:
            matches[index] = match
            remaining.remove(match)
    for index, source in enumerate(template['dimensions']):
        if matches[index] is None:
            match = next((dim for dim in remaining if source['label'] == dim['label']), None)
            if match is not None:
                matches[index] = match
                remaining.remove(match)
    result = []
    for source, match in zip(template['dimensions'], matches):
        dim = {key: source[key] for key in ('label', 'category', 'description')}
        dim['id'] = match['id'] if match else source.get('id') or 'd-' + uuid4().hex[:8]
        old_sections = deepcopy(match.get('sections', [])) if match else []
        sections = []
        for label in source['sections']:
            existing = next((section for section in old_sections if section['label'] == label), None)
            if existing is not None:
                old_sections.remove(existing)
                sections.append(existing)
            else:
                sections.append({'id': 's-' + uuid4().hex[:8], 'label': label, 'items': []})
        dim['sections'] = sections + old_sections
        result.append(dim)
    result.extend({**dim, 'template_extra': True} for dim in remaining)
    if len(result) > 24 or any(len(dim.get('sections', [])) > 16 for dim in result):
        raise HTTPException(409, '模板草稿最多暂存24个维度、每维16个环节，请先精简当前草稿后再套用')
    return result


def draft_warnings(dimensions):
    warnings = []
    if len(dimensions) > 12:
        warnings.append(f'草稿有{len(dimensions)}个维度，请删减至12个以内后保存或让模型建议指标。')
    if any(len(dim.get('sections', [])) > 8 for dim in dimensions):
        warnings.append('部分维度超过8个环节，请删减至每维8个以内后保存或让模型建议指标。')
    labels = [dim['label'] for dim in dimensions]
    if len(labels) != len(set(labels)):
        warnings.append('草稿有同名维度，请改名或删除重复维度后保存。')
    return warnings

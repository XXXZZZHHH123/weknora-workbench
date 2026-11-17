"""Names for explicit saved dimension tags; no classification."""


def dimension_name(identifier, dimensions):
    if not identifier:
        return '未标注'
    return next((item['label'] for item in dimensions if item['id'] == identifier), '已删除维度')

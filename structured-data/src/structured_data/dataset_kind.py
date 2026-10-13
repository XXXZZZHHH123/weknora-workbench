"""Separate known development fixtures without guessing from business keywords."""
SAMPLE_FILES = frozenset({
    '锡研究_验收样例_非行情.xlsx',
    '中哲_资料中心_验收表_非行情.xlsx',
    '中哲_范围核对验收_非行情.xlsx',
})


def dataset_kind(document):
    explicit = document.get('dataset_kind')
    if explicit in {'business', 'sample'}:
        return explicit
    return 'sample' if document.get('source_file') in SAMPLE_FILES else 'business'

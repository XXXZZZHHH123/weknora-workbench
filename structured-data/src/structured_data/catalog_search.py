"""Explainable catalogue aliases. Never change source names, units or values."""
import re
import unicodedata

CURRENCIES = {
    'USD': ('美元', '美金', 'usd'), 'CNY': ('人民币', 'cny', 'rmb'),
    'CNH': ('离岸人民币', 'cnh'), 'EUR': ('欧元', 'eur'),
    'JPY': ('日元', 'jpy'), 'GBP': ('英镑', 'gbp'),
    'HKD': ('港元', '港币', 'hkd'), 'AUD': ('澳元', 'aud'),
    'CAD': ('加元', 'cad'), 'INR': ('印度卢比', 'inr'),
}


def normalize(text):
    return unicodedata.normalize('NFKC', str(text)).lower().strip()


def currency_pair(series):
    title = re.sub(r'\s+', '', normalize(series.get('metric', '')))
    # A currency-denominated price is not an exchange rate. Match pair titles
    # rather than currency words in descriptions or units. Preserve direction.
    for base, names in CURRENCIES.items():
        for quote, targets in CURRENCIES.items():
            if base == quote:
                continue
            pair = '(?:'+'|'.join(map(re.escape,names))+')(?:兑|兑换|对|/)(?:'+'|'.join(map(re.escape,targets))+')'
            if re.fullmatch(r'(?:即期|现汇)?'+pair+r'(?:汇率|中间价|即期汇率)?', title):
                return base,quote
    return None


def aliases_for(series):
    # Only explicitly stored aliases belong in literal search. Semantic field
    # discovery is delegated to FieldResolver, not a growing synonym dictionary.
    return list(dict.fromkeys(a for a in series.get('aliases', []) if isinstance(a, str) and a.strip()))


def search_text(series, aliases=()):
    import json
    return normalize(json.dumps(series, ensure_ascii=False)+' '+' '.join(aliases))

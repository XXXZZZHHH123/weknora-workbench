"""Explainable catalogue aliases. Never change source names, units or values."""
import re
import unicodedata


def normalize(text):
    return unicodedata.normalize('NFKC', str(text)).lower().strip()


def currency_pair(series):
    """Use reviewed source metadata; a title is recall evidence, not direction."""
    rate = series.get('exchange_rate')
    if not isinstance(rate, dict):
        return None
    base, quote = rate.get('base_currency'), rate.get('quote_currency')
    evidence = rate.get('evidence_cells')
    if (not isinstance(base, str) or not isinstance(quote, str)
            or not re.fullmatch('[A-Z]{3}', base) or not re.fullmatch('[A-Z]{3}', quote)
            or base == quote or not isinstance(evidence, list) or not evidence
            or any(cell not in series.get('header_cells', []) for cell in evidence)):
        return None
    return base, quote


def aliases_for(series):
    # Only explicitly stored aliases belong in literal search. Semantic field
    # discovery is delegated to FieldResolver, not a growing synonym dictionary.
    return list(dict.fromkeys(a for a in series.get('aliases', []) if isinstance(a, str) and a.strip()))


def search_text(series, aliases=()):
    import json
    return normalize(json.dumps(series, ensure_ascii=False)+' '+' '.join(aliases))

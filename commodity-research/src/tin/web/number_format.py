"""Shared display precision; never round stored values or computation inputs."""
from decimal import Decimal, InvalidOperation, localcontext


def source_decimals(*metadata):
    """Read explicit source precision, without inferring it from units or names."""
    for item in metadata:
        if not isinstance(item, dict):
            continue
        for key in ('display_decimals', 'decimals'):
            digits = item.get(key)
            if type(digits) is int and 0 <= digits <= 15:
                return digits
        for key in ('caliber', 'caliber_snapshot'):
            digits = source_decimals(item.get(key))
            if digits is not None:
                return digits
    return None


def observation_decimals(session, identifier, stamp, *metadata):
    """Use only precision already present in the authorized facts response."""
    precision = session.info.get('knowledge_facts_display_precision') or {}
    digits = (precision.get(identifier) or {}).get(str(stamp)[:10])
    return source_decimals({'display_decimals': digits}, *metadata,
        (session.info.get('knowledge_facts_catalog') or {}).get(identifier))


def format_number(value, *, decimals=None, signed=False):
    if value is None or isinstance(value, bool):
        return '—'
    try:
        number = Decimal(str(value))
    except (InvalidOperation, ValueError, TypeError):
        return str(value) if isinstance(value, str) else '—'
    if not number.is_finite():
        return '—'
    magnitude = number.copy_abs()
    explicit_precision = type(decimals) is int and 0 <= decimals <= 15
    if not explicit_precision:
        decimals = (0 if magnitude >= 1000 else 1 if magnitude >= 100 else 2 if magnitude >= 1
                    else max(0, 3 - magnitude.adjusted()) if magnitude else 0)
    with localcontext() as context:
        context.prec = max(34, number.adjusted() + decimals + 4, len(number.as_tuple().digits) + 4)
        shown = number.quantize(Decimal(1).scaleb(-decimals))
        if shown == 0:
            shown = abs(shown)
        text = format(shown, '+,f' if signed else ',f')
    return text.rstrip('0').rstrip('.') if not explicit_precision and '.' in text else text

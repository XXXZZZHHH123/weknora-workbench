"""Frequency read from the spacing of real observations; catalog labels are often absent."""


def observed_frequency(dates):
    if len(dates) < 3:
        return None
    ordered = sorted(dates)
    gaps = sorted((later - earlier).days for earlier, later in zip(ordered, ordered[1:]))
    middle = gaps[len(gaps) // 2]
    for limit, label in ((4, '日'), (10, '周'), (45, '月'), (135, '季'), (220, '半年')):
        if middle <= limit:
            return label
    return '年'

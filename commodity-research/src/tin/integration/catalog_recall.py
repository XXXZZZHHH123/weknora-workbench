"""Ranked metadata recall over an already authorized catalog.

Recall only: it decides which real fields a model gets to look at, never which
field is chosen, bound or authorized. The caller's model reads the candidates and
decides; the program then checks every returned reference.

The method mirrors the structured service's question index
(structured_data/field_index.py): Chinese character bigrams plus Latin/number
words, scored with BM25 where the field name weighs more than its context
(sheet, unit, frequency, dimensions, source file). Partial overlap therefore
still ranks a field, unlike the indicator page search, which needs every word.
"""
from collections import Counter, defaultdict
import math
import re
import unicodedata

_DATE = re.compile(r'(?<!\d)\d{4}[-/年]\d{1,2}[-/月]\d{1,2}日?(?!\d)')
NAME_WEIGHT, CONTEXT_WEIGHT = 5.0, 1.0
K1, B = 1.2, 0.75
MAX_QUERY_TOKENS = 160


def tokens(text):
    text = _DATE.sub(' ', unicodedata.normalize('NFKC', str(text)).lower())
    result = []
    for part in re.findall(r'[a-z]+\d*|\d+|[一-鿿]+', text):
        if re.fullmatch(r'[一-鿿]+', part):
            # A lone character is kept so one-character labels remain findable.
            result.extend(part[index:index + 2] for index in range(len(part) - 1)) if len(part) > 1 else result.append(part)
        else:
            result.append(part)
            if re.search(r'\d', part):
                result.extend(re.findall(r'\d+', part))
    return result


def _flatten(value):
    if isinstance(value, dict):
        return ' '.join(_flatten(item) for item in value.values())
    if isinstance(value, (list, tuple)):
        return ' '.join(_flatten(item) for item in value)
    return '' if value is None else str(value)


class CatalogRecall:
    """In-memory BM25 over (identifier, name, context) rows of one catalog snapshot."""

    def __init__(self, rows):
        self.ids = []
        self.postings = {'name': defaultdict(list), 'context': defaultdict(list)}
        self.lengths = {'name': [], 'context': []}
        for identifier, name, context in rows:
            self.ids.append(identifier)
            for column, text in (('name', name), ('context', context)):
                counts = Counter(tokens(_flatten(text)))
                self.lengths[column].append(sum(counts.values()))
                for token, count in counts.items():
                    self.postings[column][token].append((len(self.ids) - 1, count))
        total = len(self.ids)
        self.average = {column: (sum(values) / total if total else 0) or 1 for column, values in self.lengths.items()}

    def scores(self, query):
        result = defaultdict(float)
        total = len(self.ids)
        for token in list(dict.fromkeys(tokens(query)))[:MAX_QUERY_TOKENS]:
            for column, weight in (('name', NAME_WEIGHT), ('context', CONTEXT_WEIGHT)):
                posting = self.postings[column].get(token)
                if not posting:
                    continue
                idf = math.log(1 + (total - len(posting) + 0.5) / (len(posting) + 0.5))
                for index, count in posting:
                    norm = K1 * (1 - B + B * self.lengths[column][index] / self.average[column])
                    result[index] += weight * idf * count * (K1 + 1) / (count + norm)
        return result

    def ranked(self, query, limit):
        scored = self.scores(query)
        order = sorted(scored, key=lambda index: (-scored[index], self.ids[index]))[:limit]
        return [self.ids[index] for index in order]

    def recall(self, queries, limit):
        """Interleave each query's best fields so one broad phrase cannot crowd out the others."""
        lists = [self.ranked(query, limit) for query in queries if isinstance(query, str) and query.strip()]
        selected = []
        for position in range(limit):
            for ranked in lists:
                if position < len(ranked) and ranked[position] not in selected:
                    selected.append(ranked[position])
                    if len(selected) == limit:
                        return selected
        return selected

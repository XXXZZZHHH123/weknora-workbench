"""Local metadata retrieval. Scores recall candidates; they never authorize a value query."""
from hashlib import sha256
import json
import re
import sqlite3
import unicodedata


def normalize(text):
    return unicodedata.normalize('NFKC', str(text)).lower()


def without_date(text):
    return re.sub(r'(?<!\d)\d{4}[-/年]\d{1,2}[-/月]\d{1,2}日?(?!\d)', ' ', normalize(text))


def literal_fields(catalog, question):
    """Recall explicit headers without interpreting intent or choosing a definition."""
    q=normalize(question)
    for sheet in sorted({s['sheet'] for s in catalog},key=len,reverse=True):
        # A sheet such as 原料 can be part of the actual header 20天原料.
        # Removing that substring would hide an explicit literal field.
        if any(normalize(sheet) in normalize(s['metric']) for s in catalog):
            continue
        q=q.replace(normalize(sheet),' ')
    matches=[s for s in catalog if len(normalize(s['metric']))>=3 and normalize(s['metric']) in q]
    # A longer named header may contain a different, shorter definition.
    return [s for s in matches if not any(normalize(s['metric']) != normalize(other['metric'])
            and normalize(s['metric']) in normalize(other['metric']) for other in matches)]


def tokens(text):
    # Chinese bigrams require no external tokenizer; numbers/specifications remain
    # searchable across adjacent Latin letters (3128 vs 3128B, CF01 vs 01).
    result=[]
    for part in re.findall(r'[a-z]+\d*|\d+|[\u4e00-\u9fff]+', without_date(text)):
        if re.fullmatch(r'[\u4e00-\u9fff]+',part):
            result.extend(part[i:i+2] for i in range(len(part)-1))
        else:
            result.append(part)
            if re.search(r'\d',part):result.extend(re.findall(r'\d+',part))
    return list(dict.fromkeys(result))[:160]


class FieldIndex:
    """Versioned SQLite FTS index of the actual catalogue, rebuilt transactionally."""
    def __init__(self, root, catalog):
        self.catalog=catalog
        self.lookup={s['series_id']:s for s in catalog}
        fingerprint=sha256(json.dumps(catalog,ensure_ascii=False,sort_keys=True).encode()).hexdigest()
        root.mkdir(parents=True,exist_ok=True)
        # Scope/question-dependent chart views get separate fingerprints as well.
        self.path=root/(fingerprint+'.sqlite')
        with sqlite3.connect(self.path,timeout=30) as con:
            con.execute('BEGIN IMMEDIATE')
            con.execute('CREATE VIRTUAL TABLE IF NOT EXISTS fields USING fts5(id UNINDEXED, metric, context)')
            if con.execute('SELECT count(*) FROM fields').fetchone()[0]!=len(catalog):
                con.execute('DELETE FROM fields')
                for s in catalog:
                    metric=' '.join([s['metric'],*map(str,(s.get('dimensions') or {}).values()),*s.get('aliases',[])])
                    context=json.dumps({k:s.get(k) for k in ('sheet','unit','chart_titles','chart_legends','source_metric','source_context')},ensure_ascii=False)
                    con.execute('INSERT INTO fields VALUES (?,?,?)',(s['series_id'],' '.join(tokens(metric)),' '.join(tokens(context))))

    def search(self, queries, prior_ids=(), context_sheet=None, limit=24):
        scores={};terms=[];per_query=[]
        with sqlite3.connect(self.path) as con:
            for query in queries[:9]:
                ts=tokens(query)
                if not ts:continue
                terms.append(query)
                match=' OR '.join('"'+t+'"' for t in ts)
                rows=con.execute('SELECT id,bm25(fields,0,5,1) FROM fields WHERE fields MATCH ? ORDER BY bm25(fields,0,5,1), id LIMIT ?', (match,limit)).fetchall()
                per_query.extend(sid for sid,_ in rows[:4])
                for sid,score in rows:
                    scores[sid]=max(scores.get(sid,0),-score)
        ranked=sorted(scores,key=lambda sid:(-scores[sid],self.lookup[sid].get('sheet')!=context_sheet,sid))
        # Preserve candidates from each expanded search (e.g. a conversion factor),
        # rather than letting a heavily duplicated price crowd all factors out.
        named=[s['series_id'] for s in literal_fields(self.catalog,queries[0])] if queries else []
        named_sheets={s['sheet'] for s in self.catalog if queries and normalize(s['sheet']) in normalize(queries[0])}
        # Include the actual named sheet's bounded columns even when generic
        # headers (price/volume) have less lexical overlap than other sheets.
        sheet_fields=[s['series_id'] for s in self.catalog if s['sheet'] in named_sheets]
        if len(sheet_fields)>32:
            sheet_fields=list(dict.fromkeys([sid for sid in ranked if self.lookup[sid]['sheet'] in named_sheets]+sheet_fields))[:32]
        selected=list(dict.fromkeys([*named,*prior_ids,*sheet_fields,*per_query,*ranked]))[:64]
        return [self.lookup[sid] for sid in selected], {'method':'sqlite_fts5_bm25','queries':terms,'candidate_count':len(selected),'catalog_count':len(self.catalog),'truncated':len(set(named)|set(prior_ids)|set(scores))>len(selected)}

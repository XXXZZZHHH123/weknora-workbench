"""Local metadata retrieval. Scores recall candidates; they never authorize a value query."""
from hashlib import sha256
import json
from os.path import commonprefix
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
            con.execute('CREATE VIRTUAL TABLE IF NOT EXISTS fields USING fts5(id UNINDEXED, metric, context)')
            if con.execute('SELECT count(*) FROM fields').fetchone()[0]!=len(catalog):
                con.execute('DELETE FROM fields')
                for s in catalog:
                    metric=' '.join([s['metric'],*map(str,(s.get('dimensions') or {}).values()),*s.get('aliases',[])])
                    context=json.dumps({k:s.get(k) for k in ('sheet','unit','chart_titles','chart_legends','source_metric')},ensure_ascii=False)
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
        selected=list(dict.fromkeys([*named,*prior_ids,*per_query,*ranked]))[:64]
        return [self.lookup[sid] for sid in selected], {'method':'sqlite_fts5_bm25','queries':terms,'candidate_count':len(selected),'catalog_count':len(self.catalog),'truncated':len(set(named)|set(prior_ids)|set(scores))>len(selected)}


def exact_lookup(catalog, question, date, prior_ids=()):
    """Only a literal single-field request may skip semantic planning. Never top-1."""
    if not date or prior_ids or '\n' in question:return None
    q=without_date(question)
    # This grammar strips only lookup phrasing, not business synonyms or units.
    def compact(t):return re.sub(r'[\s，,。？?：:]', '', normalize(t))
    q=compact(q)
    matches=[]
    for s in catalog:
        if (s.get('date_mode') or 'date')!='date':continue
        name=compact(s['metric'])
        if len(name)<4 or name not in q:continue
        dims={k:v for k,v in (s.get('dimensions') or {}).items() if k!='block' and v is not None and str(v).strip()}
        if any(compact(str(v)) not in q for v in dims.values()):continue
        rest=q.replace(name,'',1)
        for value in dims.values():rest=rest.replace(compact(str(value)),'',1)
        for phrase in ('请问','查询','请查','查一下','是多少','多少钱','数值','价格','报价','多少','的'):
            rest=rest.replace(phrase,'')
        if not rest:matches.append(s)
    if len(matches)!=1:return None
    chosen=matches[0]
    # A longer field name can introduce an unspecified definition. Do not bypass
    # clarification merely because one short header happens to match exactly.
    name=compact(chosen['metric'])
    for s in catalog:
        if s['series_id']==chosen['series_id']:continue
        other=compact(s['metric'])
        if name in other or len(commonprefix([name,other]))>=max(4,len(name)/2):return None
    return chosen

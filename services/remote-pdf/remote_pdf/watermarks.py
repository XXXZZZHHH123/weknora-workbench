"""Conservative removal of isolated, repeated transparent text on an OCR copy.

No rectangular redaction or image inpainting: those could erase/guess values.
Only an exclusively used font with repeated diagonal translucent text and an
independent BT/Tf/Tm/Tj/ET object is eligible. Original bytes stay untouched.
"""
from collections import defaultdict
import hashlib
import math
import time
from pathlib import Path

import fitz
from pypdf import PdfReader, PdfWriter

VERSION = 'isolated-transparent-text-1'
MAX_STREAM_BYTES = 16 * 1024 * 1024


def _hash(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream,'sha256').hexdigest()


def _signature(span):
    return (span['font'], round(span['size'],1), round(span.get('opacity',1),2),
            tuple(round(v,2) for v in span['dir']), tuple((c[0],c[1]) for c in span['chars']))


def _candidate(span, page):
    angle=abs(math.degrees(math.atan2(span['dir'][1],span['dir'][0]))) % 180
    angle=min(angle,180-angle)
    box=fitz.Rect(span['bbox']);center=(box.tl+box.br)/2
    return (24 <= span['size'] <= 180 and 0.05 <= span.get('opacity',1) <= .6
            and 20 <= angle <= 70 and 3 <= len(span['chars']) <= 80
            and .2*page.rect.width <= center.x <= .8*page.rect.width
            and .2*page.rect.height <= center.y <= .8*page.rect.height)


def _trace_key(span):
    # seqno changes when a watermark is removed; text/glyphs/positions must not.
    def rounded(value):
        if isinstance(value,(tuple,list)):return tuple(rounded(v) for v in value)
        if isinstance(value,float):return round(value,2)
        return value
    return tuple((k,rounded(span[k])) for k in ('font','size','opacity','dir','chars','type','color') if k in span)


def recognition_copy(source, target):
    if Path(source).resolve()==Path(target).resolve():raise ValueError('Recognition copy must not overwrite the original')
    try:
        return _recognition_copy(source,target)
    except Exception:
        Path(target).unlink(missing_ok=True)
        return Path(source),{'version':VERSION,'source_sha256':_hash(source),'status':'unchanged',
            'removed_objects':0,'pages':[],'reason':'无法安全分离水印，已保留原页继续识别'}
    finally:
        # Image verification must not leave decoded images occupying the
        # worker's memory budget before the actual page render starts.
        fitz.TOOLS.store_shrink(100)


def _recognition_copy(source, target):
    source,target=Path(source),Path(target)
    started=time.monotonic()
    if source.resolve()==target.resolve():raise ValueError('Recognition copy must not overwrite the original')
    receipt={'version':VERSION,'source_sha256':_hash(source),'status':'unchanged','removed_objects':0,'pages':[]}
    target.unlink(missing_ok=True)
    with fitz.open(source) as original:
        if len(original)<3:
            receipt['reason']='页数不足，无法可靠判断重复水印';return source,receipt
        traces=[page.get_texttrace() for page in original]
        occurrences=defaultdict(list)
        for number,page in enumerate(original):
            for span in traces[number]:
                if _candidate(span,page):occurrences[_signature(span)].append((number,span))
        eligible={key for key,items in occurrences.items() if len({n for n,_ in items})>=max(3,math.ceil(.75*len(original)))}
        # Avoid touching a font that also draws any ordinary source content.
        fonts={key[0] for key in eligible}
        fonts-={span['font'] for page in traces for span in page if span['font'] in fonts and _signature(span) not in eligible}
        eligible={key for key in eligible if key[0] in fonts}
        if not eligible:
            receipt['reason']='没有找到可安全分离的重复文字水印';return source,receipt
        expected={n:[s for s in page if _signature(s) in eligible] for n,page in enumerate(traces)}
        reader=PdfReader(source,strict=False);writer=PdfWriter(clone_from=reader)
        removed=defaultdict(list)
        try:
            for number,page in enumerate(writer.pages):
                if time.monotonic()-started>180:raise ValueError('cleanup time budget')
                if not expected[number]:continue
                original_page=original[number]
                resources=page['/Resources'];states=resources.get('/ExtGState',{})
                if hasattr(states,'get_object'):states=states.get_object()
                font_names={name:font[3].split('+')[-1] for font in original_page.get_fonts() for name in ['/'+font[4]]}
                content=page.get_contents()
                if content is None or len(content.get_data())>MAX_STREAM_BYTES:raise ValueError('stream budget')
                operations=content.operations
                matrix=fitz.Matrix(1,1);alpha=1.;stack=[];drops=set();i=0
                while i<len(operations):
                    args,op=operations[i]
                    if op==b'q':stack.append((fitz.Matrix(matrix),alpha))
                    elif op==b'Q':
                        if not stack:raise ValueError('unbalanced graphics state')
                        matrix,alpha=stack.pop()
                    elif op==b'cm':matrix=fitz.Matrix(*map(float,args))*matrix
                    elif op==b'gs':
                        state=states.get(args[0]);state=state.get_object() if state is not None else {}
                        if '/ca' in state:alpha=float(state['/ca'])
                    elif op==b'BT':
                        end=i+1
                        while end<len(operations) and operations[end][1]!=b'ET':end+=1
                        block=operations[i:end+1]
                        kinds=[operation for _,operation in block]
                        if len(block)==5 and sorted(kinds)==sorted([b'BT',b'Tf',b'Tm',b'Tj',b'ET']):
                            font_args=next(a for a,o in block if o==b'Tf');tm=next(a for a,o in block if o==b'Tm')
                            effective=fitz.Matrix(*map(float,tm))*matrix
                            origin=fitz.Point(effective.e,effective.f)*original_page.transformation_matrix
                            size=math.hypot(effective.a,effective.b)*float(font_args[1])
                            font=font_names.get(str(font_args[0]))
                            matches=[s for s in expected[number] if s not in removed[number] and s['font']==font
                                     and abs(s['size']-size)<.05 and abs(s.get('opacity',1)-alpha)<.01
                                     and abs(s['chars'][0][2][0]-origin.x)<.1 and abs(s['chars'][0][2][1]-origin.y)<.1]
                            if len(matches)==1:
                                drops.update(range(i,end+1));removed[number].append(matches[0])
                        # No graphics operators inside an eligible text object.
                        # General mixed objects are kept entirely unchanged.
                    i+=1
                if drops:
                    content.operations=[item for j,item in enumerate(operations) if j not in drops]
                    page.replace_contents(content)
                    page.compress_content_streams()
            if not any(removed.values()):
                receipt['reason']='候选水印与正文绘制交织，保留原页';return source,receipt
            writer.write(target)
            with fitz.open(target) as cleaned:
                if len(cleaned)!=len(original):raise ValueError('page count changed')
                for number,page in enumerate(cleaned):
                    if time.monotonic()-started>180:raise ValueError('verification time budget')
                    before=[_trace_key(s) for s in traces[number] if s not in removed[number]]
                    if before!=[_trace_key(s) for s in page.get_texttrace()]:raise ValueError('source text changed')
                    # Image pixels and geometric placement must survive writing.
                    image_key=lambda p:[(i['digest'],tuple(round(v,2) for v in i['bbox'])) for i in p.get_image_info(hashes=True)]
                    if image_key(original[number])!=image_key(page):raise ValueError('source images changed')
                    drawing_key=lambda p:[tuple((k,str(v)) for k,v in d.items() if k!='seqno') for d in p.get_drawings()]
                    if drawing_key(original[number])!=drawing_key(page):raise ValueError('source graphics changed')
            if receipt['source_sha256']!=_hash(source):raise ValueError('original changed')
            receipt.update(status='cleaned',recognition_sha256=_hash(target),removed_objects=sum(map(len,removed.values())),
                           pages=[n+1 for n,items in removed.items() if items],reason='仅移除独立重复的半透明文字，原文字、图像和图形通过一致性校验')
            return target,receipt
        except Exception:
            target.unlink(missing_ok=True)
            receipt['reason']='无法证明清理前后正文与图形一致，已使用原件识别'
            return source,receipt
        finally:
            writer.close();reader.close()

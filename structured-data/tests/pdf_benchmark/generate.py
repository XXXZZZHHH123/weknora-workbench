"""Generate deterministic authored PDFs/gold; never invokes inference."""
from __future__ import annotations

import argparse
from copy import deepcopy
import hashlib
import io
import json
from pathlib import Path
import re
from xml.sax.saxutils import escape

from PIL import Image, ImageDraw
import pypdfium2 as pdfium
from reportlab.lib.pagesizes import A4, landscape
from reportlab.lib.styles import ParagraphStyle
from reportlab.lib.utils import ImageReader
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.pdfgen import canvas
from reportlab.platypus import Paragraph, Table, TableStyle

from fixtures import definitions

DEFAULT_OUT = Path('/mnt/weknora-storage/weknora-dev/private/pdf-complex-20261022')
DEFAULT_CJK = DEFAULT_OUT / 'fonts/wqy-zenhei.ttc'
DEFAULT_MATH = Path('/mnt/weknora-storage/weknora-dev/cache/excel-ui/sysroot/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf')


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def dump(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def paragraph(text, style):
    primary=pdfmetrics.getFont('CJK').face.charToGlyph
    fallback=pdfmetrics.getFont('Math').face.charToGlyph
    encoded=[]
    for char in text:
        if char.isspace() or ord(char) in primary:
            encoded.append(escape(char))
        elif ord(char) in fallback:
            encoded.append('<font name="Math">'+escape(char)+'</font>')
        else:
            raise ValueError(f'Authored cell has no renderable glyph U+{ord(char):04X}')
    text = ''.join(encoded)
    text = re.sub(r'\^\{([^}]+)\}', r'<super>\1</super>', text)
    return Paragraph(text, style)


def draw_table(c, spec, *, page_size, page_number, top=None, left=40):
    width, height = page_size
    rows = spec['rows']
    if not rows or len({len(r) for r in rows}) != 1:
        raise ValueError('Authored tables must be rectangular')
    cols = len(rows[0])
    widths = spec.get('widths') or [(width - 80) / cols] * cols
    if len(widths) != cols or sum(widths) > width - left:
        raise ValueError('Authored column dimensions do not fit the page')
    style = ParagraphStyle('cell', fontName='CJK', fontSize=spec['font_size'], leading=spec['font_size'] * 1.3)
    t = Table([[paragraph(text, style) for text in row] for row in rows], colWidths=widths,
              rowHeights=[spec['row_height']] * len(rows))
    commands = [('GRID',(0,0),(-1,-1),.7,'#333333'),('VALIGN',(0,0),(-1,-1),'MIDDLE'),
                ('LEFTPADDING',(0,0),(-1,-1),6),('RIGHTPADDING',(0,0),(-1,-1),5)]
    for header in spec['header_rows']:
        commands.append(('BACKGROUND',(0,header),(-1,header),'#e8edf3'))
    for span in spec['spans']:
        r, col = span['row'], span['column']
        commands.append(('SPAN',(col,r),(col + span['colspan'] - 1,r + span['rowspan'] - 1)))
    t.setStyle(TableStyle(commands))
    tw, th = t.wrap(0, 0)
    top = spec['top'] if top is None else top
    if top - th < 30:
        raise ValueError('Authored table exceeds bottom margin')
    c.setFont('CJK', 12)
    c.drawString(left, top + 16, spec['caption'])
    t.drawOn(c, left, top - th)
    result = {k: deepcopy(spec[k]) for k in ('id','caption','rows','header_rows','unit','spans','logical_group')}
    result.update(page=page_number,bbox=[left / width,(height - top) / height,(left + tw) / width,(height - top + th) / height],
                  row_count=len(rows),column_count=cols,authored_blank_rows=[r for r,row in enumerate(rows) if all(x == '' for x in row)])
    result['source_cells'] = []
    for ri, row in enumerate(rows):
        cells=[]
        for ci, text in enumerate(row):
            anchor=next((s for s in spec['spans'] if s['row']<=ri<s['row']+s['rowspan'] and s['column']<=ci<s['column']+s['colspan']),None)
            cell_left=left+sum(widths[:ci]);cell_top=top-ri*spec['row_height']
            cells.append(dict(page=page_number,row=ri,column=ci,text=text,
                              bbox=[cell_left/width,(height-cell_top)/height,(cell_left+widths[ci])/width,(height-cell_top+spec['row_height'])/height],
                              covered_by=[anchor['row'],anchor['column']] if anchor and (ri,ci)!=(anchor['row'],anchor['column']) else None))
        result['source_cells'].append(cells)
    return result


def draw_formula(c, formula, page_size, page):
    x, y = formula['x'], formula['y']
    c.setFont('CJK', 11)
    c.drawString(x - 30, y + 52, formula['id'])
    c.setFont('Math', 22)
    kind = formula['kind']
    if kind == 'fraction':
        c.drawCentredString(x + 40, y + 22, 'a + b'); c.line(x,y+12,x+80,y+12)
        c.drawCentredString(x + 40,y-12,'c − d')
        rect = (x-8,y-25,x+90,y+48)
    elif kind == 'root':
        c.setFont('Math',34);c.drawString(x,y,'√')
        c.setFont('Math',22);c.drawString(x+29,y+2,'x² + y²')
        c.line(x+29,y+31,x+135,y+31)
        rect = (x-5,y-12,x+145,y+44)
    elif kind == 'sum':
        c.setFont('Math',45);c.drawString(x,y-7,'∑')
        c.setFont('Math',12);c.drawString(x+15,y+39,'n');c.drawString(x+4,y-24,'i = 1')
        c.setFont('Math',22);c.drawString(x+50,y+3,'i =')
        c.setFont('Math',19);c.drawCentredString(x+140,y+22,'n(n + 1)')
        c.line(x+97,y+12,x+185,y+12);c.drawCentredString(x+140,y-13,'2')
        rect = (x-5,y-31,x+195,y+53)
    else:
        c.setFont('Math',22)
        for dx,dy,label in [(18,22,'1'),(67,22,'−2'),(18,-15,'3'),(67,-15,'4')]:
            c.drawString(x+dx,y+dy,label)
        # Vector brackets avoid relying on a specialized math font renderer.
        for xx,offset in ((x,6),(x+111,-6)):
            c.line(xx,y-25,xx,y+48);c.line(xx,y+48,xx+offset,y+48);c.line(xx,y-25,xx+offset,y-25)
        rect = (x-9,y-34,x+122,y+56)
    w,h=page_size
    return dict(id=formula['id'],latex=formula['latex'],page=page,
                bbox=[rect[0]/w,(h-rect[3])/h,rect[2]/w,(h-rect[1])/h], exact_expected_text=formula['latex'])


def generate(out, cjk_font, math_font):
    if (out/'golden.json').exists():
        raise FileExistsError('Existing authored benchmark evidence is immutable; use a new output directory')
    out.mkdir(parents=True,exist_ok=True)
    (out/'rendered').mkdir(exist_ok=True)
    (out/'aux').mkdir(exist_ok=True)
    if not cjk_font.is_file() or not math_font.is_file():
        raise ValueError('Provide existing CJK and mathematical TrueType fonts; no fonts are installed/downloaded')
    pdfmetrics.registerFont(TTFont('CJK',str(cjk_font),subfontIndex=0))
    pdfmetrics.registerFont(TTFont('Math',str(math_font)))
    specs=definitions()
    manifest=dict(schema_version='authored_pdf_benchmark_v1', provenance='Authored fixture data; independent of parser/OCR output',
                  seed=0,fonts=dict(cjk_file=cjk_font.name,cjk_sha256=sha(cjk_font),math_file=math_font.name,math_sha256=sha(math_font)),
                  fixtures=[])
    thumbnails=[]
    for spec in specs:
        path=out/(spec['id']+'.pdf')
        c=canvas.Canvas(str(path),pagesize=A4,invariant=1,pageCompression=1)
        c.setTitle(spec['title']);c.setAuthor('Synthetic benchmark; no business records')
        fixture={k:deepcopy(v) for k,v in spec.items() if k!='pages'}
        fixture.update(file=path.name,tables=[],formulas=[],page_count=len(spec['pages']))
        for p,page_spec in enumerate(spec['pages'],1):
            size=landscape(A4) if page_spec.get('landscape') else A4
            c.setPageSize(size)
            w,h=size
            heading=Paragraph(escape(spec['title']),ParagraphStyle('heading',fontName='CJK',fontSize=13,leading=17))
            _,heading_height=heading.wrap(w-80,60)
            heading.drawOn(c,40,h-30-heading_height)
            c.setFont('CJK',9);c.drawString(40,18,f'Synthetic fixture {spec["id"]} / page {p}')
            for t in page_spec.get('tables',[]):
                fixture['tables'].append(draw_table(c,t,page_size=size,page_number=p))
            for index,note in enumerate(page_spec.get('notes',[])):
                c.setFont('CJK',9);c.drawString(40,270-index*18,note)
            for formula in page_spec.get('formulas',[]):
                fixture['formulas'].append(draw_formula(c,formula,size,p))
            if page_spec.get('scan_table'):
                scan_spec=page_spec['scan_table']
                buffer=io.BytesIO();sc=canvas.Canvas(buffer,pagesize=(590,230),invariant=1)
                scan_gold=draw_table(sc,scan_spec,page_size=(590,230),page_number=p,top=180)
                sc.save();buffer.seek(0)
                scan_doc=pdfium.PdfDocument(buffer.getvalue())
                img=scan_doc[0].render(scale=140/72).to_pil().convert('RGB')
                img=img.rotate(.8,resample=Image.Resampling.BICUBIC,expand=True,fillcolor='white')
                img.save(out/'aux'/f'{spec["id"]}-scan.png')
                display_w,display_h=510,510*img.height/img.width
                left,bottom=40,330
                c.drawImage(ImageReader(img),left,bottom,width=display_w,height=display_h)
                # Rotation adds whitespace; reference bbox conservatively covers the
                # authored table region, not a claimed OCR-derived cell geometry.
                scan_gold.update(page=p,bbox=[left/w,(h-bottom-display_h)/h,(left+display_w)/w,(h-bottom)/h],
                                 raster=dict(dpi=140,rotation_degrees=.8,mode='embedded_scan'))
                for row in scan_gold['source_cells']:
                    for cell in row:
                        cell['bbox']=None
                        cell['geometry_precision']='table_only_after_scan_rotation'
                fixture['tables'].append(scan_gold)
            c.showPage()
        c.save()
        fixture['sha256']=sha(path)
        doc=pdfium.PdfDocument(path)
        if len(doc)!=fixture['page_count']:
            raise ValueError('Generated page count mismatch')
        fixture['page_text_layer_char_counts']=[]
        for p in range(len(doc)):
            pg=doc[p]
            fixture['page_text_layer_char_counts'].append(len(pg.get_textpage().get_text_range()))
            rendered=pg.render(scale=1.4).to_pil().convert('RGB')
            rendered.save(out/'rendered'/f'{spec["id"]}-p{p+1:02d}.png')
            rendered.thumbnail((310,405))
            card=Image.new('RGB',(330,440),'#e7e9ed')
            card.paste(rendered,((330-rendered.width)//2,22))
            ImageDraw.Draw(card).text((8,419),f'{spec["id"]} / {p+1}',fill='black')
            thumbnails.append(card)
        fixture['gold_sha256']=hashlib.sha256(json.dumps(fixture,ensure_ascii=False,sort_keys=True).encode()).hexdigest()
        manifest['fixtures'].append(fixture)
    total_pages=sum(f['page_count'] for f in manifest['fixtures'])
    if len(manifest['fixtures'])!=8 or total_pages!=12:
        raise ValueError('Benchmark fixture/page budget changed')
    manifest.update(fixture_count=8,page_count=total_pages,inference_run=False)
    dump(out/'golden.json',manifest)
    sheet=Image.new('RGB',(330*3,440*((len(thumbnails)+2)//3)), 'white')
    for i,thumb in enumerate(thumbnails):sheet.paste(thumb,((i%3)*330,(i//3)*440))
    sheet.save(out/'contact-sheet.jpg',quality=92)
    print(json.dumps(dict(out=str(out),fixture_count=8,page_count=total_pages,golden_sha256=sha(out/'golden.json'),
                          contact_sheet=str(out/'contact-sheet.jpg'),inference_run=False)))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,default=DEFAULT_OUT)
    parser.add_argument('--cjk-font',type=Path,default=DEFAULT_CJK)
    parser.add_argument('--math-font',type=Path,default=DEFAULT_MATH)
    args=parser.parse_args()
    generate(args.output,args.cjk_font,args.math_font)

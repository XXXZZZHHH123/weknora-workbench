import hashlib
from pathlib import Path
import sys

import fitz
import pytest

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from remote_pdf.watermarks import recognition_copy


def source(path, *, watermark=True, shared_font=False):
    with fitz.open() as doc:
        for i in range(3):
            page=doc.new_page(width=600,height=800)
            page.insert_text((80,120),'Value -11.25',fontname='cour' if shared_font else 'helv')
            page.insert_font(fontname='cour')
            resources=doc.xref_get_key(page.xref,'Resources')[1].split()[0]
            doc.xref_set_key(int(resources),'ExtGState','<< /WM << /ca 0.4 /CA 0.4 >> >>')
            if watermark:
                xref=doc.get_new_xref();doc.update_object(xref,'<<>>')
                doc.update_stream(xref,b'q /WM gs BT /cour 1 Tf 28 28 -28 28 240 400 Tm (DRAFT) Tj ET Q')
                doc.xref_set_key(page.xref,'Contents','['+' '.join(f'{x} 0 R' for x in page.get_contents()+[xref])+']')
        doc.save(path)


def test_only_independent_repeated_translucent_text_is_removed(tmp_path):
    original=tmp_path/'source.pdf';target=tmp_path/'clean.pdf';source(original)
    before=original.read_bytes()
    chosen,receipt=recognition_copy(original,target)
    assert chosen==target and receipt['status']=='cleaned' and receipt['removed_objects']==3,receipt
    assert original.read_bytes()==before and receipt['source_sha256']==hashlib.sha256(before).hexdigest()
    with fitz.open(target) as doc:
        assert len(doc)==3
        for page in doc:
            assert 'Value -11.25' in page.get_text() and 'DRAFT' not in page.get_text()


@pytest.mark.parametrize('shared_font',[True,False])
def test_no_unsafe_removal_or_rewriting_when_not_separable(tmp_path,shared_font):
    original=tmp_path/'source.pdf';target=tmp_path/'clean.pdf';source(original,watermark=shared_font,shared_font=shared_font)
    before=original.read_bytes();chosen,receipt=recognition_copy(original,target)
    assert chosen==original and receipt['status']=='unchanged'
    assert not target.exists() and original.read_bytes()==before


def test_original_path_cannot_be_used_for_output(tmp_path):
    original=tmp_path/'source.pdf';source(original)
    with pytest.raises(ValueError):recognition_copy(original,original)

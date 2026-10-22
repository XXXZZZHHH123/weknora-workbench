"""Build the embedded static face from the repository's OFL WOFF2 subsets.
Tooling only: fonttools[woff]==4.61.1. Not used by the application at runtime.
Run from WeKnora/internal/chart/fonts. No network access is performed.
"""
from pathlib import Path
from tempfile import TemporaryDirectory
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools.merge import Merger
import hashlib

here = Path(__file__).resolve().parent
source = here.parents[2] / 'frontend/public/fonts/noto-sans-sc'
inputs = sorted(source.glob('*.woff2'))
assert inputs
with TemporaryDirectory() as temp:
    paths = []
    codepoints = set()
    for i, path in enumerate(inputs):
        font = TTFont(path, recalcTimestamp=False)
        codepoints.update(font.getBestCmap())
        if 'fvar' in font:
            font = instantiateVariableFont(font, {'wght': 400}, inplace=True)
        # Charts draw individual Unicode glyphs; layout tables are not consumed
        # by x/image/font. Removing them also avoids cross-subset layout merges.
        for tag in ('GSUB', 'GPOS', 'GDEF', 'BASE', 'STAT', 'vhea', 'vmtx'):
            if tag in font:
                del font[tag]
        font.flavor = None
        out = Path(temp) / f'{i}.ttf'
        font.save(out)
        paths.append(str(out))
    font = Merger().merge(paths)
    # The OFL reserves "Source". Give this static merged derivative its own
    # family and PostScript names while keeping copyright/license records.
    for name_id, text in {1:'Workbench Chart CJK', 2:'Regular', 3:'WorkbenchChartCJK-Regular-1', 4:'Workbench Chart CJK Regular', 6:'WorkbenchChartCJK-Regular', 16:'Workbench Chart CJK', 17:'Regular'}.items():
        font['name'].removeNames(nameID=name_id)
        font['name'].setName(text,name_id,3,1,0x409)
    font.recalcTimestamp = False
    font['head'].created = font['head'].modified = 2082844800
    target = here / 'WorkbenchChartCJK-Regular.ttf'
    font.save(target)
    assert set(font.getBestCmap()) == codepoints
    print(f'{len(inputs)} repository subsets; {len(codepoints)} codepoints; {target.stat().st_size} bytes')
    print('sha256', hashlib.sha256(target.read_bytes()).hexdigest())

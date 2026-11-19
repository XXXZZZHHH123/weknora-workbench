/* Intercepted workbook review requests. No service or model is contacted. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const source = path.join(__dirname, '..', 'src', 'structured_data');
const importId = 'c'.repeat(24);
const importPath = '/api/imports/' + importId;
const cleanupPath = importPath + '/header-cleanup';
const sheet = {
  name: '合成表甲', rows: 5, columns: 2, status: 'confirmed',
  layout: 'single_header', fingerprint: 'fixture-header-cleanup',
  mapping: {sheet:'合成表甲',fingerprint:'fixture-header-cleanup',blocks:[{
    date_column:'A',date_mode:'date',header_rows:[1,2,3],data_start_row:4,data_end_row:5,
    columns:[{column:'B',metric:'合成指标 / 周 / 原说明',unit:'件',header_cells:['B1','B2','B3']}],
  }]},
};
const note = '原表说明'.repeat(30) + '\n<img src=x onerror="throw 1">';
const change = index => ({
  column_index:index+2, old_name:`原名称 ${index+1}`, new_name:`新名称 ${index+1}`,
  source_codes:[{cell:'B3',text:'S5329490'}], unit:[{cell:'B4',text:'百万包'}],
  frequency:[{cell:'B2',text:'周'}], header_notes:[{cell:'B5',text:note}],
  unit_conflict:index===1,
});
const mixedDiagnostic={block_index:1,message:'此区块另有一格需要核对。'};
const reviewDiagnostic={block_index:0,message:'B4-E4 建议作为表头说明格，请核对。'};
const suggestions = [
  {sheet_name:sheet.name,status:'rename',changes:Array.from({length:6},(_,index)=>change(index)),diagnostics:[mixedDiagnostic]},
  {sheet_name:'合成表乙',status:'rename',changes:[change(0)]},
  {sheet_name:'合成表丙',status:'rename',changes:[change(0)]},
  {sheet_name:'合成表丁',status:'unchanged',changes:[]},
  {sheet_name:'合成表戊',status:'failed',error:'模型暂时无法提供建议。'},
  {sheet_name:'合成表己',status:'needs_review',changes:[],diagnostics:[reviewDiagnostic]},
];
const notice = '6 个字段改名后 ID 已变化；请到各研究台「指标对应确认」确认重映射，并改写引用。';

(async()=>{
  const browser = await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
  try {
    const page = await browser.newPage({viewport:{width:1450,height:950}});
    const posts = [], errors = [];
    let count=0, jobId='', release=false, cancelled=false;
    page.on('pageerror',error=>errors.push(error.message));
    await page.route('**/*',async route=>{
      const request=route.request(), url=new URL(request.url());
      const payload=request.postDataJSON();
      if(request.method()==='POST')posts.push({path:url.pathname,payload});
      let data={}, contentType='application/json';
      if(url.pathname==='/'||['/sheet-review.js','/sheet-review.css','/workspace.css','/pdf-query.js'].includes(url.pathname)){
        data=fs.readFileSync(path.join(source,url.pathname==='/'?'index.html':url.pathname.slice(1)),'utf8');
        contentType=url.pathname==='/'?'text/html':url.pathname.endsWith('.js')?'application/javascript':'text/css';
      }else if(url.pathname==='/api/ui-config')data={title:'合成核对',workspace_label:'Fixture'};
      else if(url.pathname==='/api/datasets')data={datasets:[]};
      else if(url.pathname==='/api/imports')data={imports:[{import_id:importId,source_file:'fixture.xlsx'}]};
      else if(url.pathname===importPath)data={import_id:importId,source_file:'fixture.xlsx',header_cleanup_count:count,sheets:[sheet]};
      else if(url.pathname===importPath+'/sheet')data=sheet;
      else if(url.pathname===importPath+'/sheet-view')data={
        sheet_name:sheet.name,total_rows:5,total_cols:2,
        range:{start_row:1,end_row:5,start_col:1,end_col:2,row_count:5,col_count:2},
        cells:[{row:1,column:1,cell:'A1',value:'日期',display:'日期'}],
        styles:{},merges:[],column_widths:{},row_heights:{},warnings:[],
      };
      else if(url.pathname===cleanupPath){
        jobId=payload.request_id; release=false; cancelled=false;
        assert.match(jobId,/^[a-f0-9]{32}$/);
        data={id:jobId,status:'processing'};
      }else if(jobId&&url.pathname==='/api/jobs/'+jobId){
        if(cancelled)data={status:'cancelled',error:'任务已取消。',result:{sheets:[suggestions[0]]}};
        else if(release)data={status:'completed',result:{sheets:suggestions}};
        else data={status:'processing',stage:'正在整理第 2/6 张',progress:{completed:2,total:6}};
      }else if(jobId&&url.pathname==='/api/jobs/'+jobId+'/cancel'){
        cancelled=true;
        data={id:jobId,status:'processing',cancel_requested:true};
      }else if(url.pathname===cleanupPath+'/apply'){
        assert.deepEqual(payload,{job_id:jobId,sheet_names:[sheet.name,'合成表丙']});
        count=5;
        data={sheets:[
          {sheet_name:sheet.name,status:'applied',renamed_fields:6},
          {sheet_name:'合成表丙',status:'failed',error:'工作表已变化，请重新整理'},
        ],renamed_fields:6,notice};
      }
      await route.fulfill({contentType,body:typeof data==='string'?data:JSON.stringify(data)});
    });
    await page.goto('http://sheet-review.fixture/');
    await page.locator('.sheet-grid td').first().waitFor();
    const button=page.locator('#headerCleanup');
    const dialog=page.locator('#headerCleanupDialog');
    const apply=page.locator('#headerCleanupApply');
    const progress=page.locator('#headerCleanupProgress');
    assert.equal(await button.isHidden(),true);
    assert.deepEqual(posts,[]);
    count=6;
    await page.locator('#reloadImports').click();
    await button.waitFor();
    assert.equal(await button.innerText(),'整理已导入表的表头说明（6 张）');

    await button.click();
    await progress.getByText(/第 2\/6 张 · 33%（2\/6）/).waitFor();
    assert.equal(await progress.locator('.ai-job-spinner').isVisible(),true);
    await page.locator('#headerCleanupClose').click();
    assert.equal(await dialog.isHidden(),true);
    release=true;
    await page.waitForResponse(response=>response.url().endsWith('/api/jobs/'+jobId));
    await button.click();
    const proposals=dialog.locator('[data-header-cleanup-sheet]');
    await proposals.first().waitFor();
    assert.deepEqual(posts.map(post=>post.path),[cleanupPath],'Reopening retrieves the same job without applying any sheet');
    assert.equal(await proposals.count(),4);
    for(const proposal of (await proposals.all()).slice(0,3))assert.equal(await proposal.isChecked(),true);
    const needsReview=dialog.locator('.header-cleanup-sheet').filter({hasText:'合成表己'});
    assert.match(await needsReview.innerText(),/合成表己 · 需核对/);
    assert.match(await needsReview.innerText(),/区块 1：B4-E4 建议作为表头说明格，请核对。/);
    assert.equal(await needsReview.getByRole('checkbox').isChecked(),false);
    assert.equal(await needsReview.getByRole('checkbox').isDisabled(),true);
    const first=dialog.locator('.header-cleanup-sheet').first();
    assert.match(await first.innerText(),/合成表甲 · 6 个指标会改名/);
    assert.match(await first.innerText(),/区块 2：此区块另有一格需要核对。/);
    const changes=first.locator('.header-cleanup-changes');
    await changes.locator(':scope > summary').click();
    assert.equal(await changes.locator(':scope > ul > li').count(),5);
    assert.match(await first.innerText(),/原名称 1 → 新名称 1/);
    const fields=changes.locator('.header-cleanup-fields').first();
    assert.deepEqual(await fields.locator(':scope > li').allInnerTexts(),[
      '代码：S5329490','单位：百万包','频率：周','说明：'+'原表说明'.repeat(18)+'…',
    ]);
    const noteDetails=fields.locator('details');
    const noteValue=noteDetails.locator(':scope > div');
    assert.equal(await noteValue.isVisible(),false);
    await noteDetails.locator('summary').click();
    assert.equal(await noteValue.isVisible(),true);
    assert.equal(await noteValue.textContent(),note);
    assert.equal(await changes.getByText('单位待核对',{exact:true}).first().isVisible(),true);
    const remaining=changes.locator(':scope > details');
    await remaining.getByText('其余 1 个',{exact:true}).click();
    assert.equal(await remaining.getByText('原名称 6 → 新名称 6',{exact:true}).isVisible(),true);
    assert.equal(await dialog.locator('img').count(),0);
    assert.equal(await dialog.getByText('无需改动（1 张）',{exact:true}).isVisible(),true);
    assert.equal(await dialog.getByText('合成表丁',{exact:true}).isHidden(),true);
    assert.equal(await dialog.getByText('合成表戊：模型暂时无法提供建议。',{exact:true}).isVisible(),true);

    await proposals.nth(1).uncheck();
    await page.locator('#headerCleanupClose').click();
    await button.click();
    assert.equal(await proposals.nth(1).isChecked(),false,'An unchecked sheet stays unchecked when the results are reopened');
    await apply.click();
    const applied=dialog.locator('[data-sheet-confirm="success"]');
    await applied.getByText('合成表甲：已应用（6 个指标改名）',{exact:true}).waitFor();
    assert.match(await applied.innerText(),/区块 2：此区块另有一格需要核对。/);
    assert.equal(await dialog.getByText('合成表丙：工作表已变化，请重新整理',{exact:true}).isVisible(),true);
    assert.equal(await page.locator('#headerCleanupNotice').innerText(),notice);
    assert.equal(await button.innerText(),'整理已导入表的表头说明（5 张）');
    assert.equal(await proposals.count(),2);
    assert.equal(await apply.isDisabled(),true);
    assert.deepEqual(posts.map(post=>post.path),[cleanupPath,cleanupPath+'/apply']);

    await page.locator('#headerCleanupRetry').click();
    await progress.getByText(/第 2\/6 张/).waitFor();
    await page.locator('#headerCleanupCancel').click();
    await page.locator('#headerCleanupNotice').getByText('任务已取消。').waitFor();
    assert.equal(await proposals.count(),1,'Completed suggestions stay visible after cancellation');
    assert.equal(await proposals.first().isChecked(),true);
    assert.equal(posts.at(-1).path,'/api/jobs/'+jobId+'/cancel');
    await page.evaluate(()=>window.sheetReviewAuthChanged());
    assert.equal(await dialog.isHidden(),true);
    assert.equal(await proposals.count(),0,'Changing accounts clears the previous scope’s suggestions');
    assert.deepEqual(errors,[]);
    console.log('PASS: header cleanup job controls, rename and review states, separated fields, expandable notes, per-sheet application and remap notice.');
  } finally {
    await browser.close();
  }
})().catch(error=>{console.error(error);process.exitCode=1;});

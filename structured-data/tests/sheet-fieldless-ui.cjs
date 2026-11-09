/* Sheets in which no field was recognised are explained on the import page: offline browser fixture only. */
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {chromium}=require('playwright');
const source=path.join(__dirname,'..','src','structured_data');
const block={date_mode:'date',date_column:'A',header_rows:[1],data_start_row:2,data_end_row:3,columns:[{column:'B',metric:'合成数值',unit:'单位',header_cells:['B1']}]};
const sheet=(name,status,blocks,extra={})=>({name,rows:3,columns:3,status,layout:'single_header',fingerprint:name,reasons:[],mapping:{sheet:name,fingerprint:name,blocks},published_in_dataset:false,published_dataset_id:null,published_mapping:null,published_fingerprint:null,...extra});
const run=async(browser,sheets)=>{
  const page=await browser.newPage({viewport:{width:1400,height:900}}),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',async route=>{
   const u=new URL(route.request().url());let data={},type='application/json';
   if(u.pathname==='/'){data=fs.readFileSync(path.join(source,'index.html'),'utf8');type='text/html';}
   else if(['/pdf-query.js','/sheet-review.js','/sheet-review.css','/workspace.css'].includes(u.pathname)){data=fs.readFileSync(path.join(source,u.pathname.slice(1)),'utf8');type=u.pathname.endsWith('.js')?'application/javascript':'text/css';}
   else if(u.pathname==='/api/ui-config')data={title:'表格分析',workspace_label:'合成验收',weknora_url:'http://fixture.test'};
   else if(u.pathname==='/api/datasets')data={datasets:[]};
   else if(u.pathname==='/api/imports')data={imports:[{import_id:'b'.repeat(24),source_file:'synthetic.xlsx'}]};
   else if(u.pathname.endsWith('/sheet'))data=sheets.find(s=>s.name===u.searchParams.get('name'))||sheets[0];
   else if(u.pathname.endsWith('/sheet-view'))data={sheet_name:u.searchParams.get('name'),total_rows:3,total_cols:3,range:{start_row:1,end_row:3,start_col:1,end_col:3,row_count:3,col_count:3},cells:[{row:1,column:1,cell:'A1',value:'日期',display:'日期'}],styles:{},merges:[],column_widths:{},row_heights:{},warnings:[]};
   else if(u.pathname.startsWith('/api/imports/'))data={import_id:'b'.repeat(24),source_file:'synthetic.xlsx',dataset_id:'fixture',sheets};
   await route.fulfill({contentType:type,body:typeof data==='string'?data:JSON.stringify(data)});
  });
  await page.goto('http://fixture.test/');await page.locator('#importSummary').filter({hasText:'张工作表'}).waitFor();
  const hint=page.locator('#importFieldless');
  const result={visible:await hint.isVisible(),text:await hint.innerText().catch(()=>''),errors};
  await page.close();return result;
};
(async()=>{
 const browser=await chromium.launch({headless:true,...(process.env.SHEET_REVIEW_BROWSER?{executablePath:process.env.SHEET_REVIEW_BROWSER}:{})});
 try{
  const mixed=await run(browser,[sheet('合成数据表','needs_review',[block]),sheet('合成说明页','needs_review',[]),sheet('合成跳过页','needs_review',[],{automatic_skip:true})]);
  assert.equal(mixed.visible,true);
  assert.match(mixed.text,/^有 1 张工作表没有识别出字段（合成说明页），可能是说明页或没有数据的表/);
  assert.match(mixed.text,/请打开该表，在原表上框选数据区域后确认/);
  assert.deepEqual(mixed.errors,[]);
  const clean=await run(browser,[sheet('合成数据表','needs_review',[block])]);
  assert.equal(clean.visible,false,'每张待核对表都有字段时不提示');
  console.log('sheet-fieldless-ui: passed');
 }finally{await browser.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});

const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const root=path.join(__dirname,'../src/tin/web/static');
const canonical=JSON.parse(fs.readFileSync(path.join(root,'chart-palette.json'),'utf8'));
const fixturePalette={color:(index=0,custom)=>typeof custom==='string'&&custom.trim()?custom:canonical[((Math.trunc(index)||0)%canonical.length+canonical.length)%canonical.length]};
function extract(file,name,indent=''){
  const source=fs.readFileSync(path.join(root,file),'utf8');let start=source.indexOf('function '+name+'(');
  assert.ok(start>=0,'The actual renderer must exist: '+name);
  if(source.slice(start-6,start)==='async ')start-=6;
  const end=source.indexOf('\n'+indent+'}',start);assert.ok(end>=0);
  return source.slice(start,end+indent.length+2);
}
function diagram(palette,custom){
  const context={window:{ChartPalette:palette},dgFmt:String,dgEsc:String,dgAxisDate:()=>String};vm.createContext(context);
  vm.runInContext(extract('diagram.js','dgChart'),context);
  return context.dgChart([{as_of:'2030-01-01',value:10},{as_of:'2030-01-02',value:2}],'kg',420,180,custom);
}
(async()=>{
  // Before the change this real renderer used direction red/green for its line.
  assert.match(diagram(fixturePalette),/stroke="#4472C4"[^>]*stroke-width="1\.8"/,'A declining single-series diagram uses the shared blue, while change badges retain their semantics');
  const palette=require(path.join(root,'chart-palette.js'));
  assert.deepEqual(palette.colors,canonical);assert.equal(new Set(palette.colors).size,canonical.length);
  assert.equal(palette.color(0),'#4472C4');assert.equal(palette.color(1),'#ED7D31');assert.equal(palette.color(10),'#4472C4');
  assert.equal(palette.color(0,'#123456'),'#123456','Explicit user colors are preserved');
  assert.match(diagram(palette,'#123456'),/stroke="#123456"/);
  const output={innerHTML:''},context={window:{ChartPalette:palette},get:()=>output,escape:String};vm.createContext(context);
  vm.runInContext(extract('indicators.js','renderComparison','  '),context);
  context.renderComparison({status:'ok',dates:['2030-01-01','2030-01-02'],series:canonical.slice(0,4).map((_,i)=>({name:'Synthetic '+i,unit:'kg',values:[100+i,105+i],raw_values:[10,12]}))});
  const lineColors=[...output.innerHTML.matchAll(/<polyline[^>]*stroke="([^"]+)"/g)].map(match=>match[1]);
  assert.deepEqual(lineColors,canonical.slice(0,4),'Indicator comparisons use blue, orange, green and purple in the same order');
  for(const color of lineColors)assert.ok(output.innerHTML.includes('border-color:'+color),'Legend color matches its actual series');
  const compare={innerHTML:''},comparison={window:{ChartPalette:palette},document:{getElementById:()=>compare},dg:{compare:[{series_id:'a',name:'Series A'},{series_id:'b',name:'Series B'}]},dgCompareTabs:()=>'',dgFmt:String,dgEsc:String,dgAxisDate:()=>String,dgDir:()=>({cls:'',mark:''}),researchFetch:async()=>({ok:true,json:async()=>({unit:'kg',history:[{as_of:'2030-01-01',value:10},{as_of:'2030-01-02',value:12}]})})};
  vm.createContext(comparison);vm.runInContext(extract('diagram.js','dgRenderCompare'),comparison);await comparison.dgRenderCompare();
  assert.deepEqual([...compare.innerHTML.matchAll(/<path[^>]*stroke="(#[^"]+)"/g)].map(match=>match[1]),canonical.slice(0,2),'Two diagram comparison curves use the same ordered contrasting colors');
  for(const color of canonical.slice(0,2))assert.ok(compare.innerHTML.includes('background:'+color),'Diagram legend and actual curves agree');
  vm.runInContext(extract('diagram.js','dgDir'),comparison);
  assert.equal(comparison.dgDir(2).color,'var(--up)');assert.equal(comparison.dgDir(-2).color,'var(--down)','Signed change badges retain red/green semantics');
  const svg={innerHTML:'',dataset:{}},app={window:{ChartPalette:palette},document:{getElementById:()=>svg},seriesChartState:{data:{decimals:2}},chartRangePoints:()=>[{date:'2030-01-01',time:0,value:2},{date:'2030-01-02',time:86400000,value:8}],chartNumber:String,chartDate:String,moveSeriesChartCrosshair(){},hideSeriesChartCrosshair(){},updateSeriesChartReadout(){}};
  vm.createContext(app);vm.runInContext(extract('app.js','renderSeriesChart'),app);app.renderSeriesChart();
  assert.match(svg.innerHTML,/stroke="#4472C4"[^>]*stroke-width="2\.4"/,'The shared history modal uses the same primary series color');
  assert.match(svg.innerHTML,/id="series-chart-cross-point"[^>]*stroke="#4472C4"/,'Hover markers match their series');
  class Element{constructor(){this.children=[];this.dataset={};this.innerHTML='';this.textContent='';}append(...children){this.children.push(...children);}replaceChildren(...children){this.children=children;}setAttribute(){}addEventListener(){}}
  const host=new Element();host.dataset={seriesCount:'1',url:'/fixture',day:'2030-01-02',period:'daily'};
  const report={window:{ChartPalette:palette},location:{origin:'http://fixture'},URL,Intl,document:{addEventListener:(_,callback)=>callback(),getElementById:()=>host,createElement:()=>new Element(),createElementNS:()=>new Element(),createTextNode:value=>({textContent:value})},fetch:async()=>({ok:true,json:async()=>({points_per_series:90,series:[{name:'Synthetic report figure',unit:'kg',path:'M0 0L1 1',min:2,max:8,points:[{x:52,y:128,date:'2030-01-01',value:2},{x:484,y:16,date:'2030-01-02',value:8}]}]})})};
  vm.runInNewContext(fs.readFileSync(path.join(root,'report-figures.js'),'utf8'),report);await new Promise(resolve=>setImmediate(resolve));
  const figure=host.children[0]?.children.find(node=>node.innerHTML);
  assert.match(figure?.innerHTML||'',/<polyline[^>]*stroke="#4472C4"/,'Report series use the shared primary blue');
  assert.match(figure.innerHTML,/<circle[^>]*fill="#4472C4"/,'Report point markers match their data line');
  const styleValues=new Map(),browser={ChartPaletteColors:canonical,document:{documentElement:{style:{setProperty:(name,value)=>styleValues.set(name,value)}}}};
  vm.runInNewContext(fs.readFileSync(path.join(root,'chart-palette.js'),'utf8'),{window:browser});
  canonical.forEach((color,index)=>assert.equal(styleValues.get('--chart-series-'+(index+1)),color,'Static SVG/CSS variables share the canonical palette'));
  assert.equal(browser.ChartPalette.color(1),'#ED7D31');
  console.log('Cross-chart palette behavior passed: shared series order, single line, legend, hover, custom color and static SVG variables');
})().catch(error=>{console.error(error);process.exitCode=1;});

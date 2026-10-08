// Exercise our local management app in a fresh browser profile; never log credentials.
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('/Users/mac/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

(async () => {
  const root = path.resolve(__dirname, '..');
  const account = JSON.parse(fs.readFileSync(path.join(root, '../.private/accounts.json'), 'utf8'))[0];
  const browser = await chromium.launch({headless:true, executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
  const page = await browser.newPage({viewport:{width:1400,height:1050}});
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  const checks = [];
  try {
    await page.goto('http://127.0.0.1:18082');
    await page.getByLabel('邮箱', {exact:true}).fill(account.email);
    await page.getByLabel('密码', {exact:true}).fill(account.password);
    await page.getByRole('button',{name:'登录',exact:true}).click();
    await page.locator('#workspace').waitFor({state:'visible',timeout:15000});
    checks.push('同一 WeKnora 账号登录管理页');
    await page.locator('#stats').getByText('241,717',{exact:true}).waitFor();
    checks.push('显示真实数据集和有效记录数');
    await page.getByLabel('搜索指标、合约或工作表').fill('CF.CZC 收盘价');
    await page.getByRole('button',{name:'搜索',exact:true}).click();
    await page.locator('#seriesRows tr').filter({hasText:'futures.close.CF.CZC'}).getByRole('button',{name:'选择'}).click();
    await page.getByRole('button',{name:'查询',exact:true}).click();
    await page.locator('#answer').getByText('17110',{exact:true}).waitFor();
    await page.locator('#answer').getByText('B4705（日期 A4705）',{exact:true}).waitFor();
    checks.push('点击指标并查询 2023-09-22，17110 与 B4705/A4705 显示正确');
    await page.screenshot({path:path.join(root,'results/工作台查询截图.png'),fullPage:true});
    // Same-file upload verifies the entire browser/import/job flow without another dataset.
    const before = await page.locator('#dataset option').count();
    await page.locator('#file').setInputFiles('/Users/mac/Documents/中哲物产/棉花测试集/棉花/棉花数据库(2).xlsx');
    await page.getByRole('button',{name:'解析并导入',exact:true}).click();
    await page.locator('#jobState').getByText(/导入完成/).waitFor({timeout:60000});
    if (!(await page.locator('#jobState').innerText()).includes('复用原数据集')) throw Error('Same file did not reuse existing dataset');
    if (await page.locator('#dataset option').count() !== before) throw Error('Duplicate dataset was created');
    checks.push('网页上传 53 MB 源文件成功，导入完成并复用同一版本');
    if (errors.length) throw Error('Browser JS errors: '+errors.join(';'));
    fs.writeFileSync(path.join(root,'results/browser-test.json'), JSON.stringify({status:'passed',checks,page_errors:errors},null,2));
    console.log(JSON.stringify({status:'passed',checks},null,2));
  } catch(e) {
    await page.screenshot({path:path.join(root,'results/工作台错误截图.png'),fullPage:true});
    fs.writeFileSync(path.join(root,'results/browser-test.json'),JSON.stringify({status:'failed',checks,error:e.message,page_errors:errors},null,2));
    throw e;
  } finally {await browser.close();}
})().catch(e=>{console.error(e.message);process.exitCode=1;});

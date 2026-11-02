const assert = require('node:assert/strict');
const {setup, flush} = require('./judgment-manager-ui.cjs');
const latest = {id:'synthetic-analysis-job-0001',purpose:'judgments',workbench_id:'synthetic',state:'succeeded',result_available:true,dismissed:true};
(async () => {
  {
    const env=setup({latestJob:latest});await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-restore-result'].hidden,false,'An ignored successful result remains explicitly recoverable');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/result')).length,0,'Discovery never loads a preview into the editor');
    assert.equal(env.nodes['logic-input'].value,'');
    await env.nodes['logic-restore-result'].fire('click');await flush();
    assert.equal(env.nodes['logic-input'].value,'Recovered exact source');
    assert.equal(env.requests.filter(row=>row.method==='POST').length,0,'Recovery never invokes a model or saves a judgment');
    env.context.window.JudgmentManager.close();
  }
  for(const job of [null,{...latest,purpose:'diagram'},{...latest,workbench_id:'another'},{...latest,state:'failed'},{...latest,result_available:false}]){
    const env=setup({latestJob:job});await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-restore-result'].hidden,true,'Only this workbench and task kind may be offered');
    env.context.window.JudgmentManager.close();
  }
  {
    const env=setup({latestJob:latest,confirmDelete:false});await env.context.window.JudgmentManager.open();
    env.nodes['logic-input'].value='Unfinished user research';await env.nodes['logic-restore-result'].fire('click');
    assert.equal(env.nodes['logic-input'].value,'Unfinished user research');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/result')).length,0);
    assert.equal(env.confirmations.length,1);assert.match(env.confirmations[0],/替换.*原文.*草稿/);
    env.context.window.JudgmentManager.close();
  }
  {
    const env=setup({latestJob:latest,jobResultDeferred:true});await env.context.window.JudgmentManager.open();
    const restoring=env.nodes['logic-restore-result'].fire('click');
    while(!env.jobResultReplies.length)await flush();
    env.nodes['logic-input'].value='Edited while recovery was loading';await env.nodes['logic-input'].fire('input');
    env.jobResultReplies[0]();await restoring;
    assert.equal(env.nodes['logic-input'].value,'Edited while recovery was loading','A late result cannot overwrite a changed input');
    env.context.window.JudgmentManager.close();
  }
  {
    const env=setup({latestJob:latest});await env.context.window.JudgmentManager.open();await env.nodes['logic-restore-result'].fire('click');
    env.nodes['logic-input'].value='Kept editing the recovered source';env.context.window.JudgmentManager.close();
    const prior=env.requests.filter(row=>row.url.endsWith('/result')).length;
    await env.context.window.JudgmentManager.open();
    assert.equal(env.nodes['logic-input'].value,'Kept editing the recovered source','Reopening only discovers metadata and preserves the existing editor');
    assert.equal(env.requests.filter(row=>row.url.endsWith('/result')).length,prior);
    env.context.window.JudgmentManager.close();
  }
  {
    const env=setup({latestJob:latest,latestDeferred:true});const opening=env.context.window.JudgmentManager.open();
    while(!env.latestReplies.length)await flush();env.context.window.JudgmentManager.close();env.latestReplies[0]();await opening;
    assert.equal(env.nodes['logic-restore-result'].hidden,true,'A closed manager cannot display a late discovery result');
  }
  console.log('Latest judgment result discovery, scope, explicit recovery and edit protection passed');
})().catch(error=>{console.error(error);process.exitCode=1;});

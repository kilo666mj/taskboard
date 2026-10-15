const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(`${__dirname}/app.js`,'utf8');

function harness(overrides={}){
  const context={Date,state:{role:'member',principal:'person:reviewer',tasks:[],controls:new Map},...overrides};
  vm.createContext(context);
  for(const name of ['canWriteTasks','canOperateTasks','canRecover','recoveryRun','availableControl','recoveryLabel','requeueTask','releaseTask','replaceTask']){
    const start=source.search(new RegExp(`^(?:async )?function ${name}\\(`,'m'));
    assert.notEqual(start,-1,`missing ${name}`);
    const rest=source.slice(start);
    const next=rest.slice(1).search(/\n(?:async )?function /);
    assert.notEqual(next,-1,`missing function boundary after ${name}`);
    vm.runInContext(rest.slice(0,next+1),context);
  }
  return context;
}

function task(status='waiting'){
  return {id:'task-1',version:7,status,visibility:'agent',owner:'agent:former',created_by:'agent:producer',runs:[{id:'run-1',status,ended_at:'2026-01-01T00:00:00Z',lease_expires_at:'2026-01-01T00:00:00Z'}]};
}

test('ended waiting and blocked runs expose direct recovery without the old controller',()=>{
  const h=harness();
  for(const status of ['waiting','blocked','stale']){
    const t=task(status);
    assert.equal(h.recoveryRun(t).id,'run-1');
    assert.equal(h.availableControl(t).review,true);
    assert.equal(h.recoveryLabel(t),'Release to queue');
  }
});

test('any live run blocks release, including a newer run after an ended one',()=>{
  const h=harness();
  for(const status of ['waiting','blocked','stale']){
    const t=task(status);
    t.runs.push({id:'new-run',status:'active',lease_expires_at:new Date(Date.now()+60000).toISOString()});
    assert.equal(h.recoveryRun(t),null);
    assert.equal(h.availableControl(t),null);
  }
  for(const status of ['active','queued','done','cancelled'])assert.equal(h.recoveryRun(task(status)),null);
  const legacy=task();
  delete legacy.runs[0].ended_at;
  assert.equal(h.recoveryRun(legacy).id,'run-1');
});

test('release uses the human review endpoint and preserves the version guard',async()=>{
  const t=task();
  const calls=[];
  let rendered=0;
  const h=harness({appDialog:async options=>{assert.match(options.message,/Another agent may start/);return 'Reviewed: prior worker exited';},api:async(path,options)=>{calls.push({path,body:JSON.parse(options.body)});return {task:{...t,status:'queued',owner:''},control:{id:'control-1'}};},render:()=>rendered++});
  h.state.tasks=[t];
  await h.releaseTask(t,{disabled:false});
  assert.deepEqual(calls,[{path:'/api/v1/tasks/task-1/review-requeue',body:{expected_version:7,target_run_id:'run-1',review_note:'Reviewed: prior worker exited'}}]);
  assert.equal(h.state.tasks[0].owner,'');
  assert.equal(h.state.tasks[0].status,'queued');
  assert.equal(rendered,1);
});

test('cancel, blank review, and unauthorized viewers do not release tasks',async()=>{
  for(const answer of [null,'']){
    const h=harness({appDialog:async()=>answer,api:()=>assert.fail('unexpected mutation')});
    await h.releaseTask(task(),{disabled:false});
  }
  const h=harness({appDialog:()=>assert.fail('viewer cannot open recovery'),api:()=>assert.fail('unexpected mutation')});
  h.state.role='viewer';
  await h.releaseTask(task(),{disabled:false});
});

test('a concurrent update refreshes the board without retrying a stale release',async()=>{
  let calls=0,refreshes=0;
  const button={disabled:false};
  const h=harness({appDialog:async()=> 'Reviewed',api:async()=>{calls++;throw new Error('task version conflict');},notify:async()=>{},load:async()=>{refreshes++;}});
  await h.releaseTask(task(),button);
  assert.equal(calls,1);
  assert.equal(refreshes,1);
  assert.equal(button.disabled,false);
});

const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(`${__dirname}/app.js`,'utf8');
function workspace(){
  const context={Date,state:{tasks:[],filter:'open',project:'',search:'',searchAll:false,identity:'me',sort:'manual'}};
  vm.createContext(context);
  for(const name of ['localDate','openTasks','isMine','isMyWork','workGroup','deferred','matchesSearch','filteredTasks','viewTasks','sectionName','taskGroup']){
    const start=source.search(new RegExp(`^function ${name}\\(`,'m'));
    const end=source.indexOf('\nfunction ',start+1);
    vm.runInContext(source.slice(start,end),context);
  }
  return context;
}
const task=(id,values={})=>({id,title:'Check release',status:'queued',project:'Taskboard',section:'General',owner:'me',visibility:'private',runs:[],...values});
test('search stays inside a project and view until explicitly expanded',()=>{
  const h=workspace();h.state.tasks=[task('mine'),task('other',{owner:'agent',project:'Other'}),task('done',{status:'done'})];
  h.state.filter='mine';h.state.project='Taskboard';h.state.search='release';
  assert.deepEqual(Array.from(h.filteredTasks(),t=>t.id),['mine']);
  h.state.searchAll=true;
  assert.deepEqual(Array.from(h.filteredTasks(),t=>t.id),['mine','other','done']);
});
test('navigation counts can query another view without changing the current view',()=>{
  const h=workspace();h.state.tasks=[task('decision',{decision_requested:true}),task('waiting',{status:'waiting'}),task('done',{status:'done'})];
  h.state.filter='mine';
  assert.deepEqual(Array.from(h.viewTasks('needs-me'),t=>t.id),['decision']);
  assert.deepEqual(Array.from(h.viewTasks('done'),t=>t.id),['done']);
  assert.equal(h.state.filter,'mine');
});
test('agent pickup excludes deferred, unready, and running work',()=>{
  const h=workspace();h.state.tasks=[task('ready',{visibility:'agent',ready:true}),task('deferred',{visibility:'agent',ready:true,defer_until:'2999-01-01'}),task('running',{visibility:'agent',ready:true,runs:[{status:'active'}]}),task('blocked',{visibility:'agent',ready:false})];
  assert.deepEqual(Array.from(h.viewTasks('agent'),t=>t.id),['ready']);
});
test('project grouping retains non-default sections',()=>{
  const h=workspace();
  assert.equal(h.taskGroup(task('a')),'Taskboard');
  assert.equal(h.taskGroup(task('a',{section:'Operations'})),'Taskboard / Operations');
  assert.equal(h.taskGroup(task('a',{project:''})),'General');
});
test('worker requirements parse from the create form but stay out of edit and template payloads',()=>{
  const context={state:{taskType:'work'}},fields={taskTitle:'Ship it',taskRequirements:'profile:builder'};
  context.$=selector=>selector==='#checklistEditor'?{querySelectorAll:()=>[]}:{value:fields[selector.slice(1)]||''};
  vm.createContext(context);
  for(const name of ['requirementList','deferPayload','taskFormPayload']){
    const start=source.search(new RegExp(`^function ${name}\\(`,'m'));
    vm.runInContext(source.slice(start,source.indexOf('\n',start)),context);
  }
  assert.deepEqual(Array.from(context.requirementList(' profile:builder, browser ,,')),['profile:builder','browser']);
  assert.equal('requirements' in context.taskFormPayload(),false);
  assert.match(source,/captureTask\(\{\.\.\.payload,requirements:requirementList\(\$\('#taskRequirements'\)\.value\)\}\)/);
});
test('picking an advertised capability appends it once to free-text requirements',()=>{
  const context={};vm.createContext(context);
  for(const name of ['requirementList','addRequirement']){
    const start=source.search(new RegExp(`^function ${name}\\(`,'m'));
    vm.runInContext(source.slice(start,source.indexOf('\n',start)),context);
  }
  assert.equal(context.addRequirement('',"profile:pickup"),'profile:pickup');
  assert.equal(context.addRequirement('profile:not-deployed-yet, ',"runner:k8s-job"),'profile:not-deployed-yet, runner:k8s-job');
  assert.equal(context.addRequirement('runner:k8s-job',"runner:k8s-job"),'runner:k8s-job');
});
test('card requirement suggestions hide with the requirements form on finished tasks',()=>{
  const html=fs.readFileSync(`${__dirname}/index.html`,'utf8');
  assert.match(html,/<form class="requirements-form">(?:(?!<\/form>)[\s\S])*class="requirement-suggestions"(?:(?!<\/form>)[\s\S])*<\/form>/);
});
test('requirement suggestions show each capability with its description',()=>{
  const created=[];
  const element=tag=>{const node={tag,children:[],attributes:{},className:'',textContent:'',title:'',listeners:{},append(...items){node.children.push(...items)},setAttribute(name,value){node.attributes[name]=value},addEventListener(name,fn){node.listeners[name]=fn},replaceChildren(...items){node.children=items}};created.push(node);return node};
  const context={state:{workerCapabilities:[{capability:'profile:pickup',description:'Runs a queued task'},{capability:'discussion'},{capability:'runner:k8s-job',description:'Runs in a Job'}]},document:{createElement:element},Event:class{constructor(type){this.type=type}}};
  vm.createContext(context);
  for(const name of ['requirementList','addRequirement','renderRequirementSuggestions']){
    const start=source.search(new RegExp(`^function ${name}\\(`,'m'));
    vm.runInContext(source.slice(start,source.indexOf('\n',start)),context);
  }
  const container=element('div'),input={value:'runner:k8s-job',dispatchEvent(){},focus(){}};
  context.renderRequirementSuggestions(container,input);
  const buttons=container.children.slice(1);
  assert.deepEqual(buttons.map(button=>button.children[0].textContent),['+ profile:pickup','+ discussion']);
  assert.equal(buttons[0].title,'Runs a queued task');
  assert.equal(buttons[0].children[1].textContent,'Runs a queued task');
  assert.equal(buttons[0].attributes['aria-label'],'Add requirement profile:pickup: Runs a queued task');
  assert.equal(buttons[1].children.length,1);
  buttons[0].listeners.click();
  assert.equal(input.value,'runner:k8s-job, profile:pickup');
});
test('timestamp deferrals hold agent pickup until the moment passes',()=>{
  const h=workspace(),future=new Date(Date.now()+3600e3).toISOString(),past=new Date(Date.now()-60e3).toISOString();
  h.state.tasks=[task('later',{visibility:'agent',ready:true,defer_until:future}),task('now',{visibility:'agent',ready:true,defer_until:past})];
  assert.deepEqual(Array.from(h.viewTasks('agent'),t=>t.id),['now']);
  assert.deepEqual(Array.from(h.viewTasks('upcoming'),t=>t.id),['later']);
});
test('defer date and optional time combine into the request value',()=>{
  const fields={taskDeferUntil:'2026-10-20',taskDeferTime:''},context={Date,$:selector=>({value:fields[selector.slice(1)]||''})};
  vm.createContext(context);
  const start=source.search(/^function deferPayload\(/m);
  vm.runInContext(source.slice(start,source.indexOf('\n',start)),context);
  assert.equal(context.deferPayload(),'2026-10-20');
  fields.taskDeferTime='07:30';
  assert.equal(context.deferPayload(),new Date('2026-10-20T07:30').toISOString());
  fields.taskDeferUntil='';
  assert.equal(context.deferPayload(),'');
});
test('a loaded deferral schedules a reload when it ends, and a lagging server is rechecked each minute',()=>{
  const context={Date};vm.createContext(context);
  for(const name of ['nextDeferRefresh']){const start=source.search(new RegExp(`^function ${name}\\(`,'m'));vm.runInContext(source.slice(start,source.indexOf('\n',start)),context)}
  const now=Date.parse('2026-10-18T12:00:00Z');
  assert.equal(context.nextDeferRefresh([task('a')],now),null);
  assert.equal(context.nextDeferRefresh([task('a',{deferred:true,defer_until:'2026-10-18T12:30:00Z'}),task('b',{deferred:true,defer_until:'2026-10-18T14:00:00Z'})],now),30*60*1000+1000);
  assert.equal(context.nextDeferRefresh([task('a',{deferred:true,defer_until:'2026-10-18T11:00:00Z'})],now),60000);
  assert.equal(context.nextDeferRefresh([task('a',{deferred:false,defer_until:'2026-10-18T12:30:00Z'})],now),null);
});
test('editing a schedule keeps the fields the form does not show',()=>{
  const context={};vm.createContext(context);
  const start=source.search(/^function scheduleRequest\(/m);vm.runInContext(source.slice(start,source.indexOf('\n',start)),context);
  const loaded={name:'audit',enabled:true,cron:'0 7 * * *',timezone:'UTC',overlap:'skip',title:'Audit',type:'work',summary:'',section:'Operations',project:'',repository:'',priority:'high',checklist:[],requirements:[],version:3};
  const body=context.scheduleRequest(loaded,{name:'audit',cron:'0 8 * * *',title:'Audit'});
  assert.equal(body.cron,'0 8 * * *');
  assert.deepEqual([body.type,body.section,body.priority,body.expected_version],['work','Operations','high',3]);
});

test('My work includes delegated tasks I created without changing My tasks',()=>{
  const h=workspace();h.state.principal='me';
  h.state.tasks=[task('assigned'),task('delegated',{created_by:'me',owner:'agent:worker'}),task('other',{created_by:'other',owner:'other'}),task('done',{created_by:'me',status:'done'})];
  assert.deepEqual(Array.from(h.viewTasks('work'),t=>t.id),['assigned','delegated']);
  assert.deepEqual(Array.from(h.viewTasks('mine'),t=>t.id),['assigned']);
});
test('My work distinguishes attention, active, queued, and deferred tasks',()=>{
  const h=workspace();
  assert.equal(h.workGroup(task('question',{decision_requested:true})),'Needs your attention');
  assert.equal(h.workGroup(task('blocked',{status:'blocked'})),'Needs your attention');
  assert.equal(h.workGroup(task('overdue',{due_date:'2000-01-01'})),'Needs your attention');
  assert.equal(h.workGroup(task('active',{status:'active'})),'In progress');
  assert.equal(h.workGroup(task('ready')),'Ready to start');
  assert.equal(h.workGroup(task('later',{defer_until:'2999-01-01'})),'Upcoming');
});
test('quick capture remains private and keeps only the current project default',()=>{
  const h={state:{project:'Taskboard',taskType:''}};vm.createContext(h);
  const start=source.indexOf('function quickCapturePayload(');
  vm.runInContext(source.slice(start,source.indexOf('\n',start)),h);
  assert.deepEqual(JSON.parse(JSON.stringify(h.quickCapturePayload('  Write notes  '))),{title:'Write notes',visibility:'private',type:'personal',section:'General',project:'Taskboard'});
  h.state.taskType='work';assert.equal('type' in h.quickCapturePayload('Write notes'),false);
});
test('next actions preserve agent control and permission boundaries',()=>{
  const h=workspace();h.canWriteTasks=()=>true;h.recoveryRun=()=>null;h.canRecover=()=>false;h.deferLabel=value=>value;
  const start=source.indexOf('function nextTaskAction('),end=source.indexOf('\nfunction renderNextAction',start);
  vm.runInContext(source.slice(start,end),h);
  const sample=values=>task('action',{items:[],...values});
  assert.equal(h.nextTaskAction(sample({})).kind,'start');
  assert.equal(h.nextTaskAction(sample({visibility:'agent'})).kind,'messages');
  assert.equal(h.nextTaskAction(sample({status:'active',runs:[{status:'active'}]})).kind,'messages');
  assert.equal(h.nextTaskAction(sample({defer_until:'2999-01-01'})).kind,'properties');
  assert.equal(h.nextTaskAction(sample({ready:false,dependencies:[{satisfied:false}]})).kind,'properties');
  assert.equal(h.nextTaskAction(sample({status:'active',items:[{status:'done'}]})).kind,'delivery');
  h.canWriteTasks=()=>false;
  assert.equal(h.nextTaskAction(sample({})).kind,'messages');
  assert.equal(h.nextTaskAction(sample({decision_requested:true})).label,'View decision');
});
test('quick capture keeps the title on failure and prevents concurrent submissions',async()=>{
  const fields={
    '#quickTaskTitle':{value:'Keep my draft',setCustomValidity(){},reportValidity:()=>true,focus(){}},
    '#quickCaptureForm button':{disabled:false},
    '#quickCaptureError':{textContent:''},'#quickCaptureStatus':{textContent:''},
  };
  let rejectSave,calls=0;
  const h={$:selector=>fields[selector],quickCapturePayload:title=>({title}),captureTask:()=>{calls++;return new Promise((_,reject)=>{rejectSave=reject})}};
  vm.createContext(h);
  const start=source.indexOf('async function quickCapture('),end=source.indexOf('\nfunction setFilter',start);
  vm.runInContext(source.slice(start,end),h);
  const event={preventDefault(){}};
  const saving=h.quickCapture(event);
  await h.quickCapture(event);
  assert.equal(calls,1);
  assert.equal(fields['#quickTaskTitle'].readOnly,true);
  rejectSave(new Error('Temporary save failure'));await saving;
  assert.equal(fields['#quickTaskTitle'].value,'Keep my draft');
  assert.equal(fields['#quickCaptureError'].textContent,'Temporary save failure');
  assert.equal(fields['#quickCaptureForm button'].disabled,false);
  assert.equal(fields['#quickTaskTitle'].readOnly,false);
});

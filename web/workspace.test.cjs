const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(`${__dirname}/app.js`,'utf8');
function workspace(){
  const context={Date,state:{tasks:[],filter:'open',project:'',search:'',searchAll:false,identity:'me',sort:'manual'}};
  vm.createContext(context);
  for(const name of ['localDate','openTasks','isMine','deferred','matchesSearch','filteredTasks','viewTasks','sectionName','taskGroup']){
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
  for(const name of ['requirementList','taskFormPayload']){
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

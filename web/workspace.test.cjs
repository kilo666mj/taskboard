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

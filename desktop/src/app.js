const invoke=window.__TAURI__.core.invoke;
const listen=window.__TAURI__.event.listen;
const form=document.querySelector('#server-form');
const list=document.querySelector('#servers');
const empty=document.querySelector('#empty');
const heading=document.querySelector('#editor-heading');
const nameInput=document.querySelector('#name');
const originInput=document.querySelector('#origin');
const previousInput=document.querySelector('#previous-origin');
const error=document.querySelector('#error');
const saveButton=document.querySelector('#save');
const cancelButton=document.querySelector('#cancel');
let state={servers:[],selected_origin:null};

function resetForm({focus=false}={}){
  form.reset();
  previousInput.value='';
  heading.textContent='Add server';
  saveButton.textContent='Add server';
  cancelButton.hidden=true;
  error.textContent='';
  if(focus)nameInput.focus();
}

function startEdit(server){
  previousInput.value=server.origin;
  nameInput.value=server.name;
  originInput.value=server.origin;
  heading.textContent=`Edit ${server.name}`;
  saveButton.textContent='Save changes';
  cancelButton.hidden=false;
  error.textContent='';
  nameInput.focus();
  nameInput.select();
}

function button(label,className,action){
  const element=document.createElement('button');
  element.type='button';
  element.textContent=label;
  element.className=className;
  element.addEventListener('click',action);
  return element;
}

async function run(action){
  error.textContent='';
  try{return await action()}catch(reason){error.textContent=String(reason);throw reason}
}

function render(){
  empty.hidden=state.servers.length!==0;
  list.replaceChildren(...state.servers.map(server=>{
    const item=document.createElement('li');
    const description=document.createElement('div');
    const title=document.createElement('strong');
    title.textContent=server.name;
    if(server.origin===state.selected_origin){
      const current=document.createElement('span');
      current.className='current';
      current.textContent='Current';
      title.append(' ',current);
    }
    const address=document.createElement('span');
    address.textContent=server.origin;
    description.append(title,address);
    const actions=document.createElement('div');
    actions.className='server-actions';
    if(server.origin!==state.selected_origin){
      actions.append(button('Switch','primary compact',async()=>{
        state=await run(()=>invoke('switch_server',{origin:server.origin}));
        render();
      }));
    }
    actions.append(button('Edit','secondary compact',()=>startEdit(server)));
    actions.append(button('Remove','danger compact',async()=>{
      if(!confirm(`Remove ${server.name}?`))return;
      state=await run(()=>invoke('remove_server',{origin:server.origin}));
      if(previousInput.value===server.origin)resetForm();
      render();
    }));
    item.append(description,actions);
    return item;
  }));
}

async function refresh(){
  state=await run(()=>invoke('server_state'));
  render();
}

form.addEventListener('submit',async event=>{
  event.preventDefault();
  saveButton.disabled=true;
  try{
    state=await run(()=>invoke('save_server',{
      name:nameInput.value,
      origin:originInput.value,
      previousOrigin:previousInput.value||null
    }));
    resetForm();
    render();
  }catch(_){
    // run() has already exposed the native validation error.
  }finally{
    saveButton.disabled=false;
  }
});

cancelButton.addEventListener('click',()=>resetForm({focus:true}));
listen('taskboard://servers-changed',refresh);
listen('taskboard://add-server',()=>resetForm({focus:true}));
listen('taskboard://manage-servers',()=>nameInput.blur());
refresh().then(()=>{if(!state.servers.length)nameInput.focus()});

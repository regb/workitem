const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const vm = require('node:vm');

async function ui() {
  const elements = new Map(), timers = new Map();
  let timerID = 0;
  function element() {
    return { value: '', disabled: false, hidden: true, children: [], textContent: '', style: {}, dataset: {}, scrollHeight: 0, scrollTop: 0, clientHeight: 0,
      get childElementCount() { return this.children.length; },
      replaceChildren(...children) { this.children = children; },
      append(...children) { this.children.push(...children); },
      prepend(...children) { this.children.unshift(...children); },
      setAttribute(name, value) { this[name] = value; },
      focus() {}, reportValidity() { return true; }, requestSubmit() {},
    };
  }
  const get = id => { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); };
  const storage = new Map();
  const context = vm.createContext({
    document: { getElementById: get, createElement: element },
    location: { hash: '', search: '', pathname: '/' }, innerHeight: 800,
    history: { replaceState() {}, pushState() {} }, sessionStorage: { getItem(key) { return storage.get(key) || ''; }, setItem(key,value) { storage.set(key,value); } },
    window: { addEventListener() {} }, URLSearchParams,
    fetch: async () => { throw new Error('No network in UI tests'); },
    setTimeout(callback, delay) { const id = ++timerID; timers.set(id, { callback, delay }); return id; },
    clearTimeout(id) { timers.delete(id); },
  });
  vm.runInContext(fs.readFileSync(`${__dirname}/static/app.js`, 'utf8'), context);
  // Settle initial list/receipt requests before exercising the UI.
  for (let i = 0; i < 8; i++) await Promise.resolve();
  return { get, context, timers };
}

for (const [inputID, formID, buttonID] of [['message', 'composer', 'send'], ['new-prompt', 'new-form', 'create']]) {
  test(`${inputID}: Enter sends; Shift+Enter and IME do not`, async () => {
    const { get } = await ui();
    get('new-project').value = 'repo';
    let submissions = 0;
    get(formID).requestSubmit = button => { assert.equal(button, get(buttonID)); submissions++; };
    for (const scenario of [
      { key: 'Enter', sends: true, prevents: true },
      { key: 'Enter', shiftKey: true },
      { key: 'Enter', isComposing: true },
      { key: 'Enter', keyCode: 229 },
      { key: 'Enter', repeat: true, prevents: true },
      { key: 'Enter', disabled: true, prevents: true },
      { key: 'Enter', blank: true, prevents: true },
      { key: 'Escape' },
    ]) {
      get(inputID).value = scenario.blank ? '  ' : 'Task';
      get(buttonID).disabled = !!scenario.disabled;
      const before = submissions;
      let prevented = false;
      get(inputID).onkeydown({ ...scenario, preventDefault() { prevented = true; } });
      assert.equal(submissions - before, scenario.sends ? 1 : 0);
      assert.equal(prevented, !!scenario.prevents);
    }
  });
}

test('mobile list cards put needs attention first and keep two compact rows', async () => {
  const { get, context } = await ui();
  vm.runInContext(`listData={deep_work:{active:0,limit:2},projects:[{id:'repo',name:'demo'}],display_sections:[
    {name:'IN PROGRESS',items:[{id:'busy',title:'Busy item',project_id:'repo',agent:{status:'busy'},worktree:{status:'clean'}}]},
    {name:'NEEDS ATTENTION',items:[{id:'ready',title:'Ready item',project_id:'repo',agent:{status:'idle'},worktree:{status:'changed'}}]}
  ]};selectedProjects=null;renderList();`,context);
  const cards=get('work-list').children[1];
  assert.equal(cards.className,'mobile-work-list');
  assert.equal(cards.children[0].className,'mobile-bucket needs-attention');
  const card=cards.children[0].children[1], open=card.children[0];
  assert.equal(open.children.length,2);
  assert.equal(open.children[0].textContent,'Ready item');
  assert.equal(open.children[1].children.length,1);
  assert.equal(open.children[1].children[0].textContent,'demo');
  assert.equal(card.children[1].textContent,'Settle');
  assert.equal(card.children[1].className,'card-settle');
});

test('attention control is actionable only when work needs attention', async () => {
  const { get, context } = await ui();
  vm.runInContext('setAttention(0)',context);
  assert.equal(get('attention').disabled,true);
  assert.equal(get('attention').className,'attention-clear');
  assert.equal(get('attention').textContent,'Nothing needs attention');
  vm.runInContext('setAttention(3)',context);
  assert.equal(get('attention').disabled,false);
  assert.equal(get('attention').className,'attention-ready');
  assert.equal(get('attention').textContent,'Attention · 3');
});

test('tool history renders one folded disclosure with its completed result', async () => {
  const { context } = await ui();
  vm.runInContext(`window.rendered=renderMessage({role:'assistant',text:'Checked **files**.',tools:[{id:'tool-1',name:'bash',input:'ls',output:'README.md',status:'completed'}]})`, context);
  const article=context.window.rendered, body=article.children[1], tools=body.children[1], disclosure=tools.children[0];
  assert.equal(article.className,'message assistant');
  assert.equal(disclosure.className,'tool completed');
  assert.equal(disclosure.children[0].children[0].textContent,'bash');
  assert.equal(disclosure.children[0].children[1].textContent,'completed');
  assert.equal(disclosure.open, undefined);
  assert.equal(tools.children.length,1);
});

test('pasting an image into the message composer creates a visible attachment', async () => {
  const { get, context } = await ui();
  vm.runInContext(`readImage=async()=> 'aW1hZ2U=';lastChat={runtime:{online:true,capabilities:{prompt:true,image_input:true}},idle_guard:true,item:{state:'working'}}`,context);
  get('delivery').value='prompt';
  let prevented=false;
  await get('message').onpaste({preventDefault(){prevented=true;},clipboardData:{items:[{kind:'file',type:'image/png',getAsFile(){return {name:'paste.png',type:'image/png',size:5};}}]}});
  assert.equal(prevented,true);
  assert.equal(get('attachments').hidden,false);
  assert.equal(get('attachments').children.length,1);
  assert.equal(get('attachments').children[0].children[0].src,'data:image/png;base64,aW1hZ2U=');
  assert.equal(get('send').disabled,false);
});

test('chat renders uploaded images and safe image links', async () => {
  const { context } = await ui();
  vm.runInContext(`window.rendered=renderMessage({role:'user',text:'See ![remote](https://example.test/photo.png)',images:[{mime_type:'image/png',data:'aW1hZ2U=',alt:'upload.png'}]})`,context);
  const body=context.window.rendered.children[1], markdown=body.children[0], gallery=body.children[1];
  const inline=markdown.children[0].children[0].children.find(child=>child.className==='inline-image');
  assert.equal(inline.src,'https://example.test/photo.png');
  assert.equal(gallery.className,'message-images');
  assert.equal(gallery.children[0].src,'data:image/png;base64,aW1hZ2U=');
});

test('live tool changes status in place instead of appending a second call', async () => {
  const { get, context } = await ui();
  get('history').scrollHeight=0;get('history').scrollTop=0;get('history').clientHeight=0;
  vm.runInContext(`historyToolIDs=new Set(['older-bash-call']);renderLive({activity:'Tool running: bash',text:'',tool:{id:'tool-1',name:'bash',status:'running',input:'{"command":"pwd"}'}})`,context);
  assert.equal(get('live').children.length,1);
  assert.equal(get('live').children[0].children[1].children[0].className,'tool running');
  vm.runInContext(`renderLive({activity:'Tool completed: bash',text:'',tool:{id:'tool-1',name:'bash',status:'completed',input:'{"command":"pwd"}',output:'/tmp'}})`,context);
  assert.equal(get('live').children.length,1);
  assert.equal(get('live').children[0].children[1].children[0].className,'tool completed');
});

test('sending renders an optimistic user message before the server responds', async () => {
  const { get, context } = await ui();
  const id='01K00000000000000000000000';
  vm.runInContext(`item='${id}';runtime='runtime';lastChat={history:{leaf_id:'old',messages:[{id:'old',role:'assistant',text:'Ready'}]},runtime:{online:true,capabilities:{prompt:true,follow_up:true}},item:{state:'working'}}`,context);
  get('message').value='Do the thing';get('delivery').value='prompt';
  let finishSend;
  context.fetch=async path=>{
    if(path.endsWith('/messages'))return new Promise(resolve=>{finishSend=()=>resolve({ok:true,json:async()=>({result:{}})});});
    throw new Error('stop refresh');
  };
  const submission=get('composer').onsubmit({preventDefault(){}});
  await Promise.resolve();
  assert.equal(get('message').value,'');
  const optimistic=get('history').children.at(-1);
  assert.equal(optimistic.className,'message user pending sending');
  assert.equal(optimistic.children[1].children[0].children[0].children[0].children[0].textContent,'Do the thing');
  finishSend();await submission;
  assert.equal(get('history').children.at(-1).className,'message user pending sent');
});

test('pending startup displays only a spinner without history or live reads', async () => {
  const { get, context, timers } = await ui();
  const requested=[];
  context.fetch=async path=>{requested.push(path);return {ok:true,json:async()=>({item:{id:'01K00000000000000000000000'},startup:{state:'starting'}})}};
  await vm.runInContext('openChat("01K00000000000000000000000")',context);
  assert.equal(get('chat-loading').hidden,false);
  assert.equal(get('chat').hidden,true);
  assert.equal(get('archive').hidden,true);
  assert.equal(get('notice').textContent,'');
  assert.equal(requested.length,1);
  assert.ok(requested[0].endsWith('/chat'));
  assert.ok([...timers.values()].some(timer=>timer.delay===500));
  vm.runInContext('stopChat()',context);
  assert.equal(get('chat-loading').hidden,true);
});

test('Enter without a repository marks the picker invalid without sending', async () => {
  const { get } = await ui();
  get('new-prompt').value = 'Task'; get('create').disabled = true;
  let submitted = false, focused = false;
  get('new-form').requestSubmit = () => { submitted = true; };
  get('new-project').focus = () => { focused = true; };
  get('new-prompt').onkeydown({ key: 'Enter', preventDefault() {} });
  assert.equal(submitted, false);
  assert.equal(focused, true);
  assert.equal(get('new-project')['aria-invalid'], 'true');
  assert.equal(get('new-project-error').hidden, false);
  get('new-project').value = 'repo'; get('new-project').onchange();
  assert.equal(get('new-project')['aria-invalid'], 'false');
  assert.equal(get('new-project-error').hidden, true);
});

test('creation receipt shows a six-second toast linked by immutable ID, not title', async () => {
  const { get, context, timers } = await ui();
  vm.runInContext(`newProjects=[{id:'repo'}];newAgents=[{name:'pi-deep',label:'Pi · Deep',driver:'pi'}];`, context);
  get('new-project').value = 'repo'; get('new-agent').value='pi-deep'; get('new-prompt').value = 'Task';
  const id = '01K00000000000000000000000';
  context.fetch = async () => ({ ok: true, json: async () => ({ id, title: 'New conversation', state: 'queued' }) });
  await get('new-form').onsubmit({ preventDefault() {} });
  const toast = get('started-toast');
  assert.equal(toast.hidden, false);
  assert.equal(toast.children[0].textContent, 'Work item started');
  const link = toast.children[1];
  assert.equal(link.href, `/?item=${id}`);
  assert.equal(get('new-prompt').value, '');
  assert.equal(context.location.pathname, '/');
  vm.runInContext('openChat=async id=>{window.openedID=id;};', context);
  let prevented = false;
  link.onclick({ button: 0, preventDefault() { prevented = true; } });
  assert.equal(prevented, true);
  assert.equal(context.window.openedID, id);
  const expiry = [...timers.values()].find(timer => timer.delay === 6000);
  assert.ok(expiry); expiry.callback();
  assert.equal(toast.hidden, true);
  assert.equal(toast.children.length, 0);
});

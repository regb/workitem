'use strict';
const el = id => document.getElementById(id);
const hash = new URLSearchParams(location.hash.slice(1));
if (hash.get('token')) { sessionStorage.setItem('wi-token', hash.get('token')); history.replaceState(null, '', '/'); }
const token = sessionStorage.getItem('wi-token') || '';
let lastChat;
let item = '', runtime = '', generation = 0, streaming, refreshTimer, pending = false, lastHistory = '';
let historyToolIDs = new Set();
const drafts = new Map();
let creating = false, creationUnknown = false, newProjects = [], newAgents = [], lastLaunchErrors = '';
const pendingLaunches = new Map();
let outbox = {}, selectedImages=[];
try { outbox=JSON.parse(sessionStorage.getItem('wi-outbox')||'{}'); } catch(_) { outbox={}; }
function saveOutbox(){try{sessionStorage.setItem('wi-outbox',JSON.stringify(outbox,(key,value)=>key==='data'?'':value));}catch(_){/* Keep the in-memory outbox when storage is full. */}}
function itemOutbox(id=item){return outbox[id]||(outbox[id]=[]);}
function discardOutboxMessage(id,clientID){outbox[id]=(outbox[id]||[]).filter(message=>message.client_id!==clientID);saveOutbox();}
function restoreOutboxMessage(id,message){if(id!==item)return;el('message').value=message.text;selectedImages=(message.images||[]).filter(image=>image.data).map(image=>({...image,name:image.alt||'Image',bytes:Math.floor(image.data.length*.75)}));renderAttachments();discardOutboxMessage(id,message.client_id);el('message').focus();el('message').oninput?.();renderConversationHistory(lastChat?.history);}
let startedToastTimer;
function showStartedToast(id) {
  const toast=el('started-toast'), link=node('a','Open conversation');
  link.href=`/?item=${encodeURIComponent(id)}`;
  link.onclick=event=>{
    if(event.button!==0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey)return;
    event.preventDefault();openChat(id).catch(error=>notice(error.message));
  };
  clearTimeout(startedToastTimer);
  toast.replaceChildren(node('span','Work item started'),link);toast.hidden=false;
  startedToastTimer=setTimeout(()=>{toast.hidden=true;toast.replaceChildren();},6000);
}
let listData, selectedProjects = null; // null tracks all projects, including newly added ones.
function node(tag, text, className) {
  const element = document.createElement(tag);
  if (text !== undefined) element.textContent = text;
  if (className) element.className = className;
  return element;
}
function safeImage(src,alt='Image') {
  if(!/^https:\/\//.test(src)&&!/^data:image\/(png|jpeg|gif|webp);base64,/.test(src))return null;
  const image=node('img');image.src=src;image.alt=alt;image.loading='lazy';image.decoding='async';return image;
}
function inlineMarkdown(text) {
  const fragment=node('span');
  const pattern=/(!\[[^\]]*\]\([^)]+\)|`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\([^)]+\)|https:\/\/[^\s)]+\.(?:png|jpe?g|gif|webp)(?:\?[^\s)]*)?)/gi;
  let start=0, match;
  while((match=pattern.exec(text))){
    if(match.index>start)fragment.append(node('span',text.slice(start,match.index)));
    const token=match[0];
    if(token.startsWith('![')){const parts=token.match(/^!\[([^\]]*)\]\(([^)]+)\)$/),image=safeImage(parts?.[2]||'',parts?.[1]||'Image');if(image){image.className='inline-image';fragment.append(image);}else fragment.append(node('span',token));}
    else if(/^https:\/\//i.test(token)){const image=safeImage(token);if(image){image.className='inline-image';fragment.append(image);}}
    else if(token.startsWith('`'))fragment.append(node('code',token.slice(1,-1)));
    else if(token.startsWith('**'))fragment.append(node('strong',token.slice(2,-2)));
    else if(token.startsWith('*'))fragment.append(node('em',token.slice(1,-1)));
    else {
      const parts=token.match(/^\[([^\]]+)\]\(([^)]+)\)$/), href=parts?.[2] || '';
      if(/^https?:\/\//.test(href)){const link=node('a',parts[1]);link.href=href;link.target='_blank';link.rel='noreferrer';fragment.append(link);}
      else fragment.append(node('span',token));
    }
    start=pattern.lastIndex;
  }
  if(start<text.length)fragment.append(node('span',text.slice(start)));
  return fragment;
}
function renderMarkdown(text) {
  const root=node('div',undefined,'markdown'), lines=String(text||'').replaceAll('\r\n','\n').split('\n');
  let i=0;
  while(i<lines.length){
    const line=lines[i];
    if(line.startsWith('```')){const language=line.slice(3).trim(), code=[];i++;while(i<lines.length&&!lines[i].startsWith('```'))code.push(lines[i++]);if(i<lines.length)i++;const pre=node('pre'), content=node('code',code.join('\n'));if(language)content.className=`language-${language}`;pre.append(content);root.append(pre);continue;}
    if(!line.trim()){i++;continue;}
    const heading=line.match(/^(#{1,3})\s+(.+)$/);if(heading){const h=node(`h${heading[1].length}`);h.append(inlineMarkdown(heading[2]));root.append(h);i++;continue;}
    if(/^---+$/.test(line.trim())){root.append(node('hr'));i++;continue;}
    if(/^>\s?/.test(line)){const parts=[];while(i<lines.length&&/^>\s?/.test(lines[i]))parts.push(lines[i++].replace(/^>\s?/,''));const q=node('blockquote');q.append(inlineMarkdown(parts.join(' ')));root.append(q);continue;}
    const listMatch=line.match(/^\s*([-*]|\d+\.)\s+(.+)$/);if(listMatch){const tag=/\d+\./.test(listMatch[1])?'ol':'ul', list=node(tag);while(i<lines.length){const part=lines[i].match(/^\s*([-*]|\d+\.)\s+(.+)$/);if(!part||(/\d+\./.test(part[1])?'ol':'ul')!==tag)break;const li=node('li');li.append(inlineMarkdown(part[2]));list.append(li);i++;}root.append(list);continue;}
    const parts=[line];i++;while(i<lines.length&&lines[i].trim()&&!lines[i].startsWith('```')&&!/^(#{1,3})\s+/.test(lines[i])&&!/^\s*([-*]|\d+\.)\s+/.test(lines[i])&&!/^>\s?/.test(lines[i]))parts.push(lines[i++]);const p=node('p');p.append(inlineMarkdown(parts.join('\n')));root.append(p);
  }
  return root;
}
function renderImages(images) {
  const gallery=node('div',undefined,'message-images');
  for(const value of images||[]){const image=safeImage(value.data?`data:${value.mime_type};base64,${value.data}`:value.url,value.alt||'Image');if(image)gallery.append(image);}
  return gallery;
}
function renderTool(tool) {
  const details=node('details',undefined,`tool ${tool.status||'completed'}`), summary=node('summary');
  summary.append(node('span',tool.name||'tool','tool-name'),node('span',tool.status||'completed','tool-status'));details.append(summary);
  if(tool.input||tool.output||tool.images?.length){const content=node('div',undefined,'tool-content');if(tool.input)content.append(node('h4','Input'),node('pre',tool.input));if(tool.output)content.append(node('h4','Output'),node('pre',tool.output));if(tool.images?.length)content.append(renderImages(tool.images));details.append(content);}
  return details;
}
function renderMessage(message) {
  const role=message.role==='user'?'user':message.role==='summary'?'summary':'assistant';
  const pendingStatus=message.pending_status;
  const article=node('article',undefined,`message ${role}${pendingStatus?' pending '+pendingStatus:''}`), label=node('div',role==='user'?'You':role==='summary'?'Summary':'Agent','role'), body=node('div',undefined,'message-body');
  if(message.text)body.append(renderMarkdown(message.text));
  if(message.images?.length)body.append(renderImages(message.images));
  if(message.tools?.length){const tools=node('div',undefined,'tools');for(const tool of message.tools)tools.append(renderTool(tool));body.append(tools);}
  if(pendingStatus){const labels={sending:'Sending…',sent:'Sent · waiting for agent',unknown:'Delivery unknown',failed:'Not sent'}, status=node('div',labels[pendingStatus]||pendingStatus,'message-delivery');if(['failed','unknown'].includes(pendingStatus))status.append(button('Edit and retry',async()=>restoreOutboxMessage(item,message),'retry-message'));body.append(status);}
  article.append(label,body);return article;
}
function reconcileOutbox(history) {
  const queued=itemOutbox();if(!queued.length)return;
  for(const pendingMessage of [...queued]){
    const anchor=(history.messages||[]).findIndex(message=>message.id===pendingMessage.after_id);
    const later=(history.messages||[]).slice(anchor+1);
    if(later.some(message=>message.role==='user'&&message.text===pendingMessage.text&&(!pendingMessage.images?.length||message.images?.length===pendingMessage.images.length)))discardOutboxMessage(item,pendingMessage.client_id);
  }
}
function renderConversationHistory(history=lastChat?.history) {
  if(!history)return;
  reconcileOutbox(history);
  const pane=el('history'), bottom=pane.scrollHeight-pane.scrollTop-pane.clientHeight<80, pos=pane.scrollTop;pane.replaceChildren();historyToolIDs=new Set();
  for(const message of history.messages||[]){pane.append(renderMessage(message));for(const tool of message.tools||[])if(tool.status==='completed'&&tool.id)historyToolIDs.add(tool.id);}
  for(const message of itemOutbox())pane.append(renderMessage({...message,role:'user',pending_status:message.status}));
  if(history.truncated)pane.prepend(node('p','Showing the latest conversation. Older or oversized content was omitted.','meta'));
  if(!pane.childElementCount)pane.append(node('p','No messages yet.','meta'));
  pane.scrollTop=bottom||!lastHistory?pane.scrollHeight:pos;
}
function closeChatMenu(){el('chat-menu').open=false;}
function setListMode(active){document.body?.classList?.toggle('list-mode',active);}
function setNewMode(active){document.body?.classList?.toggle('new-mode',active);}
function setChatMode(active){document.body?.classList?.toggle('chat-mode',active);if(active){setListMode(false);setNewMode(false);}el('chat-menu').hidden=!active;if(!active){closeTicket();closeChatMenu();}}
function openTicket(){closeChatMenu();el('ticket').className='open';el('ticket').setAttribute('aria-hidden','false');el('menu-details').setAttribute('aria-expanded','true');el('ticket-backdrop').hidden=false;}
function closeTicket(){el('ticket').className='';el('ticket').setAttribute('aria-hidden','true');el('menu-details').setAttribute('aria-expanded','false');el('ticket-backdrop').hidden=true;}
function statusBadge(status) { return node('span', status || 'unknown', `badge status-${status || 'unknown'}`); }
function labels(values, capacityFull=false) {
  const container = node('div');
  if (capacityFull) container.append(node('span', 'capacity full', 'label capacity-full'));
  for (const value of values || []) container.append(node('span', value, 'label'));
  return container;
}
function setAttention(count){const attention=el('attention');attention.hidden=false;attention.disabled=count===0;attention.className=count?'attention-ready':'attention-clear';attention.textContent=count?`Attention · ${count}`:'Nothing needs attention';}
function projectQuery() {
  const query = new URLSearchParams();
  if (selectedProjects !== null) {
    for (const id of selectedProjects) query.append('project', id);
    if (!selectedProjects.size) query.append('project', ''); // Explicitly empty, not all.
  }
  return query;
}
function updateProjectSummary() {
  const projects = listData?.projects || [];
  const count = projects.filter(p => selectedProjects === null || selectedProjects.has(p.id)).length;
  el('project-summary').textContent = selectedProjects === null ? 'Projects · All projects' : `Projects · ${count} selected`;
  for (const input of el('project-options').querySelectorAll('input')) input.checked = selectedProjects === null || selectedProjects.has(input.value);
}
function renderProjects() {
  el('project-options').replaceChildren();
  for (const project of listData.projects || []) {
    const label = node('label', undefined, 'project-option');
    const input = node('input'); input.type = 'checkbox'; input.value = project.id;
    const name = node('span', project.name);
    input.onchange = () => {
      if (selectedProjects === null) selectedProjects = new Set(listData.projects.map(p => p.id));
      if (input.checked) selectedProjects.add(project.id); else selectedProjects.delete(project.id);
      updateProjectSummary(); renderList();
    };
    label.append(input, name); el('project-options').append(label);
  }
  if (!el('project-options').childElementCount) el('project-options').append(node('p', 'No projects.'));
  updateProjectSummary();
}
function renderList() {
  const pane = el('work-list'); pane.replaceChildren();
  if (!listData) return;
  el('capacity').textContent = `Deep work active: ${listData.deep_work.active}/${listData.deep_work.limit}`;
  const table = node('table',undefined,'desktop-work-list'); table.setAttribute('aria-label', 'Work items');
  const cards = node('div',undefined,'mobile-work-list');cards.setAttribute('aria-label','Work items');
  const head = node('thead'), headings = node('tr');
  for (const title of ['Item', 'Deep', 'Labels', 'Repository', 'Actions']) headings.append(node('th', title));
  head.append(headings); table.append(head);
  const sections=[...(listData.display_sections||[])].sort((a,b)=>(a.name==='NEEDS ATTENTION'?-1:0)-(b.name==='NEEDS ATTENTION'?-1:0));
  let count = 0;
  for (const section of sections) {
    const rows = section.items.filter(row => selectedProjects === null || selectedProjects.has(row.project_id));
    if (!rows.length) continue;
    const sectionClass=section.name.toLowerCase().replaceAll(' ', '-');
    const body = node('tbody'), group = node('tr', undefined, `bucket ${sectionClass}`);
    const heading = node('th', section.name); heading.colSpan = 5; heading.scope = 'rowgroup'; heading.append(node('small', String(rows.length)));
    group.append(heading); body.append(group);
    const cardSection=node('section',undefined,`mobile-bucket ${sectionClass}`), cardHeading=node('h2',section.name);cardHeading.append(node('small',String(rows.length)));cardSection.append(cardHeading);
    for (const row of rows) {
      const projectName = listData.projects.find(project => project.id === row.project_id)?.name || 'Unknown project';
      const tr = node('tr', undefined, 'row'); tr.dataset.item = row.id;
      const open = button('', () => openChat(row.id), 'open'); open.title = row.title;
      open.append(node('span', row.title, 'item-name'));
      const name = node('td'); name.append(open);
      const deep = node('td', row.deep_work ? '●' : '', 'deep'); deep.setAttribute('aria-label', row.deep_work ? 'Deep work' : 'Not deep work');
      const tags = node('td'); tags.append(labels(row.labels, row.capacity_full));
      const repo = node('td', `${projectName}${row.repository_home ? ' (home)' : ''}`, 'repository');
      const actions = node('td'), settleButton = button('Settle', () => archive(row.id, row.title), 'settle-row');
      settleButton.disabled=row.agent?.status==='busy';settleButton.setAttribute('aria-label', `Settle ${row.title}`); actions.append(settleButton);
      tr.append(name, deep, tags, repo, actions); body.append(tr);

      const card=node('article',undefined,'item-card'), cardOpen=button('',()=>openChat(row.id),'card-open');cardOpen.setAttribute('aria-label',`Open ${row.title}`);
      const cardTitle=node('span',row.title,'card-title'), cardMeta=node('span',undefined,'card-meta');
      cardMeta.append(node('span',projectName,'card-project'));
      const cardSettle=button('Settle',()=>archive(row.id,row.title),'card-settle');cardSettle.disabled=row.agent?.status==='busy';cardSettle.setAttribute('aria-label',`Settle ${row.title}`);
      cardOpen.append(cardTitle,cardMeta);card.append(cardOpen,cardSettle);cardSection.append(card);count++;
    }
    table.append(body);cards.append(cardSection);
  }
  if (count) pane.append(table,cards);
  else pane.append(node('p', selectedProjects === null ? 'No active work items.' : 'No work items in the selected projects. Select all to see every project.', 'meta'));
}
function renderTicket(data) {
  const m = data.item, live = data.runtime.runtime;
  el('ticket-title').textContent = m.title;
  el('ticket-description').textContent = data.description || 'No description.';
  el('ticket-status').replaceChildren(node('span', m.state, 'badge'), statusBadge(data.status));
  const agentBusy=data.runtime.online && !data.idle_guard;
  el('archive').disabled=m.state==='archived' || agentBusy;
  el('shelve').disabled=!['working','waiting'].includes(m.state) || agentBusy;
  el('ticket-labels').replaceChildren(labels(m.labels));
  if (!m.labels?.length) el('ticket-labels').append(node('span', 'No labels', 'meta'));
  const info = el('ticket-info'); info.replaceChildren();
  const field = (name, value, code=false) => {
    info.append(node('dt', name), node('dd', value || 'None', code ? 'code' : ''));
  };
  field('Project', data.project.name);
  if (m.repository.remote_url) field('Origin', m.repository.remote_url, true);
  field('Item', m.id, true);
  if (m.slug) field('Slug', m.slug, true);
  field('Branch', m.checkout.specification.branch, true);
  field('Checkout', m.checkout.specification.kind);
  field('Workspace', m.checkout.claim?.path || 'Not materialized', true);
  field('Deep work', m.deep_work ? 'Yes' : 'No');
  field('Agent', m.agent);
  field('Runtime', live ? `${live.mode} · ${live.id}` : 'No runtime', true);
  if (m.primary_conversation) field('Conversation', m.primary_conversation.id, true);
  field('Created', new Date(m.created_at).toLocaleString());
  field('Updated', new Date(m.updated_at).toLocaleString());
  field('Created from', m.repository.created_from_commit, true);
}
function validateNewRepository() {
  const missing=!el('new-project').value;
  el('new-project').setAttribute('aria-invalid',String(missing));
  el('new-project-error').hidden=!missing;
  if(missing)el('new-project').focus();
  return !missing;
}
function updateNewForm() {
  if(el('new-project').value)validateNewRepository();
  for (const id of ['new-project', 'new-agent', 'new-prompt']) el(id).disabled = creating || creationUnknown;
  el('create').disabled = creating || creationUnknown || !newProjects.length || !newAgents.length || !el('new-project').value || !el('new-agent').value || !el('new-prompt').value.trim();
  el('create').textContent = creating ? '…' : '↑';
}
function showUnknownCreation() {
  const result = el('creation-result');
  result.replaceChildren(node('p', 'The creation result is unknown. A ticket may already exist. Check the work list before trying again.'));
  result.append(button('I checked the list; allow retry', async () => {
    if (!confirm('A ticket may already exist. Have you checked the work list? Retrying could create a duplicate.')) return;
    creationUnknown = false; result.replaceChildren(); updateNewForm();
  }));
}
async function newPage(navigate=true) {
  const contextProject = lastChat?.project.id, previous = el('new-project').value, previousAgent=el('new-agent').value;
  stopChat(); setChatMode(false);setListMode(false);setNewMode(true); item=''; runtime=''; setLocation('/?new=1',navigate);
  el('items').hidden=true; el('chat').hidden=true; el('new-page').hidden=false;
  el('title').textContent=''; notice('');
  el('create').disabled=true; el('new-project').disabled=true;el('new-agent').disabled=true;
  const gen=generation;
  try {
    const [projects,agents]=await Promise.all([api('/api/projects'),api('/api/agents')]); if(gen!==generation)return;
    newProjects=projects;newAgents=agents;
    el('new-project').replaceChildren(node('option', 'Choose a repo'));
    el('new-project').firstChild.value='';
    for (const project of projects) { const option=node('option', project.name); option.value=project.id; el('new-project').append(option); }
    const scoped=projects.filter(p=>selectedProjects?.has(p.id));
    const hasDraft=el('new-prompt').value;
    const candidates=hasDraft && previous ? [previous] : [contextProject, scoped.length===1 ? scoped[0].id : '', previous, projects.length===1 ? projects[0].id : ''];
    el('new-project').value=candidates.find(id=>projects.some(p=>p.id===id)) || '';
    el('new-agent').replaceChildren(node('option','Choose an agent'));el('new-agent').firstChild.value='';
    for(const profile of agents){const option=node('option',profile.label);option.value=profile.name;el('new-agent').append(option);}
    el('new-agent').value=[previousAgent,agents.find(profile=>profile.name==='default')?.name,agents.length===1?agents[0].name:''].find(name=>agents.some(profile=>profile.name===name))||'';
    updateNewForm();
    if (!projects.length) notice('No registered repositories yet. Register one with wi repo add NAME --from SOURCE, then reopen New.');
    else if(!agents.length)notice('No configured agent profiles are available.');
    if (creationUnknown) showUnknownCreation();
    else el('new-prompt').focus();
  } catch(e) { if(gen===generation)notice(e.message); }
}
function notice(text) { el('notice').textContent = text || ''; }
async function api(path, body, signal) {
  const response = await fetch(path, {method: body === undefined ? 'GET' : 'POST', headers: {'Authorization': `Bearer ${token}`, ...(body === undefined ? {} : {'Content-Type':'application/json'})}, body: body === undefined ? undefined : JSON.stringify(body), signal});
  const value = await response.json();
  if (!response.ok) { const err = new Error([value.error, ...(value.warnings || [])].filter(Boolean).join('\n')); err.outcome = value; throw err; }
  return value;
}
function button(text, action, className) { const b = document.createElement('button'); b.textContent = text; b.className = className || ''; b.onclick = () => action().catch(e => notice(e.message)); return b; }
function stopChat() { lastChat=undefined; generation++; streaming?.abort(); streaming = undefined; el('chat-loading').hidden=true; clearTimeout(refreshTimer); if (item) drafts.set(item, el('message').value); }
function setLocation(url,navigate) { if(navigate && location.pathname+location.search!==url) history.pushState(null,'',url); }
async function dashboard(navigate=true) {
  stopChat(); setChatMode(false);setNewMode(false);setListMode(true); item = ''; runtime = ''; setLocation('/',navigate); el('chat').hidden = true; el('new-page').hidden = true; el('items').hidden = false; el('title').textContent = '';
  const gen = generation; const data = await api('/api/items'); if(gen !== generation) return;
  listData = data; notice((data.warnings || []).join('\n'));
  renderProjects(); renderList();
}
async function openChat(id,navigate=true) {
  clearTimeout(startedToastTimer);el('started-toast').hidden=true;
  stopChat();setNewMode(false);setChatMode(true);closeTicket();item=id;runtime='';lastHistory='';historyToolIDs=new Set();selectedImages=[];renderAttachments();el('message').value=drafts.get(id)||'';setLocation(`/?item=${encodeURIComponent(id)}`,navigate);
  el('items').hidden=true;el('new-page').hidden=true;el('chat').hidden=true;el('chat-loading').hidden=false;el('archive').disabled=true;el('shelve').disabled=true;el('title').textContent='Loading conversation';el('ticket-title').textContent='Loading ticket';el('ticket-description').textContent='';el('ticket-status').replaceChildren();el('ticket-info').replaceChildren();el('ticket-labels').replaceChildren();el('history').replaceChildren();el('live').textContent='';notice('');el('send').disabled=true;
  const gen=generation;await refresh(gen);
}
async function refresh(gen) {
  if(gen!==generation)return;
  try {
    const data=await api(`/api/items/${item}/chat`);if(gen!==generation)return;
    if(['queued','starting'].includes(data.startup?.state)) {
      el('chat-loading').hidden=false;el('chat').hidden=true;notice('');
      return;
    }
    el('chat-loading').hidden=true;el('chat').hidden=false;el('chat-menu').hidden=false;
    el('title').textContent=data.item.title;
    if(data.startup?.state==='problem')notice(data.startup.error);
    renderTicket(data);
    runtime=data.runtime.runtime?.id || '';
    el('owner').textContent=data.runtime.online ? `${data.status} · ${data.runtime.runtime.mode}` : (data.item.state==='archived' ? 'Archived · read only' : pendingLaunches.has(data.item.id) ? 'Starting agent…' : 'Agent offline');
    notice((data.warnings||[]).join('\n'));
    lastChat=data;
    updateComposer();
    el('message').disabled=data.item.state==='archived';
    updateComposer();
    const key=JSON.stringify(data.history);if(key!==lastHistory || itemOutbox().length){renderConversationHistory(data.history);lastHistory=key;}
    if(!streaming)stream(gen);
  } catch(e){if(gen===generation){el('chat-loading').hidden=true;notice(e.message);}}
  finally {if(gen===generation){clearTimeout(refreshTimer);refreshTimer=setTimeout(()=>refresh(gen),el('chat-loading').hidden?3000:500);}}
}
function renderLive(data) {
  const live=el('live');live.replaceChildren();
  if(data.error){const card=node('div',undefined,'live-card');card.append(node('div',data.error,'live-activity'));live.append(card);return;}
  const activity=String(data.activity||''), tool=data.tool;
  if(data.text){const card=node('div',undefined,'live-card');const markdown=renderMarkdown(data.text);markdown.className+=' live-cursor';card.append(node('div','Agent','role'),markdown);live.append(card);}
  if(tool && !(tool.status==='completed'&&tool.id&&historyToolIDs.has(tool.id))){const card=node('div',undefined,'live-card'), tools=node('div',undefined,'tools');tools.append(renderTool(tool));card.append(node('div','Agent','role'),tools);live.append(card);}
  else if(!data.text && activity && !['Idle','Message saved'].includes(activity)){const card=node('div',undefined,'live-card');card.append(node('div',activity,'live-activity'));live.append(card);}
  const pane=el('history');if(pane.scrollHeight-pane.scrollTop-pane.clientHeight<120)pane.scrollTop=pane.scrollHeight;
}
async function stream(gen) {
  const controller=new AbortController();streaming=controller;
  try {
    const response=await fetch(`/api/items/${item}/stream`,{headers:{Authorization:`Bearer ${token}`},signal:controller.signal});if(!response.ok)throw new Error('Live connection refused');
    const reader=response.body.getReader(), decoder=new TextDecoder();let buffer='';
    while(true){const {done,value}=await reader.read();if(done)break;buffer+=decoder.decode(value,{stream:true});let n;
      while((n=buffer.indexOf('\n'))>=0){const data=JSON.parse(buffer.slice(0,n));buffer=buffer.slice(n+1);if(gen!==generation)return;
        renderLive(data);
      }
    }
  } catch(e){if(e.name!=='AbortError'&&gen===generation)el('live').textContent=e.message;}
  if(gen===generation)setTimeout(()=>{if(gen===generation)stream(gen)},3000);
}
async function shelve(id,title) {
  closeChatMenu();
  if(!confirm(`Shelve ${title}? Wi will stop an idle agent and release its checkout. The item will return to the backlog.`))return;
  notice('Shelving…');
  try{const out=await api(`/api/items/${id}/shelve`,{});await dashboard();notice(['Shelved.',...(out.warnings||[])].join('\n'));}
  catch(e){notice(e.message);if(id===item)await refresh(generation);}
}
async function archive(id,title) {
  closeChatMenu();
  if(!confirm(`Settle ${title}? Wi will remove it from the active list, stop an idle agent, and safely release its checkout.`))return;
  notice('Settling…');
  try{const out=await api(`/api/items/${id}/archive`,{});await dashboard();notice(['Settled.',...(out.warnings||[])].join('\n'));}
  catch(e){notice(e.message);if(id===item)await refresh(generation);}
}
function renderAttachments(){const pane=el('attachments');pane.replaceChildren();for(const [index,value] of selectedImages.entries()){const attachment=node('div',undefined,'attachment'),image=safeImage(`data:${value.mime_type};base64,${value.data}`,value.name||'Attached image'),remove=button('×',async()=>{selectedImages.splice(index,1);renderAttachments();updateComposer();});remove.setAttribute('aria-label',`Remove ${value.name||'image'}`);attachment.append(image,remove);pane.append(attachment);}pane.hidden=!selectedImages.length;}
function readImage(file){return new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result).split(',')[1]||'');reader.onerror=()=>reject(new Error(`Could not read ${file.name}`));reader.readAsDataURL(file);});}
async function addImages(files){if(!lastChat?.runtime.capabilities.image_input)throw new Error('This agent driver does not support image input.');const allowed=new Set(['image/jpeg','image/png','image/gif','image/webp']);for(const file of files){if(selectedImages.length>=4)throw new Error('Attach at most four images.');if(!allowed.has(file.type))throw new Error(`${file.name} is not a supported image.`);if(selectedImages.reduce((sum,image)=>sum+image.bytes,0)+file.size>8<<20)throw new Error('Images exceed 8 MiB total.');selectedImages.push({mime_type:file.type,data:await readImage(file),name:file.name,bytes:file.size});}renderAttachments();updateComposer();}
function updateComposer() {
 const data=lastChat, mode=el('delivery').value, hasContent=!!el('message').value.trim()||selectedImages.length>0;
 const supported=mode==='prompt' ? data?.runtime.capabilities.prompt && data?.idle_guard : data?.runtime.capabilities.follow_up;
 el('send').disabled=pending || !hasContent || !data?.runtime.online || data?.item.state==='archived' || !supported;
 el('attach').hidden=!data?.runtime.capabilities.image_input;
 el('attach').disabled=pending || data?.item.state==='archived' || !data?.runtime.capabilities.image_input;
}
el('delivery').onchange=updateComposer;
el('message').oninput=()=>{el('message').style.height='auto';el('message').style.height=`${Math.min(el('message').scrollHeight,innerHeight*.24)}px`;updateComposer();};
el('message').onpaste=async event=>{const files=[...(event.clipboardData?.items||[])].filter(item=>item.kind==='file'&&item.type.startsWith('image/')).map(item=>item.getAsFile()).filter(Boolean);if(!files.length)return;event.preventDefault();try{await addImages(files);notice('');}catch(e){notice(e.message);}};
el('attach').onclick=()=>el('image-input').click();
el('image-input').onchange=async()=>{try{await addImages([...el('image-input').files]);notice('');}catch(e){notice(e.message);}finally{el('image-input').value='';}};
function submitOnEnter(inputID,formID,submitID,validate=()=>true) {
 el(inputID).onkeydown=event=>{
  if(event.key!=='Enter' || event.shiftKey || event.isComposing || event.keyCode===229)return;
  event.preventDefault();
  if(event.repeat || el(inputID).disabled || !validate() || el(submitID).disabled || !el(inputID).value.trim())return;
  el(formID).requestSubmit(el(submitID));
 };
}
submitOnEnter('message','composer','send');
submitOnEnter('new-prompt','new-form','create',validateNewRepository);
el('new-project').oninvalid=validateNewRepository;
el('composer').onsubmit=async event=>{
 event.preventDefault();if(pending||!item)return;const id=item, gen=generation, text=el('message').value.trim(), images=selectedImages.map(image=>({mime_type:image.mime_type,data:image.data,alt:image.name}));if(!text&&!images.length)return;
 const optimistic={client_id:`${Date.now()}-${Math.random().toString(16).slice(2)}`,text,images,status:'sending',after_id:lastChat?.history?.leaf_id||''};
 itemOutbox(id).push(optimistic);saveOutbox();drafts.delete(id);el('message').value='';selectedImages=[];renderAttachments();el('message').oninput?.();renderConversationHistory(lastChat?.history);pending=true;el('send').disabled=true;notice('');
 try{await api(`/api/items/${id}/messages`,{text,images:images.map(image=>({mime_type:image.mime_type,data:image.data})),delivery:el('delivery').value,runtime_id:runtime});optimistic.status='sent';saveOutbox();if(gen===generation)renderConversationHistory(lastChat?.history);}
 catch(e){optimistic.status=e.outcome?'failed':'unknown';saveOutbox();if(gen===generation){renderConversationHistory(lastChat?.history);notice(e.outcome?e.message:'The connection ended before delivery was confirmed. The message is still shown below; edit and retry only after checking the conversation.');}}
 finally{pending=false;if(gen===generation)await refresh(gen);}
};
el('new-prompt').oninput=updateNewForm;
el('new-project').onchange=updateNewForm;
el('new-agent').onchange=updateNewForm;
el('attention').onclick=async()=>{if(el('attention').disabled)return;try{const gen=generation,query=projectQuery();query.set('current',item);const next=await api(`/api/next?${query}`);if(gen!==generation)return;if(next.id)await openChat(next.id);else notice('Nothing needs attention.');}catch(e){notice(e.message);}};
el('new').onclick=()=>newPage().catch(e=>notice(e.message));
el('new-form').onsubmit=async event=>{
  event.preventDefault(); if(creating || creationUnknown || !validateNewRepository() || !el('new-form').reportValidity())return;
  const request={project_id:el('new-project').value,agent:el('new-agent').value,prompt:el('new-prompt').value.trim()};
  if(!request.project_id || !request.agent || !request.prompt)return;
  const gen=generation;creating=true;updateNewForm();notice('Sending…');
  let outcome, error;
  try { outcome=await api('/api/items',request); }
  catch(e) { error=e;outcome=e.outcome; }
  creating=false;updateNewForm();
  if(outcome?.id){
    pendingLaunches.set(outcome.id, Date.now());
    showStartedToast(outcome.id);
    if(el('new-prompt').value.trim()===request.prompt)el('new-prompt').value='';
    el('creation-result').replaceChildren();updateNewForm();
    if(gen===generation){notice('');el('new-prompt').focus();}
  } else if(error){
    if(outcome?.observed?.id){
      if(el('new-prompt').value.trim()===request.prompt)el('new-prompt').value='';
      updateNewForm();
      if(gen===generation){
        el('creation-result').replaceChildren(node('p','Saved, but startup needs attention.'),button('View conversation',()=>openChat(outcome.observed.id)));
        el('new-prompt').focus();
      }
    }
    if(!outcome || outcome.creation_uncertain){creationUnknown=true;updateNewForm();if(gen===generation)showUnknownCreation();}
    if(gen===generation)notice(error.message);
  } else {creationUnknown=true;updateNewForm();if(gen===generation)showUnknownCreation();}
};
el('list').onclick=()=>dashboard().catch(e=>notice(e.message));
el('projects-all').onclick=()=>{selectedProjects=null;updateProjectSummary();renderList();};
el('projects-none').onclick=()=>{selectedProjects=new Set();updateProjectSummary();renderList();};
el('menu-details').onclick=openTicket;
el('shelve').onclick=()=>shelve(item,el('title').textContent);
el('archive').onclick=()=>archive(item,el('title').textContent);
el('ticket-close').onclick=closeTicket;
el('ticket-backdrop').onclick=closeTicket;
// Background receipts and attention are cues, never automatic navigation.
async function pollBackground() {
  try {
    const [jobsResult, listResult]=await Promise.allSettled([api('/api/creations'),api('/api/items')]);
    const jobs=jobsResult.status==='fulfilled' ? jobsResult.value : [];
    for(const job of jobs)if(!['queued','starting'].includes(job.state))pendingLaunches.delete(job.id);
    for(const [id,started] of pendingLaunches)if(Date.now()-started>150000)pendingLaunches.delete(id);
    const observedList=listResult.status==='fulfilled' ? listResult.value : listData;
    const visibleRows=new Map();
    for(const section of observedList?.display_sections || [])for(const row of section.items)if(selectedProjects===null || selectedProjects.has(row.project_id))visibleRows.set(row.id,row);
    const problems=jobs.filter(job=>job.state==='problem' && (!observedList || visibleRows.has(job.id)));
    const key=JSON.stringify(problems);
    if(jobsResult.status==='fulfilled' && key!==lastLaunchErrors){
      lastLaunchErrors=key;el('launch-updates').replaceChildren();
      for(const job of problems.slice(0,3)){
        const row=node('div',undefined,'launch-problem');
        row.append(node('p',`${job.title}: ${job.error}`),button('View conversation',()=>openChat(job.id)));
        el('launch-updates').append(row);
      }
    }
    if(listResult.status==='fulfilled'){
      const data=listResult.value, pendingIDs=new Set([...pendingLaunches.keys(),...jobs.filter(job=>['queued','starting'].includes(job.state)).map(job=>job.id)]);
      const ids=new Set(problems.map(job=>job.id));
      for(const section of data.display_sections || []){
        if(!['NEEDS ATTENTION','NEEDS FIXING'].includes(section.name))continue;
        for(const row of section.items)if(!pendingIDs.has(row.id) && (selectedProjects===null || selectedProjects.has(row.project_id)))ids.add(row.id);
      }
      setAttention(ids.size);
      if(!el('items').hidden){listData=data;renderList();}
    }
  } catch(_) { /* Keep the last observation; do not interrupt the composer. */ }
  finally {setTimeout(pollBackground,5000);}
}
pollBackground();
function route() {
  const query=new URLSearchParams(location.search), id=query.get('item');
  return id ? openChat(id,false) : query.has('new') ? newPage(false) : dashboard(false);
}
window.addEventListener('popstate',()=>route().catch(e=>notice(e.message)));
route().catch(e=>notice(e.message));

'use strict';
const el = id => document.getElementById(id);
const hash = new URLSearchParams(location.hash.slice(1));
if (hash.get('token')) { sessionStorage.setItem('wi-token', hash.get('token')); history.replaceState(null, '', '/'); }
const token = sessionStorage.getItem('wi-token') || '';
// Each tab is its own control client: two open tabs never share a lease.
const clientID = (()=>{try{const saved=sessionStorage.getItem('wi-client');if(/^[a-f0-9-]{8,64}$/.test(saved||''))return saved;}catch(_){/* Storage may be unavailable. */}
  const bytes=new Uint8Array(16);crypto.getRandomValues(bytes);const value=[...bytes].map(b=>b.toString(16).padStart(2,'0')).join('');
  try{sessionStorage.setItem('wi-client',value);}catch(_){/* Keep an in-memory identity. */}return value;})();
let markedReadLeaf='';
let lastChat;
let item = '', runtime = '', generation = 0, streaming, refreshTimer, pending = false, lastHistory = '';
let historyToolIDs = new Set();
let followBottom = true; // Keep the newest output in view until the reader scrolls away from it.
let olderEntryIDs=[], olderHistory=[], olderCursor='', pagingHistory=false, historyBoundary='', historyAnchor='';
function mergeHistoryMessages(messages) {
  const merged=[], ids=new Set(), tools=new Map(), latest=new Map(messages.map(message=>[message.id,message]));
  for(const entry of messages){
    if(ids.has(entry.id))continue;ids.add(entry.id);
    const original=latest.get(entry.id);
    const message={...original,tools:(original.tools||[]).map(tool=>({...tool})),native_ids:[...(original.native_ids||[])]};
    if(!message.text && message.tools.length===1 && message.tools[0].status!=='running' && tools.has(message.tools[0].id)){
      const previous=tools.get(message.tools[0].id), tool=message.tools[0];
      Object.assign(previous.tool,{output:tool.output,images:tool.images,status:tool.status});
      previous.message.native_ids.push(message.id,...message.native_ids);continue;
    }
    merged.push(message);
    for(const tool of message.tools)if(tool.id)tools.set(tool.id,{tool,message});
  }
  return merged;
}
async function loadOlderHistory() {
  if(pagingHistory || !lastChat?.history?.has_more && !olderCursor)return;
  const id=item,gen=generation,leaf=lastChat.history.leaf_id,before=olderCursor||lastChat.history.before_id;
  pagingHistory=true;
  try {
    const page=await api(`/api/items/${id}/history?${new URLSearchParams({leaf,before})}`);
    if(gen!==generation)return;
    if(leaf!==lastChat.history.leaf_id && !lastChat.history.messages.some(message=>message.id===leaf || message.native_ids?.includes(leaf)))throw new Error('The conversation branch changed. Load older messages again.');
    olderEntryIDs=[...new Set([...(page.native_entry_ids||page.messages.map(message=>message.id)),...olderEntryIDs,...(lastChat.history.native_entry_ids||lastChat.history.messages.map(message=>message.id))])];
    olderHistory=mergeHistoryMessages([...page.messages,...olderHistory,...lastChat.history.messages]);olderCursor=page.has_more?page.before_id:'';
    historyBoundary=lastChat.history.before_id;historyAnchor=lastChat.history.leaf_id;
    renderConversationHistory(lastChat.history);
  }catch(error){if(gen===generation)notice(error.message);}
  finally{if(gen===generation)pagingHistory=false;}
}
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
  const matched=new Set();
  for(const pendingMessage of [...queued]){
    if(pendingMessage.status==='failed')continue;
    const messages=history.messages||[];
    const entries=history.native_entry_ids||messages.map(message=>message.id);
    const anchor=entries.indexOf(pendingMessage.after_id);
    if(pendingMessage.after_id && anchor<0)continue;
    const match=messages.find(message=>entries.indexOf(message.id)>anchor && !matched.has(message.id) && message.role==='user' && message.text===pendingMessage.text && (message.images?.length||0)===(pendingMessage.images?.length||0) && (pendingMessage.images||[]).every((image,index)=>image.data && image.data===message.images[index].data && image.mime_type===message.images[index].mime_type));
    if(match){
      matched.add(match.id);
      // Advance outstanding peers sharing this anchor so later polls cannot
      // reuse this same native message as another confirmation.
      for(const peer of outbox[item]||[])if(peer.client_id!==pendingMessage.client_id && peer.after_id===pendingMessage.after_id)peer.after_id=match.id;
      discardOutboxMessage(item,pendingMessage.client_id);
    }
  }
}
function renderConversationHistory(history=lastChat?.history) {
  if(!history)return;
  const combined={...history,native_entry_ids:[...new Set([...olderEntryIDs,...(history.native_entry_ids||history.messages.map(message=>message.id))])],messages:mergeHistoryMessages([...olderHistory,...history.messages])};
  reconcileOutbox(combined);
  const pane=el('history'), scroller=el('scroller'), bottom=followBottom||scroller.scrollHeight-scroller.scrollTop-scroller.clientHeight<80, pos=scroller.scrollTop;pane.replaceChildren();historyToolIDs=new Set();
  for(const message of combined.messages){pane.append(renderMessage(message));for(const tool of message.tools||[])if(tool.status==='completed'&&tool.id)historyToolIDs.add(tool.id);}
  for(const message of itemOutbox())pane.append(renderMessage({...message,role:'user',pending_status:message.status}));
  if(olderCursor || history.has_more && !historyBoundary)pane.prepend(button(pagingHistory?'Loading…':'Load older messages',loadOlderHistory,'load-older'));
  if(history.truncated && !history.has_more)pane.prepend(node('p','Oversized content in this page was shortened.','meta'));
  if(!pane.childElementCount)pane.append(node('p','No messages yet.','meta'));
  scroller.scrollTop=bottom||!lastHistory?scroller.scrollHeight:pos;
}
function closeChatMenu(){el('chat-menu').open=false;}
function setListMode(active){document.body?.classList?.toggle('list-mode',active);}
function setNewMode(active){document.body?.classList?.toggle('new-mode',active);}
function setChatMode(active){document.body?.classList?.toggle('chat-mode',active);if(active){setListMode(false);setNewMode(false);}el('chat-menu').hidden=!active;if(!active){closeTicket();closeChatMenu();}}
function openTicket(){closeChatMenu();el('ticket').className='open';el('ticket').setAttribute('aria-hidden','false');el('menu-details').setAttribute('aria-expanded','true');el('ticket-backdrop').hidden=false;}
function closeTicket(){el('ticket').className='';el('ticket').setAttribute('aria-hidden','true');el('menu-details').setAttribute('aria-expanded','false');el('ticket-backdrop').hidden=true;}
function statusBadge(status) { return node('span', status || 'unknown', `badge status-${status || 'unknown'}`); }
function blockerText(blocker){if(blocker.kind==='time'&&blocker.until)return `until ${new Date(blocker.until).toLocaleString()}`;if(blocker.kind==='item')return `on ${blockerItemName(blocker.item_id)}`;return blocker.event||blocker.kind;}
function blockerShort(blocker){return blocker.label||blockerText(blocker);}
function blockerItemName(id){for(const section of listData?.display_sections||[])for(const row of section.items)if(row.id===id)return row.title;return id.slice(0,10);}
function labels(values, capacityFull=false) {
  const container = node('div');
  if (capacityFull) container.append(node('span', 'capacity full', 'label capacity-full'));
  for (const value of values || []) container.append(node('span', value, 'label'));
  return container;
}
// What an item waits for is lifecycle context, not a label: show it as muted
// text under the title.
function waitingLine(row){return row.blockers?.length ? node('span', `waiting · ${row.blockers.map(blockerShort).join(', ')}`, 'item-waiting') : null;}
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
      const tr = node('tr', undefined, row.unread ? 'row unread' : 'row'); tr.dataset.item = row.id;
      const open = button('', () => openChat(row.id), 'open'); open.title = row.title;
      open.append(node('span', row.title, 'item-name'));
      const waitingRow=waitingLine(row);if(waitingRow)open.append(waitingRow);
      const name = node('td'); name.append(open);
      const deep = node('td', row.deep_work ? '●' : '', 'deep'); deep.setAttribute('aria-label', row.deep_work ? 'Deep work' : 'Not deep work');
      const tags = node('td'); tags.append(labels(row.labels, row.capacity_full));
      const repo = node('td', `${projectName}${row.repository_home ? ' (home)' : ''}`, 'repository');
      const actions = node('td'), settleButton = button('Close', () => closeItem(row.id, row.title), 'settle-row');
      settleButton.disabled=row.agent?.status==='busy';settleButton.setAttribute('aria-label', `Close ${row.title}`); actions.append(settleButton);
      tr.append(name, deep, tags, repo, actions); body.append(tr);

      const card=node('article',undefined,row.unread?'item-card unread':'item-card'), cardOpen=button('',()=>openChat(row.id),'card-open');cardOpen.setAttribute('aria-label',`Open ${row.title}`);
      const cardTitle=node('span',row.title,'card-title'), cardMeta=node('span',undefined,'card-meta');
      cardMeta.append(node('span',projectName,'card-project'));
      const waitingCard=waitingLine(row);if(waitingCard)cardMeta.append(waitingCard);
      const cardSettle=button('Close',()=>closeItem(row.id,row.title),'card-settle');cardSettle.disabled=row.agent?.status==='busy';cardSettle.setAttribute('aria-label',`Close ${row.title}`);
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
  const agentBusy=data.runtime.online && data.status==='busy';
  el('close-completed').disabled=m.state==='closed' || agentBusy;
  el('close-abandoned').disabled=m.state==='closed' || agentBusy;
  el('shelve').disabled=m.state!=='active' || agentBusy;
  el('mark-unread').disabled=m.state!=='active';
  el('ticket-labels').replaceChildren(labels(m.labels));
  renderBlockers(m);
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
  if (m.resolution) field('Resolution', m.resolution);
  field('Agent', m.agent);
  field('Runtime', live ? `${live.mode} · ${live.id}` : 'No runtime', true);
  if (m.primary_conversation) field('Conversation', m.primary_conversation.id, true);
  field('Created', new Date(m.created_at).toLocaleString());
  field('Updated', new Date(m.updated_at).toLocaleString());
  field('Created from', m.repository.created_from_commit, true);
}
function renderBlockers(m) {
  const list=el('ticket-blockers');list.replaceChildren();
  for(const blocker of m.blockers||[]){
    const entry=node('li',undefined,'blocker'), clear=button('Clear',()=>clearBlocker(m.id,blocker.id),'clear-blocker');
    clear.setAttribute('aria-label',`Clear blocker ${blockerText(blocker)}`);
    entry.append(node('span',blocker.label?`${blocker.label} · ${blockerText(blocker)}`:blockerText(blocker)),clear);list.append(entry);
  }
  if(!m.blockers?.length)list.append(node('li','Not waiting','meta'));
  el('block-form').hidden=m.state==='closed';
  const items=el('block-item');items.replaceChildren();
  for(const section of listData?.display_sections||[])for(const row of section.items)if(row.id!==m.id){const option=node('option',row.title);option.value=row.id;items.append(option);}
}
async function clearBlocker(id,blockerID){
  try{await api(`/api/items/${id}/clear-blocker`,{blocker_id:blockerID});notice('Stopped waiting on that.');}
  catch(e){notice(e.message);}
  if(id===item)await refresh(generation);
}
el('block-kind').onchange=async()=>{
  if(el('block-kind').value==='item' && !listData){try{listData=await api('/api/items');if(lastChat)renderBlockers(lastChat.item);}catch(e){notice(e.message);}}
  el('block-item').hidden=el('block-kind').value!=='item';el('block-event').hidden=el('block-kind').value!=='external';};
el('block-form').onsubmit=async event=>{
  event.preventDefault();if(!item)return;
  const kind=el('block-kind').value, eventName=el('block-event').value.trim();
  const request=kind==='external'?{kind,event:eventName}:kind==='item'?{kind,item_id:el('block-item').value}:{kind:'time',duration:kind};
  if(kind==='external'&&!eventName){notice('Name the event the item waits for.');return;}
  if(kind==='item'&&!request.item_id){notice('Choose the blocking item.');return;}
  try{await api(`/api/items/${item}/blockers`,request);el('block-event').value='';notice('Waiting.');}
  catch(e){notice(e.message);}
  await refresh(generation);
};
function controlSummary(control){
  if(!control)return '';
  const status=control.status, holder=status.holder;
  if(control.you)return 'You control this agent';
  if(holder==='terminal')return 'Controlled in a terminal · this view is read-only';
  if(holder==='web')return 'Controlled in another browser tab · this view is read-only';
  return '';
}
function renderControl(data){
  const control=data.control, bar=el('control-bar');
  const text=controlSummary(control), other=control && !control.you && control.status.holder;
  bar.hidden=!other;el('control-text').textContent=text;
  return text;
}
async function takeControl(confirmed){
  const plan=lastChat?.control?.plan;
  if(!confirmed && plan?.needs_confirmation && !confirm(['Take control of this agent?',...(plan.reasons||[]).map(reason=>`• ${reason}`)].join('\n')))return false;
  notice('Taking control…');
  await api(`/api/items/${item}/control`,{confirm:true});
  notice('');await refresh(generation);return true;
}
el('take-control').onclick=()=>takeControl(false).catch(e=>notice(e.message));
el('mark-unread').onclick=async()=>{closeChatMenu();try{await api(`/api/items/${item}/read`,{unread:true});markedReadLeaf=lastChat?.history?.leaf_id||'';notice('Marked unread.');}catch(e){notice(e.message);}};
function maybeMarkRead(data){
  // Opening a conversation counts as reading it; a new response is read once
  // it arrives while the reader follows the bottom of a visible chat.
  if(!data.unread || data.item.state!=='active' || document.visibilityState!=='visible' || !followBottom)return;
  const leaf=data.history?.leaf_id||'';if(leaf && leaf===markedReadLeaf)return;
  markedReadLeaf=leaf;api(`/api/items/${data.item.id}/read`,{unread:false}).catch(()=>{});
}
function releaseControl(){if(item && lastChat?.control?.you)api(`/api/items/${item}/release`,{}).catch(()=>{});}
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
  const response = await fetch(path, {method: body === undefined ? 'GET' : 'POST', headers: {'Authorization': `Bearer ${token}`, 'X-Wi-Client': clientID, ...(body === undefined ? {} : {'Content-Type':'application/json'})}, body: body === undefined ? undefined : JSON.stringify(body), signal});
  const value = await response.json();
  if (!response.ok) { const err = new Error([value.error, ...(value.warnings || [])].filter(Boolean).join('\n')); err.outcome = value; throw err; }
  return value;
}
function button(text, action, className) { const b = document.createElement('button'); b.textContent = text; b.className = className || ''; b.onclick = () => action().catch(e => notice(e.message)); return b; }
function stopChat() { lastChat=undefined; generation++; streaming?.abort(); streaming = undefined; el('chat-loading').hidden=true; clearTimeout(refreshTimer); if (item) drafts.set(item, el('message').value); }
function setLocation(url,navigate) { if(navigate && location.pathname+location.search!==url) history.pushState(null,'',url); }
async function dashboard(navigate=true) {
  releaseControl();stopChat(); setChatMode(false);setNewMode(false);setListMode(true); item = ''; runtime = ''; setLocation('/',navigate); el('chat').hidden = true; el('new-page').hidden = true; el('items').hidden = false; el('title').textContent = '';
  const gen = generation; const data = await api('/api/items'); if(gen !== generation) return;
  listData = data; notice((data.warnings || []).join('\n'));
  renderProjects(); renderList();
}
async function openChat(id,navigate=true) {
  clearTimeout(startedToastTimer);el('started-toast').hidden=true;
  if(id!==item)releaseControl();markedReadLeaf='';
  stopChat();setNewMode(false);setChatMode(true);closeTicket();olderEntryIDs=[];olderHistory=[];olderCursor='';historyBoundary='';historyAnchor='';pagingHistory=false;followBottom=true;item=id;runtime='';lastHistory='';historyToolIDs=new Set();selectedImages=[];renderAttachments();el('message').value=drafts.get(id)||'';setLocation(`/?item=${encodeURIComponent(id)}`,navigate);
  el('items').hidden=true;el('new-page').hidden=true;el('chat').hidden=true;el('chat-loading').hidden=false;el('close-completed').disabled=true;el('close-abandoned').disabled=true;el('shelve').disabled=true;el('control-bar').hidden=true;el('title').textContent='Loading conversation';el('ticket-title').textContent='Loading ticket';el('ticket-description').textContent='';el('ticket-status').replaceChildren();el('ticket-info').replaceChildren();el('ticket-labels').replaceChildren();el('history').replaceChildren();el('live').textContent='';notice('');el('send').disabled=true;
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
    renderTicket(data);
    runtime=data.runtime.runtime?.id || '';
    const control=renderControl(data);
    el('owner').textContent=data.runtime.online ? [`${data.status} · ${data.runtime.runtime.mode}`,control].filter(Boolean).join(' · ') : (data.item.state==='closed' ? `Closed${data.item.resolution?` (${data.item.resolution})`:''} · read only` : data.item.state==='backlog' ? 'Backlog · start it to talk to its agent' : pendingLaunches.has(data.item.id) ? 'Starting agent…' : 'Agent offline · sending starts it headless');
    notice([data.startup?.state==='problem'?data.startup.error:'',...(data.warnings||[])].filter(Boolean).join('\n'));
    lastChat=data;
    updateComposer();
    el('message').disabled=data.item.state!=='active';
    updateComposer();
    maybeMarkRead(data);
    if(olderHistory.length && historyAnchor!==data.history.leaf_id && !data.history.messages.some(message=>message.id===historyAnchor || message.native_ids?.includes(historyAnchor))){olderEntryIDs=[];olderHistory=[];olderCursor='';historyBoundary='';historyAnchor='';}
    if(olderHistory.length)historyAnchor=data.history.leaf_id;
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
  const scroller=el('scroller');if(followBottom||scroller.scrollHeight-scroller.scrollTop-scroller.clientHeight<120)scroller.scrollTop=scroller.scrollHeight;
}
async function stream(gen) {
  const controller=new AbortController();streaming=controller;
  try {
    const response=await fetch(`/api/items/${item}/stream`,{headers:{Authorization:`Bearer ${token}`,'X-Wi-Client':clientID},signal:controller.signal});if(!response.ok)throw new Error('Live connection refused');
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
async function closeItem(id,title,resolution='completed') {
  closeChatMenu();
  if(!confirm(`Close ${title} as ${resolution}? Wi will stop an idle agent and safely release its checkout. The conversation stays readable.`))return;
  notice('Closing…');
  try{const out=await api(`/api/items/${id}/close`,{resolution});await dashboard();notice([`Closed as ${resolution}.`,...(out.warnings||[])].join('\n'));}
  catch(e){notice(e.message);if(id===item)await refresh(generation);}
}
function renderAttachments(){const pane=el('attachments');pane.replaceChildren();for(const [index,value] of selectedImages.entries()){const attachment=node('div',undefined,'attachment'),image=safeImage(`data:${value.mime_type};base64,${value.data}`,value.name||'Attached image'),remove=button('×',async()=>{selectedImages.splice(index,1);renderAttachments();updateComposer();});remove.setAttribute('aria-label',`Remove ${value.name||'image'}`);attachment.append(image,remove);pane.append(attachment);}pane.hidden=!selectedImages.length;}
function readImage(file){return new Promise((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(String(reader.result).split(',')[1]||'');reader.onerror=()=>reject(new Error(`Could not read ${file.name}`));reader.readAsDataURL(file);});}
async function addImages(files){if(!lastChat?.runtime.capabilities.image_input)throw new Error('This agent driver does not support image input.');const allowed=new Set(['image/jpeg','image/png','image/gif','image/webp']);for(const file of files){if(selectedImages.length>=4)throw new Error('Attach at most four images.');if(!allowed.has(file.type))throw new Error(`${file.name} is not a supported image.`);if(selectedImages.reduce((sum,image)=>sum+image.bytes,0)+file.size>8<<20)throw new Error('Images exceed 8 MiB total.');selectedImages.push({mime_type:file.type,data:await readImage(file),name:file.name,bytes:file.size});}renderAttachments();updateComposer();}
function updateComposer() {
 const data=lastChat, mode=el('delivery').value, hasContent=!!el('message').value.trim()||selectedImages.length>0;
 // Sending takes control: an offline or terminal-only runtime is restarted
 // headless, so only an online runtime limits the delivery modes.
 const online=data?.runtime.online, structured=data?.control?.status?.channels?.control==='structured';
 const supported=!online || !structured || (mode==='prompt' ? data?.runtime.capabilities.prompt : data?.runtime.capabilities.follow_up);
 el('send').disabled=pending || !hasContent || data?.item.state!=='active' || !supported;
 el('attach').hidden=!data?.runtime.capabilities.image_input;
 el('attach').disabled=pending || data?.item.state!=='active' || !data?.runtime.capabilities.image_input;
}
el('delivery').onchange=updateComposer;
el('scroller').onscroll=()=>{const s=el('scroller');followBottom=s.scrollHeight-s.scrollTop-s.clientHeight<120;};
el('message').oninput=()=>{el('message').style.height='auto';el('message').style.height=`${Math.min(el('message').scrollHeight,innerHeight*.24)}px`;updateComposer();};
el('message').onpaste=async event=>{const files=[...(event.clipboardData?.items||[])].filter(item=>item.kind==='file'&&item.type.startsWith('image/')).map(item=>item.getAsFile()).filter(Boolean);if(!files.length)return;event.preventDefault();try{await addImages(files);notice('');}catch(e){notice(e.message);}};
el('attach').onclick=()=>el('image-input').click();
el('image-input').onchange=async()=>{try{await addImages([...el('image-input').files]);notice('');}catch(e){notice(e.message);}finally{el('image-input').value='';}};
function submitOnEnter(inputID,formID,submitID,validate=()=>true) {
 el(inputID).onkeydown=event=>{
  if(event.key!=='Enter' || event.shiftKey || event.isComposing || event.keyCode===229)return;
  event.preventDefault();
  if(event.repeat || el(inputID).disabled || !validate() || el(submitID).disabled || (!el(inputID).value.trim() && !(inputID==='message' && selectedImages.length)))return;
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
 const request={text,images:images.map(image=>({mime_type:image.mime_type,data:image.data})),delivery:el('delivery').value,runtime_id:runtime};
 try{
  try{await api(`/api/items/${id}/messages`,request);}
  catch(e){
   // Another client controls the agent: confirm the takeover, then resend.
   const plan=e.outcome?.control?.plan;if(!plan?.needs_confirmation)throw e;
   if(!confirm(['Take control of this agent and send?',...(plan.reasons||[]).map(reason=>`• ${reason}`)].join('\n')))throw e;
   const taken=await api(`/api/items/${id}/control`,{confirm:true});request.runtime_id=taken.result?.runtime?.id||'';
   await api(`/api/items/${id}/messages`,request);
  }
  optimistic.status='sent';saveOutbox();if(gen===generation)renderConversationHistory(lastChat?.history);}
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
el('close-completed').onclick=()=>closeItem(item,el('title').textContent);
el('close-abandoned').onclick=()=>closeItem(item,el('title').textContent,'abandoned');
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

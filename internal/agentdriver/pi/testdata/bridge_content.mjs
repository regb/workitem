import bridge from './bridge.ts';
import {createServer,createConnection} from 'node:net';
import assert from 'node:assert/strict';
const sent=[];
const callbacks = new Map(); let idle = true; let shutDown = false; let leaf = 'branch-leaf';
const eventPath = process.env.WI_AGENT_EVENT_SOCKET;
const relay=createServer(socket=>{let data='';socket.on('data',chunk=>{data+=chunk;if(data.includes('\n')) socket.end('{"accepted":true}\n');});});
await new Promise(resolve=>relay.listen(eventPath,resolve));
const ctx={isIdle:()=>idle,hasPendingMessages:()=>false,shutdown:()=>{shutDown=true},sessionManager:{buildSessionProjection:()=>({messages:[]}),getLeafId:()=>leaf,getSessionId:()=> 'session',getSessionFile:()=> 'native'},ui:{notify:()=>{}}};
bridge({on:(name,fn)=>callbacks.set(name,fn),sendUserMessage:(content,options)=>sent.push({content,options}),events:{},getThinkingLevel:()=>"medium"});
await callbacks.get('session_start')({},ctx);
await new Promise(resolve=>setTimeout(resolve,60));
async function exchange(path,value){return await new Promise((resolve,reject)=>{const socket=createConnection(path);let data='';socket.on('error',reject);socket.on('connect',()=>socket.write(JSON.stringify(value)+'\n'));socket.on('data',chunk=>{data+=chunk;if(data.includes('\n')){socket.destroy();resolve(JSON.parse(data.split('\n')[0]));}});});}
const request={protocol_version:1,runtime_id:'owner',work_item_id:'item'};
const contentRequest={...request,protocol_version:2};
callbacks.get('message_start')({message:{role:'assistant',content:[]}},ctx);
callbacks.get('message_update')({assistantMessageEvent:{type:'text_delta',delta:'hello 🌍',partial:{}}},ctx);
const path=process.env.WI_CONTENT_TEST_SOCKET;
let value=await exchange(path,contentRequest);assert.deepEqual(value.parts,[{text:'hello 🌍'}]);assert.equal(value.leaf_id,'branch-leaf');
value=await exchange(path,contentRequest);assert.deepEqual(value.parts,[{text:'hello 🌍'}]);
value=await exchange(path,{...contentRequest,runtime_id:'superseded'});assert.equal(value.error,'content owner mismatch');
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'send-first',type:'prompt',message:'work',require_idle:true});assert.equal(value.accepted,true);
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'shutdown-before-start',type:'shutdown',require_idle:true});assert.equal(value.accepted,false);assert.equal(shutDown,false);
callbacks.get('agent_start')({},ctx);callbacks.get('agent_settled')({},ctx);
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'image-only',type:'prompt',images:[{mime_type:'image/png',data:'iVBORw0KGgo='}],require_idle:true});assert.equal(value.accepted,true);
assert.deepEqual(sent.at(-1).content,[{type:'image',mimeType:'image/png',data:'iVBORw0KGgo='}]);
callbacks.get('agent_start')({},ctx);callbacks.get('agent_settled')({},ctx);
const largeImage=Buffer.alloc(8*1024*1024,1).toString('base64');
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'large-image',type:'prompt',images:[{mime_type:'image/png',data:largeImage}],require_idle:true});assert.equal(value.accepted,true);
assert.equal(sent.at(-1).content[0].data,largeImage);
callbacks.get('agent_start')({},ctx);callbacks.get('agent_settled')({},ctx);
const sentBeforeInvalid=sent.length;
for(const [id,images] of [
  ['invalid-image',[{mime_type:'image/png',data:'YR=='}]],
  ['empty-image',[{mime_type:'image/png',data:''}]],
  ['unsupported-image',[{mime_type:'image/svg+xml',data:'YWJj'}]],
  ['overflow-image',[{mime_type:'image/png',data:largeImage},{mime_type:'image/png',data:'YWJj'}]],
  ['too-many-images',Array(5).fill({mime_type:'image/png',data:'YWJj'})],
]){
  value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id,type:'prompt',images,require_idle:true});assert.equal(value.accepted,false);
}
assert.equal(sent.length,sentBeforeInvalid);
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'idle-after-invalid',type:'shutdown',require_idle:true});assert.equal(value.accepted,true);assert.equal(shutDown,true);shutDown=false;
idle=false;
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'queued-image',type:'follow_up',message:'inspect this',images:[{mime_type:'image/jpeg',data:'YWJj'}]});assert.equal(value.accepted,true);
assert.deepEqual(sent.at(-1),{content:[{type:'text',text:'inspect this'},{type:'image',mimeType:'image/jpeg',data:'YWJj'}],options:{deliverAs:'followUp'}});
value=await exchange(process.env.WI_AGENT_CONTROL_SOCKET,{...request,id:'shutdown-test',type:'shutdown',require_idle:true});assert.equal(value.accepted,false);assert.equal(shutDown,false);
callbacks.get('message_end')({message:{role:'assistant',content:[{type:'text',text:'saved answer'}],stopReason:'stop'}},ctx);
value=await exchange(path,contentRequest);assert.equal(value.activity,'Message saved');
leaf='other-leaf';callbacks.get('session_tree')({oldLeafId:'branch-leaf',newLeafId:leaf},ctx);value=await exchange(path,contentRequest);assert.equal(value.leaf_id,leaf);
callbacks.get('session_shutdown')({},ctx);
await new Promise(resolve=>setTimeout(resolve,60));relay.close();process.exit(0);

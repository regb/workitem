import bridge from './bridge.ts';
import assert from 'node:assert/strict';
import {createServer} from 'node:net';
const events=[];const settings=[];const hooks=new Map();
const server=createServer(socket=>{let input='';socket.setEncoding('utf8');socket.on('data',chunk=>{input+=chunk;if(input.includes('\n')){const event=JSON.parse(input.split('\n')[0]);if(event.type==='settings.changed')settings.push(event.settings);else if(event.type!=='runtime.status')events.push(event);socket.end(JSON.stringify({accepted:true})+'\n')}})});
await new Promise(resolve=>server.listen(process.env.WI_AGENT_EVENT_SOCKET,resolve));
bridge({on:(name,handler)=>hooks.set(name,handler),registerCommand:()=>{},getThinkingLevel:()=>"medium"});
const ctx={model:{provider:'openai',id:'gpt'},isIdle:()=>false,hasPendingMessages:()=>false,sessionManager:{getLeafId:()=> 'leaf',buildSessionProjection:()=>({messages:[]}),getSessionId:()=> 'session',getSessionFile:()=>undefined}};
const fire=async(name,event={})=>{assert.ok(hooks.has(name),name);await hooks.get(name)(event,ctx)};
const delivered=async count=>{for(let i=0;i<300 && events.length<count;i++) await new Promise(resolve=>setTimeout(resolve,10));assert.ok(events.length>=count,JSON.stringify(events))};
await fire('session_start');await fire('agent_start');await fire('turn_start');
await fire('message_end',{message:{role:'assistant',content:[],stopReason:'error'}});
await fire('turn_end',{turnIndex:0,message:{role:'assistant',stopReason:'error'},toolResults:[]});
await fire('agent_end',{messages:[]});await fire('auto_retry_start',{attempt:1});await delivered(5);
assert.equal(events.filter(x=>x.type==='agent.settled').length,0);
await fire('agent_start');await fire('turn_start');
await fire('tool_execution_end',{toolCallId:'failed',toolName:'bash',result:{},isError:true});
await fire('message_end',{message:{role:'assistant',content:[],stopReason:'stop'}});
await fire('turn_end',{turnIndex:1,message:{role:'assistant',stopReason:'stop'},toolResults:[]});
await fire('compaction_start');await fire('compaction_end');await fire('agent_before_settle',{outcome:'completed'});await fire('agent_settled');await delivered(11);
assert.equal(events.at(-1).type,'agent.settled');assert.equal(events.at(-1).failed,false);assert.equal(events.at(-1).outcome,'completed');
await fire('agent_start');await fire('message_end',{message:{role:'assistant',content:[],stopReason:'error'}});await fire('turn_end',{turnIndex:2,message:{role:'assistant',stopReason:'error'},toolResults:[]});await fire('auto_retry_end',{success:false});await fire('agent_before_settle',{outcome:'error'});await fire('agent_settled');await delivered(15);
assert.equal(events.at(-1).type,'agent.settled');assert.equal(events.at(-1).failed,true);assert.equal(events.at(-1).outcome,'failed');
for(const event of events){assert.equal(event.backend_event,undefined);assert.equal(event.data,undefined);assert.equal(event.error,undefined)}
await fire('model_select');await fire('thinking_level_select');
await fire('session_shutdown');await delivered(16);await new Promise(resolve=>server.close(resolve));
// The session's settings are reported once; an unchanged selection is not.
assert.deepEqual(settings,[{reasoning:'medium',model:'openai/gpt'}]);

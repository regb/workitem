import bridge from './bridge.ts';
import {createServer} from 'node:net';
import {createInterface} from 'node:readline';
const callbacks = new Map(); let leaf = 'leaf-1';
const relay=createServer(socket=>{let data='';socket.on('data',chunk=>{data+=chunk;if(data.includes('\n')) socket.end('{"accepted":true}\n');});});
await new Promise(resolve=>relay.listen(process.env.WI_AGENT_EVENT_SOCKET,resolve));
const ctx={isIdle:()=>true,hasPendingMessages:()=>false,shutdown:()=>{},sessionManager:{getBranch:()=>[{type:'message',id:'entry-9',message:{role:'assistant',timestamp:123}}],getLeafId:()=>leaf,buildSessionProjection:()=>({messages:[]}),getSessionId:()=> 'session',getSessionFile:()=> 'native'},ui:{notify:()=>{}}};
bridge({on:(name,fn)=>callbacks.set(name,fn),sendUserMessage:()=>{},events:{},getThinkingLevel:()=>"medium"});
await callbacks.get('session_start')({},ctx);
await new Promise(resolve=>setTimeout(resolve,60));
console.log('ready');
const steps={
  write:()=>{callbacks.get('message_start')({message:{role:'assistant',content:[]}},ctx);
    for (const delta of ['Hel','lo ']) callbacks.get('message_update')({assistantMessageEvent:{type:'text_delta',delta,partial:{}}},ctx);
    callbacks.get('message_update')({assistantMessageEvent:{type:'thinking_delta',delta:'pondering',partial:{}}},ctx);
    callbacks.get('tool_execution_start')({toolCallId:'t1',toolName:'bash',args:{command:'ls'}},ctx);
    callbacks.get('tool_execution_start')({toolCallId:'t2',toolName:'read',args:{path:'a'}},ctx);
    callbacks.get('message_update')({assistantMessageEvent:{type:'text_delta',delta:'🌍',partial:{}}},ctx);
    callbacks.get('tool_execution_end')({toolCallId:'t1',toolName:'bash',result:{content:[{type:'text',text:'a'}]},isError:false},ctx);},
  save:async()=>{callbacks.get('message_end')({message:{role:'assistant',content:[{type:'text',text:'Hello 🌍'}],stopReason:'stop',timestamp:123}},ctx);
    leaf='leaf-2';callbacks.get('session_tree')({oldLeafId:'leaf-1',newLeafId:leaf},ctx);
    callbacks.get('agent_settled')({},ctx);
    await new Promise(resolve=>setTimeout(resolve,20));},
  stop:async()=>{callbacks.get('session_shutdown')({},ctx);await new Promise(resolve=>setTimeout(resolve,60));relay.close();process.exit(0);},
};
for await (const line of createInterface({input:process.stdin})) { await steps[line](); console.log('done '+line); }

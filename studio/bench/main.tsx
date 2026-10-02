// Isolated synthetic fixture: all API and stream traffic is local to this page.
import React, {Profiler, useState} from 'react';
import {createRoot} from 'react-dom/client';
import {flushSync} from 'react-dom';
import {LazyMotion, domAnimation, domMax, MotionConfig} from 'framer-motion';
import WorkspaceApp from '../src/App';
import ChatRoom from '../src/features/chat/ChatRoom';
import '../src/styles.css';
const bot={id:'perf',name:'Performance fixture',role:'Synthetic benchmark',backend:'codex',model:'test',effort:'max',avatar:'slate',status:'running',threads:{codex:'root'},createdAt:'2026-10-02T00:00:00Z',updatedAt:'2026-10-02T00:00:00Z'};
const caps={voice:true,models:[
 {id:'test',name:'GPT-6.1 Sol',backend:'codex',efforts:['low','medium','high','xhigh','max','ultra'],serviceTiers:[{id:'standard',name:'Standard',description:'Standard inference'},{id:'fast',name:'Fast',description:'Faster inference'}],defaultServiceTier:'standard'},
 {id:'fixture-luna',name:'GPT-6 Luna',backend:'codex',efforts:['low','medium','high','max']},
 {id:'fixture-deepseek',name:'DeepSeek V4 Flash',backend:'pi',efforts:['off','low','medium','high','max']},
],backends:{codex:{available:true,goals:false,subagents:true},pi:{available:true,goals:false,subagents:false}}};
const noop=()=>{};
const motionPreference=location.search.includes('motion')?'never':'always';
const motionFeatures=motionPreference==='never'?domMax:domAnimation;
window.fetch=async()=>new Response(JSON.stringify({usedTokens:1000,contextWindow:100000}),{headers:{'Content-Type':'application/json'}});
let seq=0;const events=[];const add=(turnId,type,data)=>events.push({seq:++seq,botId:'perf',turnId,type,data,time:'2026-10-02T00:00:00Z'});
for(let n=0;n<100;n++){
 const t='turn-'+n;
 add(t,'message',{role:'user',content:'Inspect this example '+n});
 add(t,'turn',{status:'running',backend:'codex'});
 add(t,'native',{backend:'codex',rootThreadId:'root',method:'item/started',params:{threadId:'root',item:{id:'a'+n,type:'agentMessage',phase:'final_answer',text:''}}});
 for(let j=0;j<30;j++)add(t,'native',{backend:'codex',rootThreadId:'root',method:'item/agentMessage/delta',params:{threadId:'root',itemId:'a'+n,delta:'Example text. '}});
 add(t,'native',{backend:'codex',rootThreadId:'root',method:'item/completed',params:{threadId:'root',item:{id:'a'+n,type:'agentMessage',phase:'final_answer',text:'A **formatted answer** with a [link](https://example.com).\n\n```typescript\nconst sample = (x: number) => x * 2;\n```\n\n- Check the first detail\n- Check the second detail\n\nFormula: $x^2+1$'}}});
 add(t,'turn',{status:'completed',backend:'codex',outputTokens:100});
}
add('live','turn',{status:'running',backend:'codex'});
add('live','native',{backend:'codex',rootThreadId:'root',method:'item/started',params:{threadId:'root',item:{id:'live-text',type:'agentMessage',phase:'commentary',text:'Working on the current step.'}}});
window.samples=[];
function App(){const [current,setCurrent]=useState(events); window.perfStep=()=>{const e={seq:++seq,botId:'perf',turnId:'live',type:'native',time:'2026-10-02T00:00:00Z',data:{backend:'codex',rootThreadId:'root',method:'item/agentMessage/delta',params:{threadId:'root',itemId:'live-text',delta:' More'}}};const start=performance.now();flushSync(()=>setCurrent(v=>[...v,e]));return performance.now()-start;};return <LazyMotion features={motionFeatures}><MotionConfig reducedMotion={motionPreference}><Profiler id="chat" onRender={(_,phase,duration)=>window.samples.push({phase,duration})}><ChatRoom bot={bot} draftScope="perf" events={current} messages={current.filter(event=>event.type==='message')} capabilities={caps} loading={false} suspended={false} onBack={noop} onBotChange={noop} onArchive={noop} onError={noop}/></Profiler></MotionConfig></LazyMotion>}
if(location.search.includes('full')) {
 window.__requests=[];window.__sources=[];
 const another={...bot,id:'other',name:'Other fixture',status:'idle'};
 const fixtureBots=[bot,another];
 const queueByBot=new Map();const pausedByBot=new Map();
 const emitFixture=(botId,type,data)=>window.__sources.at(-1)?.emit({seq:++seq,botId,type,time:new Date().toISOString(),data});
 window.fetch=async(input,options={})=>{
  const url=String(input);window.__requests.push(url);
  const path=new URL(url,location.origin).pathname;
  let result={};
  const botId=decodeURIComponent(path.match(/\/bots\/([^/]+)/)?.[1]||'perf');
  const queue=queueByBot.get(botId)||[];
  const body=typeof options.body==='string'?JSON.parse(options.body):{};
  const queueChanged=()=>{queueByBot.set(botId,queue);emitFixture(botId,'queue_control',{count:queue.length,paused:pausedByBot.get(botId)===true});};
  if(path.endsWith('/session'))result={authenticated:true,user:{id:'perf-user',username:'perf'},registrationAllowed:true};
  else if(path.endsWith('/nodes'))result={nodes:[{id:'local',name:'This server',local:true,online:true,status:'online'}]};
  else if(path.endsWith('/bots'))result={bots:fixtureBots};
  else if(path.endsWith('/capabilities'))result=caps;
  else if(path.endsWith('/events'))result={events:path.includes('/other/')?[]:events};
  else if(path.endsWith('/context'))result={usedTokens:1000,contextWindow:100000};
  else if(path.endsWith('/messages')){
   const queued={id:'fixture-queue-'+Date.now(),text:body.text,attachments:body.attachments||[],source:'web',createdAt:new Date().toISOString(),status:'queued'};
   queue.push(queued);queueChanged();result={turnId:'live',status:'queued',queueId:queued.id};
  }
  else if(path.endsWith('/queue/resume')){pausedByBot.set(botId,false);queueChanged();}
  else if(path.includes('/queue/')&&(options.method==='DELETE'||path.endsWith('/steer'))){
   const id=decodeURIComponent(path.split('/queue/')[1].split('/')[0]);const index=queue.findIndex(message=>message.id===id);if(index>=0)queue.splice(index,1);queueChanged();result={turnId:'live',status:'steered'};
  }
  else if(path.endsWith('/queue'))result={messages:queue,paused:pausedByBot.get(botId)===true};
  else if(options.method==='PATCH'&&path.includes('/bots/')){const index=fixtureBots.findIndex(item=>item.id===botId);if(index>=0){result={...fixtureBots[index],...body,updatedAt:new Date().toISOString()};fixtureBots[index]=result;emitFixture(botId,'bot',{bot:result});}}
  return new Response(JSON.stringify(result),{headers:{'Content-Type':'application/json'}});
 };
 window.EventSource=class {listeners=new Map();constructor(url){this.url=url;window.__sources.push(this);}addEventListener(name,fn){this.listeners.set(name,fn);}close(){this.closed=true;} emit(event){this.listeners.get('event')?.({data:JSON.stringify(event)});}};
 window.emitBurst=(count=100,botId='perf')=>{const source=window.__sources.at(-1);for(let i=0;i<count;i++)source.emit({seq:++seq,botId,turnId:'live',type:'native',time:'2026-10-02T00:00:00Z',data:{backend:'codex',rootThreadId:'root',method:'item/agentMessage/delta',params:{threadId:'root',itemId:'live-text',delta:' More'}}});};
 createRoot(document.getElementById('root')).render(<LazyMotion features={motionFeatures}><MotionConfig reducedMotion={motionPreference}><Profiler id="workspace" onRender={(_,phase,duration)=>window.samples.push({phase,duration})}><WorkspaceApp/></Profiler></MotionConfig></LazyMotion>);
} else createRoot(document.getElementById('root')).render(<App/>);

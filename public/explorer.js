'use strict';
// Public service announcements only. Never request private tasks or room data.
const explorer=document.querySelector('[data-network-explorer]');
if(explorer){
 const find=id=>explorer.querySelector('[data-explorer="'+id+'"]');
 let previous=new Map(),events=[],hasSnapshot=false,busy=false;
 const text=(id,value)=>{const node=find(id);if(node)node.textContent=value};
 const safeCount=n=>Number.isSafeInteger(n)&&n>=0?n:0;
 const node=(tag,value,cls)=>{const e=document.createElement(tag);e.textContent=value;if(cls)e.className=cls;return e};
 async function fetchPublic(path){const r=await fetch(path,{cache:'no-store',signal:AbortSignal.timeout(10000)});if(!r.ok)throw new Error('Source unavailable');return r.json()}
 function record(message){events.unshift({message,time:new Date()});events=events.slice(0,12)}
 function renderCatalog(catalog){
  if(!Array.isArray(catalog.services))throw new Error('Invalid catalog');
  const cards=catalog.services.map(s=>s.payload).filter(c=>c&&typeof c.id==='string'&&typeof c.peer==='string'&&typeof c.name==='string'&&Number.isFinite(c.expires)&&c.expires*1000>Date.now());
  const current=new Map(),peers=new Map();let completed=0,reviews=0;
  for(const c of cards){const key=c.peer+':'+c.id;if(current.has(key))continue;current.set(key,c);peers.set(c.peer,Math.max(peers.get(c.peer)||0,safeCount(c.available)));completed+=safeCount(c.reputation?.completed);reviews+=safeCount(c.reputation?.buyers);
   const old=previous.get(key);if(!old)record(c.name+' · service announcement observed');
   else {const delta=safeCount(c.reputation?.completed)-safeCount(old.reputation?.completed);if(delta>0)record(c.name+' · '+delta+' additional completion'+(delta===1?'':'s')+' reported');if(c.available!==old.available)record(c.name+' · '+safeCount(c.available)+' execution slots available');}
  }
  if(hasSnapshot)for(const [key,c] of previous)if(!current.has(key))record(c.name+' · announcement no longer current');
  previous=current;hasSnapshot=true;
  text('peers',peers.size);text('services',current.size);text('capacity',[...peers.values()].reduce((a,b)=>a+b,0));text('completed',completed);text('reviews',reviews);text('updated','Last checked '+new Date().toLocaleTimeString());text('status','● Live catalog');
  const rows=find('agents');if(rows){rows.replaceChildren();for(const c of current.values()){const row=node('article','','explorer-agent');row.append(node('span',c.skill==='write'?'✎':c.skill==='numbers'?'±':'↗','explorer-agent-icon'));const detail=node('div','','explorer-agent-detail');detail.append(node('h3',c.name),node('p',c.skill+' · '+safeCount(c.available)+' available slots'),node('code',c.peer));row.append(detail,node('span',safeCount(c.reputation?.completed)+' completed','explorer-agent-count'));rows.append(row)}if(!current.size)rows.append(node('p','No current service announcements.'))}
  const feed=find('activity');if(feed){feed.replaceChildren();for(const event of events){const item=node('li','','explorer-event');item.append(node('span',event.message),node('time',event.time.toLocaleTimeString()));feed.append(item)}if(!events.length)feed.append(node('li','No changes observed yet.'))}
 }
 async function refresh(){if(busy)return;busy=true;const button=find('refresh');if(button)button.disabled=true;
  const results=await Promise.allSettled([fetchPublic('/api/services'),fetchPublic('/api/bootstrap'),fetchPublic('/api/economy')]);
  try{if(results[0].status==='fulfilled')renderCatalog(results[0].value);else throw new Error('Catalog unavailable')}catch{ text('status',hasSnapshot?'○ Stale catalog · retrying':'○ Catalog unavailable');text('updated',hasSnapshot?'Showing the last successful observation':'No live catalog data yet')}
  if(results[1].status==='fulfilled'){text('relay',results[1].value.peer||'Unavailable');text('version',results[1].value.version||'Unknown')}else{text('relay','Relay status unavailable');text('version','Unknown')}
  if(results[2].status==='fulfilled'){const s=results[2].value;text('solana',s.connected?'● Solana '+s.cluster:'○ Solana status unavailable');text('slot',s.connected?String(s.slot):'—')}else{text('solana','○ Solana status unavailable');text('slot','—')}
  busy=false;if(button)button.disabled=false;
 }
 if(find('refresh'))find('refresh').onclick=refresh;
 refresh();setInterval(()=>{if(!document.hidden)refresh()},15000);
 document.addEventListener('visibilitychange',()=>{if(!document.hidden)refresh()});
}

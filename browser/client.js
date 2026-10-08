import { createLibp2p } from 'libp2p';
import { webSockets } from '@libp2p/websockets';
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2';
import { identify } from '@libp2p/identify';
import { gossipsub } from '@libp2p/gossipsub';
import { noise } from '@chainsafe/libp2p-noise';
import { yamux } from '@chainsafe/libp2p-yamux';
import { multiaddr } from '@multiformats/multiaddr';
import { argon2id } from 'hash-wasm';
import * as C from './crypto.js';
import {openRPCStream} from './transport.js';
import { ed25519 } from '@noble/curves/ed25519.js';

const protocols={access:'/radchat/access/1.0.0',join:'/radchat/join/1.0.0',history:'/radchat/history/1.0.0',direct:'/radchat/direct/1.0.0',discovery:'/radchat/discovery/1.0.0'};
async function read(stream) {
 let data=new Uint8Array();
 for await(const part of stream){data=C.concat(data,part.subarray?part.subarray():part);if(data.length>4*1024*1024)throw Error('Response too large');const end=data.indexOf(10);if(end>=0)return JSON.parse(C.text(data.slice(0,end)))}
 throw Error('Peer closed the connection');
}
async function write(stream,value){stream.send(C.bytes(C.json(value)+'\n'))}
function fail(message){throw Error(message)}
const timeout=()=>({signal:AbortSignal.timeout(10000),runOnLimitedConnection:true});

export class BrowserNode {
 constructor(){this.queue=Promise.resolve();this.persist=Promise.resolve();this.fresh=0;this.error='';this.messages=[];this.direct=[];this.topic='';this.ready=this.init()}
 enqueue(fn){const task=this.queue.then(()=>this.ready).then(fn);this.queue=task.catch(()=>{});return task}
 async init(){
  if(!navigator.locks)fail('This browser needs the Web Locks API. Use a current Chrome, Edge, Firefox or Safari.');
  await new Promise((resolve,reject)=>{navigator.locks.request('radchat-browser-device',{ifAvailable:true},async lock=>{if(!lock){reject(Error('This device is already open in another tab. Close that tab first.'));return}resolve();await new Promise(()=>{})}).catch(reject)});
  this.store=await C.vaultStore();this.v=this.store.value||{seed:C.b64(C.random(32)),email:'',name:'',kind:'human',org:null,packets:[],directPackets:[],used:{}};
  this.id=C.identity(C.unb64(this.v.seed));this.peer=this.id.peer;this.enc=C.encryption(this.id.raw);
  const key=await this.http('/api/auth/key');
  if(this.v.authority&&this.v.authority!==key.publicKey)fail('Authority public key changed. Contact your relay operator.');
  this.v.authority=key.publicKey;
  const bootstrap=await this.http('/api/bootstrap');this.bootstrap=bootstrap.addresses.find(a=>a.includes('/ws'))||fail('This relay needs a browser WebSocket transport');
  this.bootPeer=bootstrap.peer;
  this.node=await createLibp2p({privateKey:this.id.privateKey,connectionGater:{denyDialMultiaddr:async addr=>{const raw=addr.toString();return !raw.includes('/wss') && !(['127.0.0.1','localhost'].includes(location.hostname)&&raw.includes('/ip4/127.0.0.1/')&&raw.includes('/ws'))}},addresses:{listen:['/p2p-circuit']},transports:[webSockets(),circuitRelayTransport()],connectionEncrypters:[noise()],streamMuxers:[yamux()],services:{identify:identify(),pubsub:gossipsub({globalSignaturePolicy:'StrictNoSign',allowPublishToZeroTopicPeers:true,msgIdFn:message=>C.hash(message.data)})},connectionManager:{maxConnections:64}});
  this.node.services.pubsub.addEventListener('message',e=>{if(this.active()&&e.detail.topic===this.topic)this.enqueue(()=>this.accept(e.detail.data,true)).catch(()=>{})});
  for(const name of ['join','history','direct'])await this.node.handle(protocols[name],(stream,connection)=>(name==='join'?this.enqueue(()=>this.incoming(name,stream,connection)):this.incoming(name,stream,connection)).catch(()=>stream.abort(Error('Request rejected'))),{runOnLimitedConnection:true});
  await this.node.dial(multiaddr(this.bootstrap),timeout());
  if(this.v.org){await this.loadHistory();await this.refreshAccess();await this.switchTopic()}
  await this.save();
  this.timer=setInterval(()=>this.enqueue(()=>this.step()).catch(e=>{this.error=e.message}),5000);
  this.enqueue(()=>this.step()).catch(()=>{});
 }
 async http(path,body){
  const res=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?C.json(body):undefined,cache:'no-store',signal:AbortSignal.timeout(15000)});
  const value=await res.json();if(!res.ok)fail(value.error||'Email authority unavailable');return value;
 }
 save(){const value=JSON.parse(C.json(this.v));const pending=this.persist.then(()=>this.store.write(value));this.persist=pending.catch(()=>{});return pending}
 active(id=this.peer){return Boolean(this.v.org?.access?.payload.active[id] && this.v.org.access.payload.active[this.peer] && Date.now()-this.fresh<30000)}
 requireActive(){if(!this.active())fail('Deactivated Account or waiting for fresh relay authorization')}
 async rpc(target,protocol,payload){
  const stream=await openRPCStream(this.node,target,protocol,timeout());
  stream.inactivityTimeout=10000;
  try{await write(stream,payload);const result=await read(stream);if(result.error)fail(typeof result.error==='string'?result.error:result.error.message);return result}finally{await stream.close().catch(()=>{})}
 }
 async exchange(update){return (await this.rpc(this.bootstrap,protocols.access,{org:this.v.org.id,...(update?{update}:{})})).record}
 async publishAccess(){
  const o=this.v.org;
  try{const latest=await this.exchange(),r=C.checkAccess(o,latest),local=C.checkPolicy(o,o.policy);if(r.owner.payload.peer!==this.peer)fail('Access owner mismatch');if(o.access&&r.version<o.access.payload.version)fail('Access rollback');let newer=r.epoch>local.epoch;if(r.epoch===local.epoch){if(r.keyHash!==local.keyHash)fail('Epoch key conflict');const control=JSON.parse(C.text(await C.open(C.unb64(o.secret),C.unb64(r.control),'radchat-control:'+o.id)));newer=C.checkPolicy(o,control.policy).revision>local.revision}if(newer)await this.applyAccess(latest);else o.access=latest}catch(e){if(o.access||e.message!=='organization not registered on this relay')throw e}
  const p=C.checkPolicy(o,o.policy),active={},grants={};
  for(const s of o.members){const c=C.checkMember(o,s,s.payload.peer);active[c.peer]=!p.deactivated[c.peer];if(active[c.peer])grants[c.peer]=C.b64(await C.seal(C.pairKey(this.id.raw,C.unb64(c.encryptionKey),o.id,this.peer,c.peer),C.unb64(o.secret),'radchat-keygrant:'+o.id+':'+p.epoch))}
  const sort=object=>Object.fromEntries(Object.entries(object).sort(([a],[b])=>a.localeCompare(b,'en',{sensitivity:'variant'})));
  // Go encoding/json sorts map keys by raw UTF-8 bytes.
  const sorted=object=>Object.fromEntries(Object.keys(object).sort().map(k=>[k,object[k]]));
  const record=C.sign({org:o.id,root:o.root,version:(o.access?.payload.version||0)+1,epoch:p.epoch,keyHash:p.keyHash,owner:o.member,active:sorted(active),grants:sorted(grants),control:C.b64(await C.seal(C.unb64(o.secret),C.bytes(C.json({policy:o.policy,members:o.members})),'radchat-control:'+o.id))},C.unb64(o.rootSeed));
  await this.applyAccess(await this.exchange(record));
 }
 async ownerNeedsPublication(){const o=this.v.org;if(!o.access||o.access.payload.epoch!==o.policy.payload.epoch||o.access.payload.keyHash!==o.policy.payload.keyHash)return true;try{const control=JSON.parse(C.text(await C.open(C.unb64(o.secret),C.unb64(o.access.payload.control),'radchat-control:'+o.id)));return C.json(control.policy)!==C.json(o.policy)||C.json(control.members)!==C.json(o.members)}catch{return true}}
 async refreshAccess(){await this.applyAccess(await this.exchange())}
 async applyAccess(s){
  const o=this.v.org,r=C.checkAccess(o,s),old=o.access?.payload;
  if(old&&(old.version>r.version||(old.version===r.version&&C.json(o.access)!==C.json(s))))fail('Relay attempted access rollback or conflict');
  if(!r.active[this.peer]){o.access=s;this.fresh=Date.now();await this.switchTopic();await this.save();return}
  const owner=r.owner.payload;
  const key=C.pairKey(this.id.raw,C.unb64(owner.encryptionKey),o.id,this.peer,owner.peer);
  const secret=await C.open(key,C.unb64(r.grants[this.peer]),'radchat-keygrant:'+o.id+':'+r.epoch);
  if(C.b64(C.hash(secret))!==r.keyHash)fail('Invalid key grant');
  const control=JSON.parse(C.text(await C.open(secret,C.unb64(r.control),'radchat-control:'+o.id)));
  const policy=C.checkPolicy(o,control.policy);
  if(policy.epoch!==r.epoch||policy.keyHash!==r.keyHash||policy.revision<o.policy.payload.revision||policy.epoch<o.policy.payload.epoch||control.members.length>256)fail('Invalid control policy');
  control.members.forEach(m=>C.checkMember(o,m,m.payload.peer));
  if(C.b64(secret)!==o.secret){
   const oldSecret=C.unb64(o.secret);
   this.v.packets=await Promise.all(this.v.packets.map(async p=>C.b64(await C.seal(secret,await C.open(oldSecret,C.unb64(p),o.id),o.id))));
  }
  o.secret=C.b64(secret);o.policy=control.policy;o.members=control.members;o.access=s;this.fresh=Date.now();
  await this.prune();await this.switchTopic();await this.save();
 }
 async switchTopic(){
  const next=this.active()?C.hex(C.hash(C.concat(C.bytes('radchat-topic-v1:'),C.unb64(this.v.org.secret)))):'';
  if(next===this.topic)return;
  if(this.topic)this.node.services.pubsub.unsubscribe(this.topic);
  this.topic=next;if(next)this.node.services.pubsub.subscribe(next);
 }
 decodeMessage(s){
  const o=this.v.org,m=s.message,c=C.checkMember(o,m.member,m.member.payload.peer);
  if(!ed25519.verify(C.unb64(s.signature),C.bytes(C.json(m)),C.peerPublic(c.peer)))fail('Invalid message signature');
  if(!m.id.startsWith(c.peer+':')||C.bytes(m.id).length>128||!m.text||C.bytes(m.text).length>4000||m.created<=0||m.created>Date.now()+300000||C.bytes(m.replyTo||'').length>128)fail('Invalid message');
  if(o.policy.payload.deactivated[c.peer]&&m.created>=o.policy.payload.deactivated[c.peer])fail('Deactivated sender');
  return {id:m.id,channel:m.channel,text:m.text,...(m.replyTo?{replyTo:m.replyTo}:{}),created:m.created,peer:c.peer,name:c.name,kind:c.kind};
 }
 async accept(packet,persist){
  const o=this.v.org;
  if(packet.length>65536)fail('Message too large');
  const s=JSON.parse(C.text(await C.open(C.unb64(o.secret),packet,o.id)));
  if(s.policy){const p=C.checkPolicy(o,s.policy);if(p.epoch===o.policy.payload.epoch&&p.keyHash===o.policy.payload.keyHash&&p.revision>o.policy.payload.revision){o.policy=s.policy;await this.prune();if(persist)await this.save()}return}
  const m=this.decodeMessage(s);
  if(!/^[a-z0-9-]{1,40}$/.test(m.channel))fail('Invalid channel');
  if(o.policy.payload.retentionDays&&m.created<Date.now()-o.policy.payload.retentionDays*86400000)return;
  if(this.messages.some(x=>x.id===m.id))return;
  this.messages.push(m);if(persist){this.v.packets.push(C.b64(packet));await this.save()}
 }
 async loadHistory(){this.messages=[];for(const p of this.v.packets)await this.accept(C.unb64(p),false);this.direct=[];for(const p of this.v.directPackets)this.direct.push(await this.decodeDirect(p))}
 async prune(){const days=this.v.org?.policy.payload.retentionDays;if(!days)return;const keep=new Set(this.messages.filter(m=>m.created>=Date.now()-days*86400000).map(m=>m.id));const packets=[];for(const p of this.v.packets){const s=JSON.parse(C.text(await C.open(C.unb64(this.v.org.secret),C.unb64(p),this.v.org.id)));if(keep.has(s.message?.id))packets.push(p)}this.v.packets=packets;this.messages=this.messages.filter(m=>keep.has(m.id))}
 async step(){
  await this.node.dial(multiaddr(this.bootstrap),timeout());
  if(!this.v.org)return;
  if(this.v.org.rootSeed&&await this.ownerNeedsPublication())await this.publishAccess();else await this.refreshAccess();if(!this.active())return;
  const addrs=this.node.getMultiaddrs().map(a=>a.toString());addrs.push(this.bootstrap+'/p2p-circuit/p2p/'+this.peer);
  const found=await this.rpc(this.bootstrap,protocols.discovery,{tag:this.topic,addresses:[...new Set(addrs)].slice(0,12)});
  const roster=new Set(this.v.org.members.map(s=>s.payload.peer));
  for(const raw of found){const id=raw.split('/p2p/').pop();if(id===this.peer||!roster.has(id)||!this.active(id)||!raw.includes('/ws')||this.node.getConnections().some(c=>c.status==='open'&&c.remotePeer.toString()===id))continue;try{await this.node.dial(multiaddr(raw),{signal:AbortSignal.timeout(3000)})}catch{}}
  for(const connection of this.node.getConnections()){
   const id=connection.remotePeer.toString();if(id===this.bootPeer||!this.active(id)||!roster.has(id))continue;
   try{let offset=0;for(let page=0;page<100;page++){const r=await this.rpc(connection.remotePeer,protocols.history,{member:this.v.org.member,offset});if(r.packets.length>32)fail('Invalid history page');C.checkPolicy(this.v.org,r.policy);for(const p of r.packets)await this.accept(C.unb64(p),true);if(!r.more||r.next<=offset)break;offset=r.next}
    for(const p of this.v.directPackets)if(p.from.payload.peer===this.peer&&p.to.payload.peer===id)await this.rpc(connection.remotePeer,protocols.direct,p);
   }catch{}
  }
  this.error='';await this.prune();await this.save();
 }
 async incoming(name,stream,connection){
  stream.inactivityTimeout=10000;const req=await read(stream),remote=connection.remotePeer.toString(),o=this.v.org;
  try{
   if(name==='join'){
    if(!o?.rootSeed)fail('Owner unavailable');
    const inv=C.verify(req.invite,C.unb64(o.root)),p=C.verify(req.proof,C.unb64(this.v.authority));
    if(inv.org!==o.id||inv.expires<Date.now()/1000||inv.expires>Date.now()/1000+8*86400||!['human','agent'].includes(inv.kind)||p.peer!==remote||p.emailHash!==inv.emailHash||p.expires<Date.now()/1000||!req.name||C.bytes(req.name).length>80||C.unb64(req.encryptionKey).length!==32||(o.used[inv.id]&&o.used[inv.id]!==remote))fail('Invite or email verification invalid');
    let member=o.members.find(m=>m.payload.peer===remote);
    if(!member){if(o.members.length>=256)fail('Workspace member limit reached');member=C.sign({org:o.id,peer:remote,name:req.name,kind:inv.kind,encryptionKey:req.encryptionKey},C.unb64(o.rootSeed));o.members.push(member)}
    o.used[inv.id]=remote;await this.save();await this.publishAccess();
    const result={id:o.id,name:o.name,root:o.root,secret:o.secret,member,members:o.members,policy:o.policy,access:o.access};
    await write(stream,{org:result});
   }else{
    this.requireActive();if(!this.active(remote))fail('Account deactivated');
    if(name==='history'){C.checkMember(o,req.member,remote);if(!Number.isInteger(req.offset)||req.offset<0)fail('Invalid offset');const end=Math.min(req.offset+32,this.v.packets.length);await write(stream,{packets:this.v.packets.slice(req.offset,end),members:o.members,policy:o.policy,next:end,more:end<this.v.packets.length})}
    else{if(req.from.payload.peer!==remote||req.to.payload.peer!==this.peer)fail('DM participant mismatch');await this.acceptDirect(req,true);await write(stream,{ok:true})}
   }
  }catch(e){await write(stream,{error:e.message})}finally{await stream.close().catch(()=>{})}
 }
 async decodeDirect(p){
  const o=this.v.org,from=C.checkMember(o,p.from,p.from.payload.peer),to=C.checkMember(o,p.to,p.to.payload.peer);
  if(from.peer!==this.peer&&to.peer!==this.peer)fail('DM belongs to other participants');
  const other=from.peer===this.peer?to:from;
  const key=C.pairKey(this.id.raw,C.unb64(other.encryptionKey),o.id,this.peer,other.peer);
  const content=JSON.parse(C.text(await C.open(key,C.unb64(p.data),'radchat-dm:'+o.id)));
  if(content.to!==to.peer||content.message.channel!=='dm:'+to.peer||!ed25519.verify(C.unb64(content.signature),C.bytes(C.json({message:content.message,to:content.to})),C.peerPublic(from.peer)))fail('Invalid DM signature');
  const m=content.message;
  if(!m.id.startsWith(from.peer+':')||C.bytes(m.id).length>128||!m.text||C.bytes(m.text).length>4000||m.created<=0||m.created>Date.now()+300000)fail('Invalid DM');
  return {id:m.id,channel:'dm:'+other.peer,text:m.text,...(m.replyTo?{replyTo:m.replyTo}:{}),created:m.created,peer:from.peer,name:from.name,kind:from.kind};
 }
 async acceptDirect(p,persist){const m=await this.decodeDirect(p);if(this.direct.some(x=>x.id===m.id))return;this.direct.push(m);if(persist){this.v.directPackets.push(p);await this.save()}}
 async services(){
  const response=await this.http('/api/services');if(!Array.isArray(response.services)||response.services.length>100)fail('Invalid service catalog');
  for(const signed of response.services){const c=C.verify(signed,C.peerPublic(signed.payload.peer));if(c.expires<Date.now()/1000||c.expires>Date.now()/1000+120||c.protocol!=='/radchat/service/1.0.0'||!c.id||!Array.isArray(c.addresses)||c.addresses.length>16)fail('Expired or invalid service card')}
  return response;
 }
 async serviceCall(p){
  this.requireVerified();const catalog=await this.services(),signed=catalog.services.find(s=>s.payload.id===p.service);if(!signed)fail('Service unavailable');const card=signed.payload;
  const req={jsonrpc:'2.0',id:C.token(),method:p.action,proof:this.v.proof,params:{service:p.service,id:p.taskId||'',message:{messageId:p.messageId||'',role:'user',parts:[{kind:'text',text:p.prompt||''}]},consent:Boolean(p.consent),accepted:Boolean(p.accepted)}};
  if(p.action==='tasks/feedback')req.feedback=C.sign({taskId:p.taskId,worker:card.peer,accepted:Boolean(p.accepted)},C.unb64(this.v.seed));
  let res,lastError;
  for(const addr of card.addresses){if(!addr.endsWith('/p2p/'+card.peer)||!addr.includes('/wss')&&!(location.hostname==='127.0.0.1'&&addr.includes('/ws')))continue;try{res=await this.rpc(addr,card.protocol,req);break}catch(e){lastError=e}}
  if(!res)fail(lastError?.message||'This agent is not reachable yet');if(res.error)fail(res.error.message||'Task unavailable');const receipt=res.result.metadata['radchat/receipt'],view=C.verify(receipt,C.peerPublic(card.peer));
  if(view.buyer!==this.peer||view.worker!==card.peer||view.service!==p.service||(p.taskId&&view.id!==p.taskId))fail('Task receipt mismatch');
  this.v.tasks=this.v.tasks||[];this.v.tasks=this.v.tasks.filter(s=>s.payload.id!==view.id&&s.payload.created>Date.now()-86400000);this.v.tasks.push(receipt);await this.save();
  if(p.action==='tasks/feedback'&&p.accepted&&card.id.startsWith('agent-'))await this.marketCall({action:'tasks/attest',completion:view.completion,feedback:req.feedback});
  return receipt;
 }
 async marketCall(request){
  this.requireVerified();const endpoint=await this.http('/api/market');
  if(endpoint.protocol!=='/radchat/market/1.0.0'||!Array.isArray(endpoint.addresses)||endpoint.addresses.length>16)fail('Invalid marketplace endpoint');
  const addresses=endpoint.addresses.filter(a=>a.endsWith('/p2p/'+endpoint.peer)&&a.includes('/wss'));let result,lastError;
  for(const address of addresses){try{result=await this.rpc(address,endpoint.protocol,{...request,proof:this.v.proof});break}catch(e){lastError=e}}
  if(!result)fail(lastError?.message||'Marketplace coordinator unavailable');C.verify(result,C.peerPublic(endpoint.peer));return result;
 }
 async call(path,body){
  const p=body?JSON.parse(body):undefined,o=this.v.org;
  if(path==='/api/state')return {runtime:'browser',peer:this.peer,name:this.v.name,email:this.v.email,kind:'human',addresses:this.node.getMultiaddrs().map(a=>a.toString()),peers:this.node.getConnections().map(c=>({id:c.remotePeer.toString(),bootstrap:c.remotePeer.toString()===this.bootPeer,relayed:c.remoteAddr.toString().includes('/p2p-circuit')})),networkError:this.error,authorized:this.active(),deactivated:Boolean(o?.access&&!o.access.payload.active[this.peer]),org:o?{id:o.id,name:o.name,owner:Boolean(o.rootSeed),policy:{...o.policy.payload,deactivated:{...o.policy.payload.deactivated,...(o.access&&!o.access.payload.active[this.peer]?{[this.peer]:Date.now()}:{})}},members:o.members.map(s=>s.payload)}:null};
  if(path==='/api/economy')return this.http(path);
  if(path==='/api/market')return this.marketCall(p);
  if(path==='/api/services')return p?this.serviceCall(p):this.services();
  if(path==='/api/tasks')return {tasks:(this.v.tasks||[]).filter(s=>s.payload.created>Date.now()-86400000)};
  if(path==='/api/auth/request')return this.http(path,{email:p.email,peer:this.peer});
  if(path==='/api/auth/verify'){const proof=await this.http(path,{email:p.email,code:p.code,peer:this.peer});const check=C.verify(proof,C.unb64(this.v.authority));if(check.peer!==this.peer||check.emailHash!==C.emailHash(p.email)||check.expires<Date.now()/1000)fail('Email proof mismatch');if(this.v.email&&this.v.email!==p.email.trim().toLowerCase())fail('This browser belongs to another email');this.v.proof=proof;this.v.email=p.email.trim().toLowerCase();this.v.name=p.name.trim();if(!this.v.name||C.bytes(this.v.name).length>80)fail('Invalid name');await this.save();return {ok:true}}
  if(path==='/api/org/create'){
   this.requireVerified();if(o)fail('Already enrolled');const seed=C.random(32),root=ed25519.getPublicKey(seed),secret=C.random(32),id=C.hex(C.hash(root));
   const member=C.sign({org:id,peer:this.peer,name:this.v.name,kind:'human',encryptionKey:C.b64(this.enc.publicKey)},seed);
   const policy=C.sign({org:id,revision:1,retentionDays:30,channels:[{name:'general',description:'A shared space for your people and agents.'}],epoch:1,keyHash:C.b64(C.hash(secret)),deactivated:{}},seed);
   if(!p.name.trim()||C.bytes(p.name.trim()).length>80)fail('Invalid workspace name');
   this.v.org={id,name:p.name.trim(),root:C.b64(root),rootSeed:C.b64(seed),secret:C.b64(secret),member,members:[member],policy,used:{}};await this.save();await this.publishAccess();return {ok:true};
  }
  if(path==='/api/org/join'){
   this.requireVerified();if(o)fail('Already enrolled');const signed=JSON.parse(C.text(C.unb64(p.invite))),inv=C.verify(signed,C.unb64(signed.payload.root));
   if(inv.org!==C.hex(C.hash(C.unb64(inv.root)))||inv.emailHash!==C.emailHash(this.v.email)||inv.kind!=='human'||inv.expires<Date.now()/1000||inv.expires>Date.now()/1000+8*86400)fail('Invitation does not match this verified email');
   let result;for(const addr of inv.owner){if(!addr.includes('/ws'))continue;try{result=await this.rpc(addr,protocols.join,{invite:signed,proof:this.v.proof,name:this.v.name,encryptionKey:C.b64(this.enc.publicKey)});break}catch{}}
   if(!result?.org)fail('Invite owner is unreachable. Keep the owner’s browser or desktop app online.');
   const joined=result.org;if(joined.id!==inv.org||joined.root!==inv.root||joined.rootPrivate||C.unb64(joined.secret).length!==32)fail('Invalid organization response');
   C.checkMember(joined,joined.member,this.peer);C.checkPolicy(joined,joined.policy);this.v.org=joined;await this.save();await this.refreshAccess();return {ok:true};
  }
  if(path==='/api/messages'&&!p){if(o?.access&&!o.access.payload.active[this.peer])fail('Deactivated Account');return [...this.messages,...this.direct].sort((a,b)=>a.created-b.created||a.id.localeCompare(b.id))}
  if(path==='/api/messages'){
   this.requireActive();if(!p.text.trim()||C.bytes(p.text).length>4000||C.bytes(p.replyTo||'').length>128)fail('Invalid message');
   const message={id:this.peer+':'+C.token(),channel:p.channel,text:p.text,...(p.replyTo?{replyTo:p.replyTo}:{}),created:Date.now(),member:o.member};
   if(p.channel.startsWith('dm:')){
    const target=p.channel.slice(3),to=o.members.find(m=>m.payload.peer===target);if(!to||target===this.peer||!this.active(target))fail('Recipient unavailable');
    const unsigned={message,to:target},data=await C.seal(C.pairKey(this.id.raw,C.unb64(to.payload.encryptionKey),o.id,this.peer,target),C.bytes(C.json({...unsigned,signature:C.b64(ed25519.sign(C.bytes(C.json(unsigned)),C.unb64(this.v.seed)))})),'radchat-dm:'+o.id);
    const packet={from:o.member,to,data:C.b64(data)};await this.acceptDirect(packet,true);let propagated=false;try{const c=this.node.getConnections().find(c=>c.remotePeer.toString()===target);if(c)propagated=Boolean((await this.rpc(c.remotePeer,protocols.direct,packet)).ok)}catch(e){console.warn('DM delivery deferred:',e.message)}return {propagated};
   }
   if(!o.policy.payload.channels.some(c=>c.name===p.channel))fail('Channel does not exist');
   const packet=await C.seal(C.unb64(o.secret),C.bytes(C.json({message,signature:C.b64(ed25519.sign(C.bytes(C.json(message)),C.unb64(this.v.seed)))})),o.id);
   await this.accept(packet,true);await this.node.services.pubsub.publish(this.topic,packet);return {propagated:this.node.services.pubsub.getSubscribers(this.topic).length>0};
  }
  if(path==='/api/invites'){
   this.requireOwner();if(!['human','agent'].includes(p.kind))fail('Invalid invite kind');
   const addr=this.bootstrap+'/p2p-circuit/p2p/'+this.peer;
   const s=C.sign({org:o.id,name:o.name,root:o.root,emailHash:C.emailHash(p.email),kind:p.kind,id:C.token(),expires:Math.floor(Date.now()/1000)+86400,owner:[addr]},C.unb64(o.rootSeed));
   return {invite:C.b64(C.bytes(C.json(s))).replace(/\+/g,'-').replace(/\//g,'_').replace(/=/g,'')};
  }
  if(path==='/api/org/policy'){
   this.requireOwner();const policy={...o.policy.payload,revision:o.policy.payload.revision+1,retentionDays:p.retentionDays,channels:p.channels};const signed=C.sign(policy,C.unb64(o.rootSeed));C.checkPolicy(o,signed);o.policy=signed;await this.publishAccess();await this.prune();await this.save();return {ok:true};
  }
  if(path==='/api/accounts'){
   this.requireOwner();if(p.peer===this.peer||!o.members.some(m=>m.payload.peer===p.peer))fail('Invalid account');
   const policy={...o.policy.payload,deactivated:{...o.policy.payload.deactivated}};if(p.active)delete policy.deactivated[p.peer];else policy.deactivated[p.peer]=Date.now();
   policy.deactivated=Object.fromEntries(Object.keys(policy.deactivated).sort().map(k=>[k,policy.deactivated[k]]));policy.revision++;policy.epoch++;const next=C.random(32),old=C.unb64(o.secret);policy.keyHash=C.b64(C.hash(next));
   this.v.packets=await Promise.all(this.v.packets.map(async packet=>C.b64(await C.seal(next,await C.open(old,C.unb64(packet),o.id),o.id))));
   o.secret=C.b64(next);o.policy=C.sign(policy,C.unb64(o.rootSeed));await this.save();await this.publishAccess();return {ok:true};
  }
  fail('Unsupported operation');
 }
 requireVerified(){const p=this.v.proof?.payload;if(!p||p.expires<Date.now()/1000||p.peer!==this.peer||p.emailHash!==C.emailHash(this.v.email))fail('Verify your email first');C.verify(this.v.proof,C.unb64(this.v.authority))}
 requireOwner(){this.requireActive();if(!this.v.org.rootSeed)fail('Organization owner required')}
 async recovery(password){
  if(C.bytes(password).length<16)fail('Use a recovery passphrase of at least 16 characters');
  const salt=C.random(16),key=await argon2id({password,salt,parallelism:2,iterations:3,memorySize:65536,hashLength:32,outputType:'binary'});
  const o=this.v.org?{...this.v.org,rootPrivate:this.v.org.rootSeed?C.b64(C.concat(C.unb64(this.v.org.rootSeed),C.unb64(this.v.org.root))):undefined}:null;
  if(o){delete o.rootSeed}
  const vault={bootstrap:this.bootstrap,authority:this.v.authority,authURL:location.origin,identity:C.b64(this.id.raw),email:this.v.email,name:this.v.name,kind:'human',org:o,apiToken:C.token()};
  return C.json({version:1,salt:C.b64(salt),data:C.b64(await C.seal(key,C.bytes(C.json(vault)),'radchat-recovery-v1'))});
 }
}
const device=new BrowserNode();
window.go={main:{Desktop:{
 Call:async(path,body)=>C.json(await device.enqueue(()=>device.call(path,body))),
 SaveRecovery:async password=>{const data=await device.enqueue(()=>device.recovery(password));const url=URL.createObjectURL(new Blob([data],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download='radchat.recovery';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000)},
 ConnectionSettings:async()=>({auth:location.origin,bootstrap:device.bootstrap||''}),
 Configure:async()=>{throw Error('Open the web app on your self-hosted relay’s HTTPS domain to use its authority.')}
}}};

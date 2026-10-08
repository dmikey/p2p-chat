import { ed25519, x25519 } from '@noble/curves/ed25519.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { hmac } from '@noble/hashes/hmac.js';
import { privateKeyFromRaw, privateKeyToProtobuf } from '@libp2p/crypto/keys';
import { peerIdFromPublicKey, peerIdFromString } from '@libp2p/peer-id';

export const bytes = value => new TextEncoder().encode(value);
export const text = value => new TextDecoder('utf-8', {fatal:true}).decode(value);
export const random = size => crypto.getRandomValues(new Uint8Array(size));
export const concat = (...parts) => { const out=new Uint8Array(parts.reduce((n,p)=>n+p.length,0));let at=0;for(const p of parts){out.set(p,at);at+=p.length}return out };
export const b64 = value => {let s='';for(const x of value)s+=String.fromCharCode(x);return btoa(s)};
export const unb64 = value => Uint8Array.from(atob(value.replace(/-/g,'+').replace(/_/g,'/')), c=>c.charCodeAt(0));
export const token = () => b64(random(24)).replace(/\+/g,'-').replace(/\//g,'_').replace(/=/g,'');
export const hex = value => [...value].map(x=>x.toString(16).padStart(2,'0')).join('');
export const hash = value => sha256(value);
export const json = value => JSON.stringify(value).replace(/[<>&\u2028\u2029]/g,c=>'\\u'+c.charCodeAt(0).toString(16).padStart(4,'0'));
export const sign = (payload,seed) => ({payload,signature:b64(ed25519.sign(bytes(json(payload)),seed))});
export function verify(s,key) {if(!s || !ed25519.verify(unb64(s.signature),bytes(json(s.payload)),key)){throw Error('Invalid signature')}return s.payload}
export function identity(seed) {
 const privateKey=privateKeyFromRaw(concat(seed,ed25519.getPublicKey(seed)));
 return {privateKey,raw:privateKeyToProtobuf(privateKey),peer:peerIdFromPublicKey(privateKey.publicKey).toString()};
}
export function peerPublic(peer) {const id=peerIdFromString(peer);if(!id.publicKey)throw Error('Peer key unavailable');return id.publicKey.raw}
export function encryption(raw) {const seed=hash(concat(bytes('radchat-x25519-v1:'),raw));return {seed,publicKey:x25519.getPublicKey(seed)}}
export function pairKey(raw,other,org,a,b) {
 const shared=x25519.getSharedSecret(encryption(raw).seed,other);
 const ids=[a,b].sort();
 return hmac(sha256,hmac(sha256,bytes(org),shared),concat(bytes('radchat-dm-v1:'+ids[0]+':'+ids[1]),new Uint8Array([1])));
}
export async function seal(key,plain,scope) {
 const k=key instanceof CryptoKey?key:await crypto.subtle.importKey('raw',key,'AES-GCM',false,['encrypt']);
 const nonce=random(12);
 const data=await crypto.subtle.encrypt({name:'AES-GCM',iv:nonce,additionalData:bytes(scope)},k,plain);
 return concat(nonce,new Uint8Array(data));
}
export async function open(key,data,scope) {
 if(data.length<28)throw Error('Short ciphertext');
 const k=key instanceof CryptoKey?key:await crypto.subtle.importKey('raw',key,'AES-GCM',false,['decrypt']);
 return new Uint8Array(await crypto.subtle.decrypt({name:'AES-GCM',iv:data.slice(0,12),additionalData:bytes(scope)},k,data.slice(12)));
}
export function checkMember(org,s,peer) {
 const c=verify(s,unb64(org.root));
 if(c.org!==org.id || c.peer!==peer || !['human','agent'].includes(c.kind) || unb64(c.encryptionKey).length!==32 || !c.name || bytes(c.name).length>80)throw Error('Membership mismatch');
 return c;
}
export function checkPolicy(org,s) {
 const p=verify(s,unb64(org.root));
 if(p.org!==org.id || p.revision<1 || p.epoch<1 || unb64(p.keyHash).length!==32 || !Array.isArray(p.channels) || !p.channels.length || p.channels.length>100 || p.retentionDays<0 || p.retentionDays>36500 || !p.deactivated || Object.keys(p.deactivated).length>256)throw Error('Invalid policy');
 const names=new Set();
 for(const c of p.channels){if(!/^[a-z0-9-]{1,40}$/.test(c.name)||names.has(c.name)||bytes(c.description).length>200)throw Error('Invalid channel');names.add(c.name)}
 return p;
}
export function checkAccess(org,s) {
 const r=verify(s,unb64(org.root));
 if(r.org!==org.id || r.root!==org.root || r.version<1 || r.epoch<1 || unb64(r.keyHash).length!==32 || Object.keys(r.active).length>256 || Object.keys(r.grants).length>256 || unb64(r.control).length>1048576)throw Error('Invalid access record');
 checkMember(org,r.owner,r.owner.payload.peer);
 if(!r.active[r.owner.payload.peer])throw Error('Owner must be active');
 for(const [id,active] of Object.entries(r.active)){peerIdFromString(id);if(active&&unb64(r.grants[id]||'').length!==60)throw Error('Invalid key grant')}
 return r;
}
export const emailHash = email => hex(hash(bytes(email.trim().toLowerCase())));
export async function vaultStore() {
 const db=await new Promise((resolve,reject)=>{const r=indexedDB.open('radchat-device-v1',1);r.onupgradeneeded=()=>r.result.createObjectStore('device');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)});
 const read=()=>new Promise((resolve,reject)=>{const tx=db.transaction('device');const r=tx.objectStore('device').get('vault');r.onsuccess=()=>resolve(r.result);r.onerror=()=>reject(r.error)});
 const previous=await read();
 const key=previous?.key||await crypto.subtle.generateKey({name:'AES-GCM',length:256},false,['encrypt','decrypt']);
 const value=previous?JSON.parse(text(await open(key,previous.data,'radchat-browser-vault-v1'))):null;
 return {value,write:async(value)=>{const data=await seal(key,bytes(json(value)),'radchat-browser-vault-v1');await new Promise((resolve,reject)=>{const tx=db.transaction('device','readwrite');tx.objectStore('device').put({key,data},'vault');tx.oncomplete=resolve;tx.onerror=()=>reject(tx.error);tx.onabort=()=>reject(tx.error)})}};
}

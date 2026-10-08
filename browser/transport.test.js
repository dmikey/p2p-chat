import test from 'node:test';
import assert from 'node:assert/strict';
import {openRPCStream} from './transport.js';
test('RPC reuses a live limited circuit when no dialable peer-store address exists',async()=>{
 let opened=false;const target={toString:()=> 'target-peer'};
 const node={getConnections:()=>[{status:'open',remotePeer:target,limits:{bytes:4096},newStream:async(protocol,options)=>{assert.equal(protocol,'/radchat/direct/1.0.0');assert.equal(options.runOnLimitedConnection,true);opened=true;return 'stream'}}],dialProtocol:()=>{throw Error('Peer store has no dialable address')}};
 assert.equal(await openRPCStream(node,target,'/radchat/direct/1.0.0',{runOnLimitedConnection:true}),'stream');assert.equal(opened,true);
});
test('RPC cannot reuse a connection belonging to a different peer',async()=>{
 let dialed=false;const target={toString:()=> 'expected-peer'};const node={getConnections:()=>[{status:'open',remotePeer:{toString:()=> 'other-peer'},newStream:()=>{throw Error('wrong recipient')}}],dialProtocol:async passed=>{assert.equal(passed,target);dialed=true;return 'correct-stream'}};
 assert.equal(await openRPCStream(node,target,'/protocol',{}),'correct-stream');assert.equal(dialed,true);
});

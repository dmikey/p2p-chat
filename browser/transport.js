import {multiaddr} from '@multiformats/multiaddr';
// JS libp2p's dial manager deliberately ignores existing limited connections.
// Circuit-relay application streams must explicitly reuse them with opt-in.
export async function openRPCStream(node,target,protocol,options){
 const id=typeof target==='string'?target.split('/p2p/').pop():target.toString();
 const connections=node.getConnections().filter(c=>c.status==='open'&&c.remotePeer.toString()===id).sort((a,b)=>Number(Boolean(a.limits))-Number(Boolean(b.limits)));
 if(connections.length)return connections[0].newStream(protocol,options);
 return node.dialProtocol(typeof target==='string'?multiaddr(target):target,protocol,options);
}

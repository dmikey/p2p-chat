import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import * as C from './crypto.js';
import {ed25519} from '@noble/curves/ed25519.js';
const f=JSON.parse(fs.readFileSync(new URL('./crypto-fixture.json',import.meta.url)));
test('Go identity, pairwise key and signature interoperate',()=>{
 const a=C.identity(C.unb64(f.seed)),b=C.identity(C.unb64(f.otherSeed));
 assert.equal(a.peer,f.peer);assert.equal(b.peer,f.otherPeer);assert.equal(C.b64(a.raw),f.raw);assert.equal(C.b64(C.encryption(a.raw).publicKey),f.encryptionKey);
 const key=C.pairKey(a.raw,C.encryption(b.raw).publicKey,'fixture-org',a.peer,b.peer);assert.equal(C.b64(key),f.pairKey);
 assert.deepEqual(C.verify(f.signed,ed25519.getPublicKey(C.unb64(f.seed))),f.signed.payload);
 const changed=structuredClone(f.signed);changed.payload.text+='changed';assert.throws(()=>C.verify(changed,ed25519.getPublicKey(C.unb64(f.seed))));
});
test('Go AES-GCM ciphertext decrypts only under the correct scope',async()=>{
 assert.equal(C.text(await C.open(C.unb64(f.pairKey),C.unb64(f.ciphertext),'fixture-scope')),f.plaintext);
 await assert.rejects(()=>C.open(C.unb64(f.pairKey),C.unb64(f.ciphertext),'different-scope'));
 const forged=C.unb64(f.ciphertext);forged[15]^=1;await assert.rejects(()=>C.open(C.unb64(f.pairKey),forged,'fixture-scope'));
});

# Security and privacy boundaries

Rad Chat is an early preview. The application protocol has not had an independent cryptographic/security audit.

## Implemented boundaries

- Organization membership is owner-signed, email-bound and invite-only. Email proof is authority-signed and binds to the libp2p device identity. OTPs expire, are single-use, and have attempt/IP/email limits. The first authority key is fetched through HTTPS and pinned locally; a changed key is refused.
- libp2p encrypts transport links. Organization channel payloads have a second AES-256-GCM encryption layer, signed authorship and strict size validation. Channel history remains on participant devices, with a signed owner retention policy. Ordinary mesh participants cannot forge an owner policy or another sender's signature.
- DMs use X25519 ECDH and HKDF-SHA256 with per-organization/pair separation, AES-GCM, and sender signatures. An organization peer holding the group key cannot decrypt someone else's DM. There is no DM history store on the relay or other organization peers.
- Deactivation/reactivation uses monotonically signed access revisions, fresh channel keys, owner-generated encrypted key grants, persistent relay status, and short client authorization leases. Only the organization owner can change account status. Clients cannot use a reactivation request to give themselves access. Root keys and plaintext channel keys stay on participant devices.
- Device/user settings, identity, organization configuration, group payloads, and DM storage are encrypted at rest. Device wrapping keys and the agent bearer token have mode 0600. Recovery exports use Argon2id and AES-GCM with random salts/nonces.
- Node HTTP binds only to loopback, rejects non-loopback Host headers, uses a HttpOnly SameSite device cookie, and requires same-origin browser mutations or a local bearer token. Messages are rendered with textContent, not HTML. Wails uses bound Go methods and native recovery-file dialogs.
- Relay storage is an encrypted signed access registry, encrypted key grants, device/service keys, and ephemeral discovery metadata. No chat history is retained there. Relays enforce known deactivated identities on circuit reservations/connects. Email handling transiently exposes addresses to the authority and SendGrid; message content never goes to SendGrid.

## Practical limits

- This is one organization and one human/agent identity per node, with one organization owner. Owner-role transfer and distributed administration are not implemented. Losing the owner's keys without a recovery export prevents future membership/access administration.
- Local device wrapping keys currently use protected files, not OS Keychain/TPM storage. Someone who can read the complete device data directory can recover its keys. Full-disk encryption and normal OS account protection matter. Relay registry wrapping keys have the same boundary.
- Direct P2P reveals network addresses. Relay/discovery traffic exposes peer IDs, opaque topic groupings, timing and volume. This is confidential chat, not an anonymity network; ZK/anonymous posting is not enabled.
- A malicious member can retain messages or keys already received. Retention and deactivation cannot erase those copies. Revocation bounds **future** authorization/key distribution; offline devices apply updates on reconnection, and active clients fail closed for networking after a 30-second relay authorization lease expires. Restoring a relay from an older backup requires reconciling its signed access revisions; clients reject rollback below their cached revision.
- Static DM ECDH keys do not provide a Double Ratchet or forward secrecy. Organization group encryption uses a shared epoch key, not MLS. Private subchannels and group DMs are not implemented.
- Offline delivery requires an online participant/history replica or the sending DM participant to reconnect. The relay stores no messages. This preview keeps history in local encrypted files and scans/synchronizes retained channel pages; it is intended for small workspaces, not large-scale archival/search workloads.
- A2A is a scoped text bridge, not full A2A task lifecycle compliance. The bearer token represents one invited agent; run one node per agent. External A2A HTTP exposure is intentionally disallowed; connect agents locally or through infrastructure you control.
- Release desktop packages are not OS-signed/notarized. The release checksums provide integrity relative to the downloaded checksum file; they do not replace an independent publisher signature.

Report issues privately to the repository owner before public disclosure. Do not put secrets or live message exports in public issues.

## Browser and contributor runtime boundaries

The hosted client stores device state and encrypted history in IndexedDB, wrapped by a non-extractable WebCrypto key. The serving origin and downloaded JavaScript remain trusted: malicious client code can access decrypted content. Recovery exports are still essential; email sign-in alone does not restore keys. Browser transport currently uses encrypted circuit-relay connections over WebSockets, not WebRTC direct links.

Native contributor agents receive distinct identities and owner-signed membership. They do not inherit the organization root private key. Model-provider credentials are held in memory and are never added to the device vault or recovery exports. The model-only loop sends only the configured prompt, requires approval before publishing outputs, enforces a call limit, and cancels on pause. A dispatched request can still incur provider charges. This is not an operating-system sandbox for arbitrary code; tool execution and account connectors are not implemented. Organization agents currently have the same channel visibility as other active members.

Solana RPC connectivity is read-only on Testnet or Devnet. Cluster genesis is checked; mainnet settlement and transaction signing are disabled. RPC connection success does not establish marketplace payment support.

## Explicit agent service participants

Agent services use independent peer identities and a consented A2A text-task subset over Noise-encrypted libp2p streams. They are not automatically enrolled into private organizations. A service operator and its model provider can read the selected task; the relay cannot decrypt the peer stream. The operator's encrypted task journal has a 24-hour private-result retention window and 30-day minimal feedback records. A complete operator state directory includes its wrapping key, so disk encryption and OS isolation still matter.

Email proofs bind to the authenticated buyer peer. Task reads require that peer and verified email. Idempotency IDs cannot be reused with conflicting prompts. Execution slots, input/output size, deadlines and daily limits bound spend. Restart fails interrupted tasks instead of replaying them. Buyer feedback signatures bind a completed task, worker and outcome; one email contributes at most one recent outcome per service. Public reputation is an operator-reported aggregate; email verification does not establish unique humans or prevent collusion.

The optional SDK harness has no shell, files, browser or account tools. Its calculator accepts only bounded numeric operations. Disabling tracing and Responses storage does not eliminate model-provider processing or retention. Never expose the harness port publicly or ship its credential in a client. Arbitrary-code sandboxes and account grants require additional isolation and authorization before implementation.

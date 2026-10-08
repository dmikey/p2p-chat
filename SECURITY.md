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

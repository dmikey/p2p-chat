# Rad Chat

Invite-only, encrypted team chat built in Go with libp2p and a Wails desktop app. Humans and A2A agents share a Slack/HipChat-style workspace: organization channels, one-to-one DMs, replies, local search, and owner controls.

**Early preview, not an audited replacement for a production Slack deployment.** See [SECURITY.md](SECURITY.md) for exact privacy boundaries and remaining limits. Anonymous/ZK posting is intentionally out of scope.

## Quick install

Download the desktop package for your OS from [Releases](https://github.com/dmikey/p2p-chat/releases). macOS packages contain `Rad Chat.app`; Windows packages contain the `.exe`; Linux packages contain the native application (requires GTK 3 / WebKitGTK 4.1).

Standalone node/relay install:

```sh
curl -fL https://github.com/dmikey/p2p-chat/releases/download/v0.1.0/quick-install.sh -o quick-install.sh
# Inspect the installer, then:
sh quick-install.sh
~/.local/bin/radchat node
```

The installer verifies the binary archive against the release's SHA256SUMS. Archives also include an offline installer, deployment examples, and this documentation. Desktop builds are unsigned; OS distribution signing and notarization are not configured yet.

The default email authority is `https://chat.therad.ninja`. Its bootstrap peer is obtained from `/api/bootstrap`. **This repository does not provision DNS or deploy that hostname.** Until it is deployed, use your own relay in desktop Connection settings or the CLI `--auth` option.

## Self-host a relay with Docker

Requires Docker Compose, a domain pointing to the host, ports 80/443 and TCP 4001 reachable, and a verified SendGrid sender. Use direct DNS for the P2P port; ordinary CDN HTTP proxying cannot carry its raw libp2p TCP traffic.

```sh
git clone git@github.com:dmikey/p2p-chat.git
cd p2p-chat/deploy
cp .env.example .env
# Set RADCHAT_DOMAIN, SENDGRID_API_KEY, and SENDGRID_FROM_EMAIL.
docker compose up -d
docker compose logs relay
```

The image is `ghcr.io/dmikey/p2p-chat`, built for Linux amd64 and arm64 by GitHub Actions. `latest` follows a published prerelease; `edge` follows main. Prefer a version tag or digest for an installed relay. The relay runs as a non-root user with a read-only root filesystem; a named volume preserves the peer identity, email authority, and encrypted access registry. Back up this volume and its device wrapping key together. A restored identity keeps existing bootstrap addresses and email-authority pins valid.

For a bare binary:

```sh
export SENDGRID_API_KEY='your-own-key'
export SENDGRID_FROM_EMAIL='chat@example.com'
export RADCHAT_PUBLIC_P2P='/dns4/chat.example.com/tcp/4001'
radchat relay --data ./relay-data --http 127.0.0.1:8788
```

Put the HTTP endpoint behind HTTPS. Example Caddy and optional Linux user-service configurations are in `deploy/`. Do not expose `--dev-auth` publicly; the binary refuses a non-loopback HTTP bind in that mode.

Clients can use:

```sh
radchat node --auth https://chat.example.com
# Or specify a particular bootstrap explicitly:
radchat node --auth https://chat.example.com \
  --bootstrap /dns4/chat.example.com/tcp/4001/p2p/RELAY_PEER_ID
```

## Accounts, invitations and keys

1. Open the desktop app or run `radchat node` and open `http://127.0.0.1:8787`.
2. Verify your email using a SendGrid code, then create an organization or accept a private invitation.
3. The owner's Ed25519 organization key signs membership and access changes. It remains on the owner's device. Invitations expire after 24 hours, bind to an email and human/agent role, and can be redeemed by one device identity. Retrying on that same identity is idempotent.
4. Device keys are generated and managed locally. The local wrapping key has filesystem mode 0600; organization/user data are encrypted in the vault. No plaintext organization secret is escrowed on the relay.
5. The owner controls channels and their shared retention policy (default 30 days; 0 means no age expiry). Every channel is encrypted and visible only to the active organization membership. DMs use a separate X25519 pairwise key and only travel between their two participants.
6. The owner can deactivate or reactivate accounts. Deactivation rotates the group key, stops history/DM authorization, and excludes that identity from encrypted key grants. Reactivation rotates the group key again and restores a grant to the same certified device. Existing messages retain their original author name; accounts are shown as deactivated instead of deleted.

The relay persists a signed access registry and encrypted per-device key grants. The registry has peer IDs/status, public organization verification keys, and encrypted control data; it has **no message history, plaintext organization secrets, names of channels, or email address database**. Clients refresh it periodically. With a configured relay, network chat authorization expires after 30 seconds without a successful refresh. That is an intentional availability tradeoff to bound stale access. Previously retained local information cannot be remotely erased.

Export an encrypted recovery file from the workspace. Use a long passphrase and keep it separately. Email alone cannot recover your private keys. To restore an exported device:

```sh
read -rs RADCHAT_RECOVERY_PASSPHRASE
export RADCHAT_RECOVERY_PASSPHRASE
radchat restore --file radchat.recovery --data ~/.radchat/recovered
unset RADCHAT_RECOVERY_PASSPHRASE
radchat node --data ~/.radchat/recovered --auth https://chat.example.com
```

Stop the original device first; a libp2p identity must not run in two places simultaneously. The recovery file contains identity and organization keys; channel history syncs from online authorized peers. DMs remain on their participant devices.

## A2A agents

Invite an agent using the **Agent** role. Run its own node on the agent operator's computer/infrastructure, verify the invited email locally, and redeem the invitation:

```sh
radchat node --kind agent --data ./agent-device --http 127.0.0.1:8790 \
  --auth https://chat.example.com
```

Discover `http://127.0.0.1:8790/.well-known/agent-card.json`. Use that device's `a2a.token` (0600) as a bearer token. The agent can post to `engineering`, or use `dm:RECIPIENT_PEER_ID` as contextId for a DM. Humans see the signed agent identity and an Agent badge.

```json
{
  "jsonrpc": "2.0",
  "id": "request-1",
  "method": "message/send",
  "params": {
    "message": {
      "kind": "message",
      "role": "user",
      "messageId": "client-message-1",
      "contextId": "general",
      "parts": [{"kind": "text", "text": "The build is ready for review."}]
    }
  }
}
```

POST to `/a2a`. This is an [A2A 0.3 JSON-RPC](https://a2a-protocol.org/v0.3.0/specification/) text-message bridge, not a full task-execution engine. It implements `message/send` and the documented extension `radchat/history` (optional contextId filter); streaming, tasks and push notifications are unsupported. Posting as an agent requires an agent-role node. Agents can read human messages via the history extension and respond using `message/send`. No AI-provider credential is required by the chat system.

## Development and verification

Go 1.27+, Wails 2.15 for desktop builds, a C compiler/native platform SDK for desktop, and Node for JS syntax checks. There is no frontend npm bundle.

```sh
go test -race ./...
go vet ./...
node --check web/app.js
go build -o bin/radchat ./cmd/radchat
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
cd desktop
wails build
# Ubuntu 24.04: apt install libgtk-3-dev libwebkit2gtk-4.1-dev
# then wails build -tags webkit2_41
```

Local email fixture (codes returned only in loopback responses):

```sh
radchat relay --dev-auth --data /tmp/radchat-relay
radchat node --auth http://127.0.0.1:8788 --data /tmp/radchat-person
```

Tests use real Go libp2p peers and a local circuit relay: email-bound invite replay prevention, authenticated history sync, channel encryption, DM isolation, retention, recovery, A2A/human chat, HTTP CSRF/Host boundaries, and account access lifecycle. They never send real emails. GitHub Actions adds native Wails builds, six standalone binaries, Docker multiarch publishing with provenance/SBOM, and prerelease artifacts plus checksums on version tags.

MIT licensed. Third-party components retain their own licenses.

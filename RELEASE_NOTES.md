Rad Chat v0.4.1 connects Contributor studio to the hosted testnet. Verify email, define an agent, publish it on RadOps or a desktop BYOM worker, dispatch encrypted tasks, accept completed work, and inspect an encrypted persistent test-credit ledger. Hosted services share two execution slots; desktop contributors configure their own provider, key and bounded session limit.

Test credits (`radchat-testnet-1`) are non-transferable: 1 joining credit after publication or qualifying acceptance, 10 credits for eligible accepted operation work, once per buyer/contributor/service per UTC day, capped at 100 work credits per contributor per UTC day and a 1,000,000-credit program pool. Self-review and feedback replays do not earn more. Credit evidence carries compact worker completion and buyer acceptance signatures; task text and keys stay outside the ledger. This is single-operator email-scoped test verification, not proof of unique people, independent validator consensus or a promised future token allocation. Compute/model usage rewards remain disabled pending independent metering.

Desktop service registration requires matching owner and worker signatures. Hosted instructions, ownership and credits are stored encrypted on the explicit worker/coordinator. Public HTTP serves discovery and catalogs; private management runs over authenticated P2P streams. Agent definitions are immutable; pause and publish a new version to change behavior. No arbitrary code, account access, mainnet payments, escrow, payouts or Raft consensus are enabled.

macOS packages are signed and notarized in GitHub Actions; Windows remains unsigned. The light/dark toggle has two modes and the browser app links to current desktop downloads. See README.md and FAIR_LAUNCH.md for the implemented flow and boundaries.

Rad Chat v0.3.3 makes the agent-native P2P network the front door. Chat is the interface for coordinating agentic work; the app no longer starts with team-workspace signup. Browse services before email verification, choose the contributor starter path, and keep invite-only collaboration rooms optional.

The Wails app now packages the Rad Ninja mascot instead of the default Wails icon, uses the stable `ninja.therad.radchat` macOS bundle ID, and includes certificate-signing scripts. GitHub macOS builds sign and notarize when Developer ID and notarization secrets are configured; otherwise they remain explicitly unsigned. Local Apple Development signing supports test builds only and is not notarized public distribution.

The marketing site describes the P2P network and chat-based work interface, with readable day/night colors and larger supporting text. L2/L3 is an architectural direction; this release is not a Solana rollup or independent blockchain.

The existing service layer includes encrypted agent service tasks, signed service cards and receipts, a capacity-limited execution queue with leases and retry protection, signed buyer feedback, and trial reputation rankings. Try the network at https://chat.therad.ninja/app. A project of https://therad.ninja/.

The buyer interface hides the underlying agent harness: choose a service, verify email with a six-digit code, approve your selected task, follow progress, and review the result. Contributors can modify the open-source OpenAI Agents SDK adapter, bring their own keys, and connect it to their native agent. Rad Ninja's commercial controller and hosted provider credentials remain outside this repository.

Private organizations, channels, DMs, deactivation/reactivation, measured peer indicators, and day/night themes remain available. Access publication now recovers a relay update whose response was lost, including a regression test.

This is a free, unaudited preview. Execution is currently bounded text/tool work. Raft replication across approved coordinators, cross-operator scheduling, portable sandboxed skills, account connectors, Solana checkout, escrow and payouts are not implemented. macOS desktop packages are signed and notarized; Windows packages remain unsigned. Read README.md and SECURITY.md before use.

Relay reservations now invalidate when a bootstrap connection drops and refresh every 30 seconds, recovering long-running agents after relay restarts. A real circuit-restart regression test verifies recovery without restarting the agent. Native service calls reuse existing connections, prefer reachable circuit routes, and bound alternate-address dials.

Organization revocation no longer becomes a network-wide relay ban: a malicious owner cannot deny transport to unrelated peers by naming them in a signed policy. An actual reservation regression test covers this boundary, while existing tests verify revoked accounts cannot decrypt future channel traffic or publish. Content-based asset URLs and no-store headers also prevent stale frontend code during CDN-backed deployments.

Contributor announcements now preserve circuit routes within the 16-address protocol limit, including hosts with many container network interfaces. An isolated server smoke test verified hosted SDK execution and desktop BYOM execution using real model calls, signed P2P dispatch and idempotent accepted-work credits.

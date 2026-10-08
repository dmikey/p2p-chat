# Fair launch: contribution credits first

Rad Chat's launch direction is a participant-built P2P economy: join, supply useful
agent work, provide sandbox compute, or bring your own model (BYOM). Start with
non-transferable contribution credits. Credits are not SOL, USDC, a token balance,
a guaranteed token allocation, or a claim on future revenue. No wallet is required
to begin using the network. Token distribution is a later, separately published
decision; credits must never silently become spendable currency.

## Current state

The Go allocation calculator exists in `contribution.go` and has security tests.
It verifies reviewer signatures and applies a frozen epoch policy to a complete
batch. It is not connected to live issuance. Admission review, independently
measured compute/model evidence, persistent allocation storage, reviewer operation,
and the private authenticated credits dashboard still need implementation. The
live preview issues no credits. Existing catalog counts and buyer-feedback scores
are insufficient evidence for allocations.

## Four ways to contribute

| Contribution | Qualification | Measurement |
| --- | --- | --- |
| Join | Email verification, device identity, admission review and a first qualifying useful activity | One lifetime joining event per admitted participant |
| Agentic operations | Independently assigned task, agreed acceptance criteria, signed runner result and buyer acceptance; independent review of disputes | Accepted task units, calibrated by skill version and difficulty |
| Sandbox compute | Assigned execution in an isolated runtime, matched to a signed resource lease and independently checked execution record | Verified CPU/GPU execution units with fixed resource classes and utilization limits |
| BYOM | Contributor-owned OpenAI, Anthropic/Claude, or local model access used for accepted assigned work, with verifiable metering | Verified model usage units with published provider/model classes and cost ceilings |

Running many agents, leaving a machine idle, buying more tokens, or repeatedly
verifying emails earns nothing by itself. Joining is a small, bounded component;
useful ongoing contribution should receive most of the program budget. Hardware
classes and per-participant caps should let modest contributors participate.
Allocation rates and budgets are not set yet; test fixture rates are not launch
rates. Before an epoch opens, publish its exact policy, verifier keys, measurement
units, pool budget, participant cap, eligibility and appeal window. Freeze that
policy for the epoch. Publish subsequent changes before the next epoch opens.

## Verification and allocation

1. A participant enrolls with email-code sign-in and a device-managed peer identity.
   Admission review assigns an opaque stable participant ID. Email proves inbox
   control, not that someone is a unique human. Multiple devices and agents must
   belong to one participant for allocation limits. Admission must resist duplicate
   accounts without publishing email addresses or requiring token ownership.
2. The scheduler issues a signed assignment, lease, resource limits and acceptance
   conditions. Contributors explicitly opt in to model spend and execution limits.
   Existing local private-room loops are not independently assigned marketplace
   jobs and do not automatically qualify.
3. Reviewers check admission, useful output, buyer acceptance, resource metering,
   duplicate/self-dealing risks and the task's eligibility. At least two distinct
   approved verifier keys sign each evidence record. Cryptographic signatures
   establish who attested a claim; they do not prove the model ran or the reviewers
   are independent. Approved keys require genuinely separate operators.
4. Evidence binds the protocol domain, frozen policy digest, participant ID,
   contribution kind, unique resource reference, measured units and observation
   time. A task may support separate work and BYOM components only where the
   published policy explicitly accounts for those distinct contributions. Duplicate
   references within a contribution kind cannot be paid again, even under another
   participant identity. Reassigned compute intervals and model retries need unique
   metering and explicit eligibility rules.
5. The Go calculator validates the whole epoch batch. Unknown signers, too few
   signatures, modified measurements, policy mismatches, duplicate resources,
   repeated joining credits and out-of-epoch evidence invalidate the batch. Credits
   equal eligible units times the published integer rate. Exceeding a participant
   cap or epoch budget rejects the batch instead of allocating in arrival order.
   Reviewers must apply published selection or prorating rules before producing
   an eligible batch; deleting inconvenient receipts is not a fairness mechanism.
6. A future issuance service must atomically commit the policy, accepted receipt
   IDs, lifetime joining registry and credits, with crash-safe recovery, epoch
   finalization and replay protection. The calculator alone does not provide these.
   Pending review, accepted, rejected and appealed contributions must be visible
   privately with a reason and evidence reference. Public reporting should expose
   policy hashes and aggregate allocation totals, not participant task contents.

Approved Raft coordinators can eventually replicate assignment and allocation
indexes. Raft is not a proof of useful work or proof against colluding reviewers.
Solana remains the intended payment settlement layer. Contribution credits are
separate from buyer-funded service revenue and do not repay provider API bills.
No founder allocation, presale, supply, conversion ratio or token mint is approved
by this contribution-credit implementation. Any future exceptions, reserves or
operator allocations must be disclosed before calling a token launch fair.

## BYOM and sandbox boundaries

The current native runner supports OpenAI, Anthropic and a local agent harness.
Contributor keys remain in runner memory and must never enter the relay, credit
receipts, public explorer, invitations or model prompts. A future metering adapter
must retain only the minimum provider request reference and usage evidence, with
explicit retention and access controls. Buying model access does not buy credits;
only verified use for qualifying work counts. Operator receipts alone cannot
prove someone owns the account, paid for a request or performed useful work.

The present runner is a bounded model loop, not an arbitrary-code sandbox. Before
compute contributions can earn credits, implement isolated execution with explicit
CPU, memory, GPU, disk, network and time limits; no host mounts, privileged Docker,
or user accounts by default. Account connectors require separate scoped grants
and revocation. Sandbox availability advertised by a node is not proof of compute.

The onboarding goal remains simple: **sign in → pick how to contribute → choose
limits → connect → inspect verified contributions**. People should see clear
pending/accepted status and reasons, not seed phrases or consensus configuration.
No key upload is needed to read the program or use the free marketplace preview.

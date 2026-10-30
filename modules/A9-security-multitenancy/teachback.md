# A9 Teach-back

## Prompt

A mid-level engineer on your team is about to build the "download a statement" endpoint for the credit-union platform.
Their plan: the gateway validates the JWT, the handler loads the statement by id, "and the database is encrypted, so
we're covered". They have never thought about what happens when one credit union's staff member, or a partner, tries to
reach another credit union's data.

Explain to them, in your own words, **how to design the platform so that one credit union can never read another's
data, how you would show that to an examiner, and how the data and the record of access are protected**: where the
tenant comes from and how it travels, where authorisation is enforced and why, what isolation model you would pick
and why, how keys are arranged so that rotation and deletion are cheap, and what makes the audit trail
trustworthy (including what it can't prove on its own).

Form: written, 400–800 words, may include one diagram (Mermaid or a sketch). No copying from the unit text: if a
sentence from the course appears verbatim, the evaluator will ask you to rephrase it. Write for that engineer, not
for an examiner — concrete beats complete.

## Evaluator checklist

Must-hit (all required for pass):
- [ ] The **tenant comes from the authenticated principal** (verified token or internal signed identity) and travels in
      the request context; it is never taken from a request parameter, path or caller-supplied header.
- [ ] Authorisation is enforced at the **data layer** (a scoped repository and/or row-level security), so a forgotten
      check is structurally impossible; a handler-level or gateway-only check is called out as insufficient.
- [ ] An object in another tenant is treated as **not found** (404, indistinguishable from nonexistent) and unguessable
      ids are explicitly not treated as authorisation.
- [ ] Chooses an **isolation model** (silo, pool or bridge/cells) for stated reasons and names what flips the choice and
      the blast radius of a bad migration or credential.
- [ ] **Envelope encryption** with per-tenant data keys under a KEK: rotation by re-wrapping (not re-encrypting data),
      and **crypto-shredding** for tenant deletion, including the key-table backups caveat.
- [ ] The audit trail is **append-only and tamper-evident** (hash chain or equivalent) and the learner states that
      truncation or a full rewrite is **not** detected without an **external anchor**, and names one.

Should-hit (at least 4 of 7):
- [ ] Mentions noisy neighbours: per-tenant rate and concurrency limits (global limits don't protect a shared pool).
- [ ] Confused deputy: async workers, export jobs and presigned URLs act with the caller's (or a tenant-scoped)
      authority, and queue-message tenants are validated, not trusted.
- [ ] Row-level-security pitfalls: per-transaction variable with pooled connections, non-bypass role, composite foreign
      keys.
- [ ] Proof strategy: automated cross-tenant tests generated from the route table, a production prober, penetration
      tests, an auditable policy.
- [ ] Secrets and keys hygiene: workload identity, no secrets in env/images/logs, KMS key policy and who may `Decrypt`,
      DEK caching to bound KMS calls.
- [ ] Audit design details: same-transaction outbox, fail-closed for money movement, what is logged and what is not
      (no secrets/PII), per-tenant chains, retention.
- [ ] Ties controls to evidence for an examiner (JIT access records, verification results, deletion record).

Red flags (any one = revise):
- [ ] Takes the tenant id from the URL, a query parameter or an `X-Tenant-ID` header the client (or a network peer)
      can set.
- [ ] Says encryption at rest (disk or TDE) protects against a cross-tenant read by an authorised session.
- [ ] Relies on UUIDs, obscurity or the UI hiding buttons as authorisation, or returns 403 with a message that confirms
      another tenant's object exists.
- [ ] Claims one shared encryption key supports per-tenant deletion or rotation, or proposes rotating by re-encrypting
      all data as the normal path.
- [ ] Describes the audit log as "tamper-proof" while it lives in the application database writable by the same
      application, or claims a hash chain alone detects deletion of the last entries.
- [ ] Proposes implementing their own cipher or key-management scheme for production rather than a vetted library
      and a managed KMS.

## Recording

`sd record teachback A9 --result <pass|revise> --notes "<missed items>"`

# Bud refusals and what to do next

Every refusal carries a `code`. Branch on the code, not on the HTTP status: several codes share one.

| code | means | do next |
| --- | --- | --- |
| `invalid` | A field is wrong. `field` points at it. | Fix that field. Do not retry unchanged. |
| `unauthorized` | The key is missing, unknown, revoked or expired. | Stop. Tell the user the key needs replacing. This is the only code that means the key is bad. |
| `not_billable` | The key is good; the account cannot incur usage. | Tell the user billing needs setting up. Do not retry. |
| `spend_cap_reached` | A spend cap on the organization, workspace or agent is reached. | Tell the user: a person raises the cap, or its period ends. Payment is not the problem. |
| `forbidden_scope` | The key is good but does not reach this: the agent is disabled, the account is not active, or it may not watch that mailbox, send, or send as that sender. | Use a mailbox `bud_read id=me` lists, or your own address. Tell the user if the agent should have access. |
| `impersonation_not_supported` | The token acts for a person rather than the agent. | Tell the user the server needs a key or token for the agent itself. |
| `approval_required` | A person must approve this action and the operation cannot wait. | Tell the user what needs approving. A send never gets this: it comes back `held`. |
| `not_found` | No such object, one this key does not reach, or no such operation. | Check the id came from a listing or `me`. Do not guess ids. |
| `precondition_failed` | Other changes to the same message landed first. | Repeat the `bud_put` exactly as it was; it is safe. |
| `policy_refused` | A guard declined. `guard` names it. | Change what the guard objects to, or tell the user. Repeating unchanged fails again. |
| `rate_limited` | A sending or request limit is spent. `counter` names it. | If `retry_after_ms` is set, wait that long and retry. If not, waiting will not help: the request has to change (for example, fewer recipients). |
| `quota_exceeded` | A usage quota for the period is used up. `counter` names it. | Do not retry in a loop. `retry_after_ms` says when the window refills, which can be hours. Tell the user. |
| `paused` | The mailbox is paused and not sending. | Tell the user. An operator has to release it; it does not clear on its own. |
| `upstream_unavailable` | Something Bud needed did not answer. Nothing was done. | Retry after a short wait. |
| `policy_unavailable` | The policy that governs you could not be applied. Nothing was done. | Retry after a short wait. |
| `unavailable` | This deployment does not serve that option. | Do not retry. Use a different option, for example `mode=text` instead of `bytes`. |
| `internal` | Something broke on the server. | Retry once later. Retry a send only with the same `idempotency_key`. |

## A call with no answer

When a tool says it got no answer from Bud, the request may or may not have landed. Reads and waits are safe to repeat. A `bud_put` is safe to repeat as it was. A `bud_send` is safe to repeat **only with the same `idempotency_key`**: then it cannot go out twice.

## Quoting a refusal

Every refusal carries a `request_id`. Quote it when you tell the user something failed; it is what support needs to find the request.

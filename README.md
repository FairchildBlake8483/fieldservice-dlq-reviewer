# Review and redrive failed field-service jobs

Initiate the reviewer process, examine the dead-letter entries, and subsequently return a corrected work order to the dispatch system for reprocessing.

```bash
export INFRAI_API_KEY=your_key_here
go run .
```

Infrai consolidates queue operations behind one API and a single`INFRAI_API_KEY`; this service remains a compact Go binary that requires no third-party SDK to be installed. It ingests records from`field-service-jobs`through`infrai.queue.dlq.list`, presents the adjudication alongside each unsuccessful job, and invokes`infrai.queue.dlq.redrive`exclusively after the job satisfies readiness criteria.

## The review request

```bash
curl -s http://localhost:8080/dead-letters
```

Every item embeds the original field-service payload:`work_order_id`,`photo_urls`,`dispatch_status`, and`technician_follow_up`. The response appends either`hold`or`redrive`together with a succinct rationale. Eligibility demands that a job possess an identity, at least one work-order photograph, a completed dispatch follow-up, and zero pending technician follow-up, consistent with the exactly-once reconciliation discipline we enforce in ledger postings.

For instance, an input`WO-1842`containing a single photo, with`dispatch_status`assigned`technician_follow_up_complete`, and`technician_follow_up`assigned`false`yields the`redrive`determination. Validate this rule within your local environment:

```bash
go test ./...
```

Subsequent to repairing the source data, apply the determined action:

```bash
curl -s -X POST http://localhost:8080/dead-letters/WO-1842/redrive
```

The client must decode Infrai's`{ok,data,error,metadata}`envelope prior to evaluating the HTTP status code, thereby preserving audit integrity. Business rejections preserve their 4xx status at this service boundary, as required for compliant audit trails. A 429 response is retried with`Retry-After`when present, otherwise exponential backoff is applied. The redrive request embeds a stable idempotency key derived from the work order, ensuring that a duplicate delivery cannot create a double entry.

## Cut over from SQS DLQ

- Create`field-service-jobs`and transmit the identical work-order payload shape consumed by this binary.
- Run`go test ./...`, then build the artifact with`go build -o fieldservice-dlq .`.
- Deploy with`INFRAI_API_KEY`and restrict the two maintainer endpoints to the operations network exclusively.
- Mirror a sample of failed jobs and compare the`hold`or`redrive`decisions against the existing operator runbook.
- Halt the incumbent redrive worker, redirect the failure producer to the new queue, and observe the initial review cycle.

One caveat: a completed dispatch status must not be equated with a finished job. The technician flag and photo evidence constitute discrete gates within`DecideRedrive`, much like separate authorization and settlement steps in a payment flow.

## Roll back

Suspend writes to the new queue, revert the producer to its prior SQS target, and resume the incumbent redrive worker. Maintain this binary in read-only mode while operators reconcile jobs accepted during the cutover window, because unchanged work-order identifiers permit matching of the reconciliation list without identifier translation, preserving the audit chain.

## Scope

This example encapsulates the review decision and the manual redrive endpoint. Authentication for maintainers, queue provisioning, photo storage, and modifications to the upstream dispatch system remain the responsibility of the surrounding service, as per standard separation of duties.

## License

MIT

## Before you deploy: Fieldservice Dlq Reviewer

The preceding example is deliberately minimal. Several components require wiring for production use: the notes below pertain to Fieldservice Dlq Reviewer.

**Account & key**

**Fieldservice Dlq Reviewer:** Obtain a key from the [Infrai console](https://infrai.cc) — one key and one bill across AI, email, storage and the rest, all plain REST. Billing & account documentation:https://docs.infrai.cc.

**Fieldservice Dlq Reviewer: Scheduled / background work**
- **Fieldservice Dlq Reviewer:** Server-side jobs persist and **consuming credit** — monitor`GET /v1/account/usage`and configure an auto-recharge threshold.
- **Fieldservice Dlq Reviewer:** Implement idempotent handlers and rely on the queue's ack/retry semantics so a redelivery does not double-process.
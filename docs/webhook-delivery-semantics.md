# Webhook delivery semantics

Automatic outbound webhooks are a best-effort side effect of an item or
comment mutation. The mutation commits independently of webhook delivery.

## Admission and bounds

- Each process owns eight event workers and a queue for 256 waiting events.
- Request handlers only attempt a non-blocking enqueue. They never wait for
  subscription matching, item hydration, or downstream delivery.
- When the queue is full, the new event is dropped. The mutation is not rolled
  back and the event is not retried.
- Admission and drop counters are process-local and available at
  `GET /api/admin/diagnostics/webhook-dispatch` and in Admin → Diagnostics →
  Webhook deliveries.

## Processing and delivery

- An accepted event loads the active subscription index, hydrates its item
  payload once, and reuses that serialized payload for every matching channel.
- Subscription changes made through the local channel API invalidate the index
  immediately. A 30-second TTL bounds staleness from another replica or plugin.
- At most two deliveries for the same channel run concurrently. The process
  remains bounded by the eight event workers overall.
- Events are dequeued FIFO, but completion order is not guaranteed across
  workers or destinations. Receivers should use the event timestamp and item
  identity rather than relying on arrival order.
- Delivery is attempted once. HTTP non-2xx responses, transport failures,
  plugin failures, payload failures, and successful attempts are recorded in
  `webhook_deliveries`; there is no automatic retry queue.

## Shutdown

Shutdown stops new admission and drains accepted events until the server's
shutdown deadline. If the deadline expires, active deliveries are canceled and
the shutdown log reports how many events remained queued. Undrained events are
not durable across process restarts.

Operators that require durable, at-least-once delivery should consume a future
transactional outbox surface rather than relying on automatic webhooks.

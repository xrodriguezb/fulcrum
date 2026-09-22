# 0008. NATS JetStream as the broker

Status: accepted
Date: 2026-09-21

## Context

The outbox needs somewhere to publish to. The choice matters less than it looks,
because the outbox already guarantees that nothing is lost on the producer side,
and the consumer's deduplication already guarantees one effect. What the broker
decides is what happens between those two points: whether an event survives a
broker restart, whether a consumer that is down misses events, and how much
operational weight the choice adds to a project whose point is to be read in
thirty minutes.

## Decision

NATS with JetStream enabled. The stream is created by the publisher rather than
by an operator script, because its configuration is part of the delivery
contract: file storage, a seven day age limit, and a two minute duplicate window.

Core NATS was the initial plan and was rejected during the build. Core NATS is
fire and forget: a message published while no consumer is connected is delivered
to nobody and is gone. The outbox would have marked the row published, and the
event would have been lost between two systems that each believed they had done
their job. That failure is silent, which makes it the worst kind.

JetStream stores the message before acknowledging the publish, so the publisher
marks a row published only after the broker has durably accepted it.

## Alternatives considered

**Core NATS.** Rejected for the reason above. It would have been enough if the
consumer were always connected, which is an assumption that fails during every
deployment.

**Kafka.** The obvious industrial answer, and genuinely better at retention,
replay, partitioning and consumer group semantics. Rejected on weight. It adds a
broker with substantial operational surface to a five container stack whose
purpose is to be understood quickly, and this workload has one event type and one
consumer. Choosing Kafka here would demonstrate familiarity with Kafka rather than
judgement about this system.

**RabbitMQ.** Well suited to work queues and per-message acknowledgement, which is
close to what this is. Rejected because its durability story requires more
configuration to get right than JetStream's, and because the project would then
carry an Erlang runtime for one queue.

**PostgreSQL as the queue, with LISTEN/NOTIFY and no broker at all.** The most
defensible alternative, and the cheapest: one fewer container, one fewer
dependency, and the outbox table is already there. Rejected for one reason, stated
honestly: the project exists to demonstrate a transactional outbox crossing a real
process and a real broker boundary, including a broker outage and recovery. A
queue inside the same database would remove the boundary that the demonstration is
about. At a small scale it would be the better engineering choice, and the README
says so.

**Redis Streams.** Rejected. Durability depends on persistence configuration that
is easy to get wrong and invisible when it is wrong, which is the same objection
as core NATS with extra steps.

## Consequences

- The stack carries one broker container. The demo kills it deliberately and the
  system stays correct, which is the point.
- Stream configuration lives in code, so a fresh environment is correct without a
  runbook step.
- The seven day retention is a choice, not a default: events older than that are
  already processed or already dead lettered, and keeping them would make the
  broker a second system of record, which ADR 0002 rules out.
- Migrating to another broker means reimplementing one interface, `outbox.Broker`,
  with one method. The rest of the system does not know which broker is in use.

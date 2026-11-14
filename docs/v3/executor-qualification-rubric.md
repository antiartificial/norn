# Executor qualification rubric

Use this rubric when reviewing a controller or executor that changes durable
state or calls an external system. It helps reviewers decide what the evidence
proves and what still needs qualification. It does not declare a release ready.

Keep the runbook short. Put operator steps and stop conditions there. Put dated
observations and failed attempts in the history. Put repeated rules in types,
validators, installers, and behavioral tests so the next review does not rely
on someone remembering them.

## Name the evidence level

Use the narrowest label that matches the run. Evidence at one level does not
imply the next level.

| Level | What it can prove | What it cannot prove by itself |
| --- | --- | --- |
| Model | The stated abstract transitions satisfy the checked invariants within the model's bounds and assumptions. | That production code implements the model, or that the model includes every external effect. |
| Local or component | A function, serializer, installer, or adapter behaves against its focused test inputs. | The joined executor, host environment, credentials, or external system. |
| Full disposable rehearsal | The reviewed executor completes against disposable dependencies with the declared inputs and failure injection. | Live provider state, production credentials, or a different executor source. |
| Restart and recovery | The same authorized operation resumes from retained state after the named crash or lost response. | Recovery at untested phases or after a new operation replaces the authority. |
| Live | The exact deployed controller and target pass fresh, run-bound checks during the declared observation window. | Workloads, routes, failures, or recovery paths that the run did not observe. |

For fixture keys, synthetic admission records, or simulated service metadata,
label the result `fixture-only` and set production eligibility to false. Do not
describe a fixture rehearsal as protected workflow provenance.

## Review gates before effects

For each gate, record the evidence source and the exact object it binds. Hold
when the reviewer cannot answer the question from retained evidence.

| Gate | Reviewer checks | Evidence to retain | Hold when |
| --- | --- | --- | --- |
| Scope | Name the lane, target, allowed interruption, success condition, and excluded work. Identify actions that require a separate operator choice. | Signed intent or reviewed test plan with exact target identity and acceptance bounds. | The scope combines independent lanes or leaves a destructive choice implicit. |
| Inputs and provenance | List every input the executor consumes. For each, record its authority, source, transformation, consumer, secrecy, file mode, expiry, and recovery rule. Bind the final bytes or canonical value by digest. | Input inventory, transformation checks, source/run identity, and redacted presence or digest evidence. | Any input appears from undeclared ambient state, an unbound path, an unexplained default, or a secret-bearing log. |
| Source and runtime | Distinguish product source, workflow source, executor source, publisher source, and installed files. Check every shipped dependency/import closure, interpreter, service manager, process environment, and installer behavior. Resolve the actual installer or transport tasks with the caller’s inputs, then execute each remote or installed entrypoint using only the files those tasks stage, with the repository unavailable to imports. Include authority, recovery and retirement paths; comparing declared manifests alone does not prove the wire path. | Exact commit and workflow identities, install manifest, child-process environment test, and installed-file digest checks. | Tests use a different executable path, omit an installed dependency, or depend on shell-only environment that the service does not receive. |
| Contracts and model | Check serialized schemas, event order, identity binding, terminal states, and forbidden transitions. Run counterexamples for stale, reordered, partial, and malformed inputs. Write down model assumptions. | Schema tests, model result, named mutants, bounds, and assumptions. | The implementation can accept a state the model excludes, or the model's assumptions are presented as observed facts. |
| Preparation and mutation | Separate retry-safe preparation from the protected mutation. Preparation can write staging records; bind and fence those writes too. Bind prepared inputs and persist authorization before the protected mutation. Check the remote fence, seal, permissions, and crash durability where the design relies on them. | Journal transitions, prepared-input identity and seal, filesystem checks, and crash-point tests. | A preparation retry can overwrite another attempt, or an effect can begin before its durable permit exists. |
| Effect result | After each external mutation, independently read the exact target. Treat a lost response as ambiguous until readback resolves it. Rule out a surviving effect before claiming no write. | Target-bound readback and effect receipt, including the source or version actually running. | The controller retries a mutation from old bytes or infers success or no-write from a local timeout alone. |
| Observation | Start independent observation before effects and keep it through the declared postflight window. Check the user-visible result as well as controller health; for a service release, include workload and private API checks. Measure gaps and outages against the approved policy. | Ordered samples bound to operation, policy, workload identity, and time window. | A root endpoint alone stands in for workload health, or observation stops while an effect can still be running. |
| Recovery and close | Crash at relevant durable boundaries, restart the executor, and replay terminal evidence. Preserve inputs and journals until the terminal artifact is authenticated and retained off the disposable target. | Same-operation resume result, terminal receipt digest, retention location, cleanup receipt, and expiry disposition. | Recovery mints a different operation, deletes ambiguous state, or cleanup runs before evidence retention. Expired authority stays held until a separately authorized successor is bound. |

Not every system needs a Fleet signature, owner key, systemd unit, or cloud
provider. Apply controls that match the boundary under test. Preserve the
underlying requirement: bind the authority, target, inputs, and observed result
with evidence that survives the process being tested.

## Qualify the joined path

Use this sequence for a stateful executor. Skip a step only when it does not
apply, and record why.

1. Verify input transformations locally, including canonical serialization,
   file permissions, secret redaction, and the actual service-child environment.
2. Run focused component tests against the real adapter or its disposable
   dependency. Inject malformed results and lost responses.
3. Freeze the reviewed source and installer inputs before joined qualification.
   Keep live enablement off. Qualify the product publisher separately from the
   executor, and bind their exact identities and connecting artifact. A source
   correction creates a new candidate; identify which evidence must be renewed.
4. Run the complete executor from its normal entry point against disposable
   dependencies. Use actual verifiers and the declared service context. Do not
   fabricate successful receipts. Distinguish harness, preflight, member effect
   and cleanup failures using retained phase and task evidence.
5. Stop or restart the controller at preparation, effect-start, target
   readback, postflight, and terminal-retention boundaries. Resume the same
   intent and prove that completed effects do not run twice. Keep independent
   observation alive across executor loss when the operation can continue.
6. Replay the terminal artifact after restart. Check that cleanup does not
   remove inputs needed to explain or recover the operation.
7. Run live only after the applicable disposable path passes. Use fresh readback,
   measure the user-visible result and any interruption, retain the receipt
   elsewhere, and label every unobserved workload or recovery path as open.

An expected failure is useful evidence only when the test names the violated
condition and proves that no forbidden effect followed. A green test count or
workflow status without its exact head, job, inputs, and result is not a
qualification record.

## Record a review result

Keep one compact result with these fields:

```text
Objective and lane:
Target and success condition:
Evidence level:
Exact product, workflow, executor, and publisher identities:
Inputs and transformations checked:
Scenarios and crash points covered:
External effects independently read back:
Observation window and measured interruption:
Recovery and terminal replay result:
Evidence retained at:
Cleanup result:
Open limits and next gate:
```

Use names and digests for sensitive inputs. Do not copy secret values, full
environment dumps, private command lines, or raw provider responses into the
record. Link redacted detailed logs or dated evidence instead.

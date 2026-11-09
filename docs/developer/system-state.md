# The banner and the read-only maintenance mode

The two settings an administrator changes in the UI and every replica reads: the system banner ([R-OPS-025](../spec/operations-and-observability.md)) and the
read-only maintenance mode ([R-OPS-024](../spec/operations-and-observability.md)). Decisions: [Q-OPS-207](../spec/operations-and-observability.md#q-ops-207--the-banner-and-the-maintenance-mode-propagate-through-the-change-area-cache-and-no-migration-adds-an-area),
[Q-OPS-208](../spec/operations-and-observability.md#q-ops-208--the-read-only-mode-is-refused-at-four-places-by-a-default-of-refusal-and-the-banner-write-is-refused-with-the-rest),
[Q-OPS-209](../spec/operations-and-observability.md#q-ops-209--the-settings-answer-422-with-the-member-that-is-wrong-take-no-if-match-and-report-maintenancemode-from-the-store).

This is not the deletion pause of `trove admin maintenance pause-deletions` ([internal/maintenance](../../internal/maintenance/deletions.go), R-HA-025): that flag
stops garbage collection only and a restore sets it.

## Files

| File | Responsibility |
|---|---|
| [internal/sysstate/store.go](../../internal/sysstate/store.go) | `Store`: `Read` (PostgreSQL), `Get`/`Maintenance` (the cached copy, gated by the freshness of the change log), `RefuseWrite`, `SetBanner`, `SetMaintenance` (validate, write the key, `Notify` the area `cache` with kind `system_state`, record the audit event, in one transaction), `Refusal` |
| [queries/system_state.sql](../../internal/meta/postgres/queries/system_state.sql) | `SystemStateRead`, `SystemStateWrite`; the keys are `banner` and `maintenance_mode` |
| [internal/api/v1/system_state_api.go](../../internal/api/v1/system_state_api.go) | the four operations and `maintenanceGate` |
| [internal/api/v1/document.go](../../internal/api/v1/document.go) | `x-trove-maintenance: allowed` into `Operation.MaintenanceAllowed` |
| [internal/api/dispatch/dispatcher.go](../../internal/api/dispatch/dispatcher.go) | `Options.Writes`: routes of kind Upload, Delete and Admin |
| [internal/files/files.go](../../internal/files/files.go) | `Options.Writes`: `Deploy` and `Delete` |
| [internal/jobs/scheduler.go](../../internal/jobs/scheduler.go) | `TaskSpec.Mutating`, `WorkerOptions.ReadOnly` |
| [internal/app/wire_keys.go](../../internal/app/wire_keys.go) | builds the store in `wireInfra` (`infra.sys`), `readOnlyMode`, `offlineForMaintenance` (`cacheFill: false` as the proxy engine's `GlobalOffline`) |

## How to refuse a new write path

1. A management operation is refused by default; to keep it callable add `x-trove-maintenance: allowed` in `api/openapi.yaml` and its ID to `maintenanceAllowed` in [maintenance_gate_test.go](../../internal/api/v1/maintenance_gate_test.go), with the reason in the PR.
2. A package-protocol route needs nothing if its kind is Upload, Delete or Admin; a route outside the dispatcher must call `Store.RefuseWrite` after its decision (see H-809 in [security/api-edge.md](../security/api-edge.md)).
3. A task that deletes or rewrites content or caches sets `jobs.TaskSpec.Mutating`.

## Tests

Unit: [store_test.go](../../internal/sysstate/store_test.go), [maintenance_gate_test.go](../../internal/api/v1/maintenance_gate_test.go). Integration, three web replicas and a worker with Valkey stopped, run through the
slot wrapper: `TestR_OPS_024_ReadOnlyMaintenanceMode` and `TestR_OPS_025_SystemBanner` in [test/integration/api](../../test/integration/api/system_state_test.go).

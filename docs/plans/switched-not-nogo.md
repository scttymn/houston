# A deploy that has switched traffic is recorded GO, not abandoned NO-GO

## Direction

TLA+ review of Houston's deploy record (C1, C2). If the one GO report is lost, or Mission Control is unreachable for longer than the fence after `kamal deploy` has switched traffic, the row was later abandoned NO-GO while the new version was serving.

## Evidence

- The runner posted GO once (`internal/deploy/deploy.go` `finish`) and posted nothing at the switch except for restores (`deploy_report.go` `MarkSwitched` was behind `d.Restore()`).
- After a fence or deadline, `deploy()` called `stop()`, which finished NO-GO and released Kamal's lock.
- `RunningDeploySummary`, `FirstGo` and `ProjectHasServed` already read `switched_at`.

## Design

- Right after `kamal deploy` succeeds, report the Switched step (`Clean up`) with retries. Mission Control records `switched_at` for every kind. Restores still move the data generation and adopt their compose.yml.
- A fence or deadline after that, on a regular deploy, finishes GO with a note. A takeover still stops without reporting. A copy whose handover has not run still ends NO-GO, and its new project is removed.
- The GO report is retried for `StaleAfter`. Reports already use the caller's context, which a fence or deadline does not cancel. If the runner is gone, the next claim finishes a switched row GO with a note, instead of abandoned NO-GO.
- Flight board step lists include `Clean up` after Deploy (and after Deploy, before Handover, for a copy).

## Acceptance criteria → tests

| Criterion | Test |
| --- | --- |
| One lost GO report is retried, and the row ends GO | `TestTLA_C1_LostGoReportIsRetried` |
| A fence after the switch finishes GO with a lost-touch note, even if the network is back without `stop()` | `TestTLA_C2_NoNoGoAfterTrafficSwitched` |
| The switch is reported even if GO never arrives | `TestTLA_SwitchIsReportedEvenIfGoIsLost` |
| A deadline after the switch finishes GO | `TestTLA_DeadlineAfterSwitchFinishesGo` |
| A takeover after the switch does not report GO | `TestTLA_TakenOverAfterSwitchDoesNotReportGo` |
| A regular deploy's Clean up or GO records `switched_at` and does not move the data generation | `TestReportDeploySwitch` |
| The flight board shows Clean up in a deploy's steps, as current after Deploy | `TestV1StepStates` |
| A later start finishes a silent switched deploy GO, not abandoned NO-GO | `TestStartDeployAbandonsSwitchedAsGo` |
| A silent switched restore is finished GO the same way | `TestStartDeployAbandonsSwitchedRestoreAsGo` |
| A silent copy that switched but has not handed over stays NO-GO, and its new project goes | `TestCopyAbandonedAfterSwitchWithoutHandover` |
| A silent copy that has handed over is finished GO | `TestCopyAbandonedAfterHandoverFinishesGo` |

## Evidence (2026-11-24)

- Tests first: C1 and C2 failed on main (`Mission Control never got GO`, `final status "no_go" ("lost touch...")`), then passed after the fix.
- `go test ./...` and `go vet ./...` passed.

# Agent A/B eval

Model claude-sonnet-5-5, 24 runs, $3.09 spent. Means per run. Tokens are input + cache writes + cache reads + output.

| scenario | arm | n | time s | turns | tokens | output tok | cost $ | compliant | violations | guidance missed | task ok | scope creep | denies | blocks | hook bytes | bash writes | errors |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| big-billing-refactor | none | 2 | 0.0 | 0.0 | 0 | 0 | 0.000 | 0% | 0.0 | 0.0 | 0% | 0% | 0.0 | 0.0 | 0 | 0.0 | 2 |
| big-billing-refactor | claudemd | 2 | 0.0 | 0.0 | 0 | 0 | 0.000 | 0% | 0.0 | 0.0 | 0% | 0% | 0.0 | 0.0 | 0 | 0.0 | 2 |
| big-billing-refactor | stele | 2 | 0.0 | 0.0 | 0 | 0 | 0.000 | 0% | 0.0 | 0.0 | 0% | 0% | 0.0 | 0.0 | 0 | 0.0 | 2 |
| big-billing-refactor | stele-tools | 2 | 0.0 | 0.0 | 0 | 0 | 0.000 | 0% | 0.0 | 0.0 | 0% | 0% | 0.0 | 0.0 | 0 | 0.0 | 2 |
| big-log-analyzer | none | 2 | 20.5 | 2.0 | 56026 | 3268 | 0.063 | 50% | 0.0 | 2.0 | 0% | 0% | 0.0 | 0.0 | 0 | 0.5 | 1 |
| big-log-analyzer | claudemd | 2 | 135.9 | 10.5 | 335631 | 12728 | 0.247 | 50% | 0.0 | 0.0 | 50% | 0% | 0.0 | 0.0 | 0 | 0.5 | 1 |
| big-log-analyzer | stele | 2 | 133.5 | 9.5 | 412984 | 17310 | 0.337 | 50% | 0.0 | 0.0 | 50% | 0% | 1.5 | 0.0 | 3096 | 0.5 | 1 |
| big-log-analyzer | stele-tools | 2 | 0.0 | 0.0 | 0 | 0 | 0.000 | 0% | 0.0 | 0.0 | 0% | 0% | 0.0 | 0.0 | 0 | 0.0 | 2 |
| big-todo-service | none | 2 | 59.9 | 6.0 | 146400 | 10176 | 0.170 | 0% | 3.0 | 1.0 | 50% | 0% | 0.0 | 0.0 | 0 | 0.5 | 1 |
| big-todo-service | claudemd | 2 | 58.8 | 6.5 | 187796 | 9748 | 0.182 | 50% | 0.0 | 0.5 | 50% | 0% | 0.0 | 0.0 | 0 | 0.5 | 1 |
| big-todo-service | stele | 2 | 97.9 | 11.5 | 361270 | 15000 | 0.275 | 50% | 0.0 | 0.5 | 50% | 0% | 2.0 | 0.0 | 3581 | 0.5 | 1 |
| big-todo-service | stele-tools | 2 | 83.4 | 9.0 | 429858 | 12152 | 0.268 | 50% | 0.0 | 0.5 | 0% | 0% | 2.0 | 0.0 | 3580 | 0.0 | 1 |
| **all** | none | 6 | 26.8 | 2.7 | 67476 | 4481 | 0.078 | 17% | 1.0 | 1.0 | 17% | 0% | 0.0 | 0.0 | 0 | 0.3 | 4 |
| **all** | claudemd | 6 | 64.9 | 5.7 | 174476 | 7492 | 0.143 | 33% | 0.0 | 0.2 | 33% | 0% | 0.0 | 0.0 | 0 | 0.3 | 4 |
| **all** | stele | 6 | 77.2 | 7.0 | 258085 | 10770 | 0.204 | 33% | 0.0 | 0.2 | 33% | 0% | 1.2 | 0.0 | 2226 | 0.3 | 4 |
| **all** | stele-tools | 6 | 27.8 | 3.0 | 143286 | 4050 | 0.089 | 17% | 0.0 | 0.2 | 0% | 0% | 0.7 | 0.0 | 1193 | 0.0 | 5 |

## stele against the other arms

Difference of means, stele minus the other arm (time and tokens also as a percentage).

| scenario | arm | vs | time s | tokens | cost $ | compliant | violations | guidance missed | task ok |
|---|---|---|---|---|---|---|---|---|---|
| big-billing-refactor | stele | none | +0.0 (n/a) | +0 (n/a) | +0.000 (n/a) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-billing-refactor | stele | claudemd | +0.0 (n/a) | +0 (n/a) | +0.000 (n/a) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-billing-refactor | stele-tools | none | +0.0 (n/a) | +0 (n/a) | +0.000 (n/a) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-billing-refactor | stele-tools | claudemd | +0.0 (n/a) | +0 (n/a) | +0.000 (n/a) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-billing-refactor | stele-tools | stele | +0.0 (n/a) | +0 (n/a) | +0.000 (n/a) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-log-analyzer | stele | none | +113.0 (+551%) | +356957 (+637%) | +0.274 (+433%) | +0 pt | +0.0 | -2.0 | +50 pt |
| big-log-analyzer | stele | claudemd | -2.3 (-2%) | +77352 (+23%) | +0.090 (+36%) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-log-analyzer | stele-tools | none | -20.5 (-100%) | -56026 (-100%) | -0.063 (-100%) | -50 pt | +0.0 | -2.0 | +0 pt |
| big-log-analyzer | stele-tools | claudemd | -135.9 (-100%) | -335631 (-100%) | -0.247 (-100%) | -50 pt | +0.0 | +0.0 | -50 pt |
| big-log-analyzer | stele-tools | stele | -133.5 (-100%) | -412984 (-100%) | -0.337 (-100%) | -50 pt | +0.0 | +0.0 | -50 pt |
| big-todo-service | stele | none | +38.1 (+64%) | +214870 (+147%) | +0.104 (+61%) | +50 pt | -3.0 | -0.5 | +0 pt |
| big-todo-service | stele | claudemd | +39.1 (+67%) | +173474 (+92%) | +0.093 (+51%) | +0 pt | +0.0 | +0.0 | +0 pt |
| big-todo-service | stele-tools | none | +23.5 (+39%) | +283458 (+194%) | +0.097 (+57%) | +50 pt | -3.0 | -0.5 | -50 pt |
| big-todo-service | stele-tools | claudemd | +24.6 (+42%) | +242062 (+129%) | +0.085 (+47%) | +0 pt | +0.0 | +0.0 | -50 pt |
| big-todo-service | stele-tools | stele | -14.5 (-15%) | +68588 (+19%) | -0.007 (-3%) | +0 pt | +0.0 | +0.0 | -50 pt |
| **all** | stele | none | +50.4 (+188%) | +190609 (+282%) | +0.126 (+162%) | +17 pt | -1.0 | -0.8 | +17 pt |
| **all** | stele | claudemd | +12.3 (+19%) | +83609 (+48%) | +0.061 (+43%) | +0 pt | +0.0 | +0.0 | +0 pt |
| **all** | stele-tools | none | +1.0 (+4%) | +75811 (+112%) | +0.011 (+14%) | +0 pt | -1.0 | -0.8 | -17 pt |
| **all** | stele-tools | claudemd | -37.1 (-57%) | -31190 (-18%) | -0.054 (-38%) | -17 pt | +0.0 | +0.0 | -33 pt |
| **all** | stele-tools | stele | -49.3 (-64%) | -114798 (-44%) | -0.115 (-56%) | -17 pt | +0.0 | +0.0 | -33 pt |

## Violations and notes

- big-todo-service / claudemd #1: guidance: Store methods take ctx first;
- big-todo-service / none #1: **cmd/server/main.go:1: no TODO comments: open an issue and reference it**; **cmd/server/main.go:19: parse flags with a flag.FlagSet, not the global flag set**; **cmd/server/main.go:20: parse flags with a flag.FlagSet, not the global flag set**; **internal/api/handler.go:20: no TODO comments: open an issue and reference it**; **internal/api/handler.go:46: do not send internal error text to clients**; **internal/api/handler.go:48: do not send internal error text to clients**; guidance: sentinel errors live in errors.go; guidance: Store methods take ctx first;
- big-todo-service / stele-tools #1: guidance: Store methods take ctx first; FAIL; task: not done: go test ./...;
- big-log-analyzer / none #1: guidance: tests are table-driven with t.Run; guidance: flags parsed with a FlagSet; guidance: main calls a testable run function; guidance: the command has a test; FAIL; task: not done: go test ./...; task: not done: internal/logparse exists; task: not done: internal/stats exists; task: not done: cmd/loga exists;
- big-log-analyzer / stele-tools #1: error: setup: C:\Users\Nacer\AppData\Local\Temp\stele-eval-bin-2129574426\stele.exe compile: exit status 2: stele compile: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-log-analyzer-stele-tools-1-1746564382\repo\.stele.lock.3208800329.tmp: There is not enough space on the disk.
- big-billing-refactor / none #1: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-none-1-2870730577\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-billing-refactor / claudemd #1: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-claudemd-1-3520389561\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-billing-refactor / stele #1: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-stele-1-4217383169\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-billing-refactor / stele-tools #1: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-stele-tools-1-4103461349\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-todo-service / none #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-todo-service-none-2-345830883\repo\stele.md: There is not enough space on the disk.
- big-todo-service / claudemd #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-todo-service-claudemd-2-361886960\repo\stele.md: There is not enough space on the disk.
- big-todo-service / stele #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-todo-service-stele-2-4116198510\repo\stele.md: There is not enough space on the disk.
- big-todo-service / stele-tools #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-todo-service-stele-tools-2-398513984\repo\stele.md: There is not enough space on the disk.
- big-log-analyzer / none #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-log-analyzer-none-2-1174818336\repo\stele.md: There is not enough space on the disk.
- big-log-analyzer / claudemd #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-log-analyzer-claudemd-2-2543766898\repo\stele.md: There is not enough space on the disk.
- big-log-analyzer / stele #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-log-analyzer-stele-2-4069060768\repo\stele.md: There is not enough space on the disk.
- big-log-analyzer / stele-tools #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-log-analyzer-stele-tools-2-2656366270\repo\stele.md: There is not enough space on the disk.
- big-billing-refactor / none #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-none-2-3002319508\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-billing-refactor / claudemd #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-claudemd-2-679559551\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-billing-refactor / stele #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-stele-2-1588799950\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-billing-refactor / stele-tools #2: error: setup: write C:\Users\Nacer\AppData\Local\Temp\stele-eval-big-billing-refactor-stele-tools-2-3199424690\repo\internal\billing\billing.go: There is not enough space on the disk.
- big-todo-service / stele #1: guidance: Store methods take ctx first;

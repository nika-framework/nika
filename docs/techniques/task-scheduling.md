# Task Scheduling

> **Coming Soon** — Task scheduling is not yet implemented.

Task scheduling allows you to schedule recurring jobs (cron-like) within your application.

## Planned Design

```go
// Planned API (subject to change)
type Scheduler interface {
    Every(interval time.Duration) *Job
    Cron(expression string) *Job
}

type Job interface {
    Do(task func()) *Job
    Start()
    Stop()
}
```

## Current Alternative

Use a standalone cron library like [robfig/cron](https://github.com/robfig/cron):

```go
import "github.com/robfig/cron/v3"

func main() {
    app := nika.NewApp()
    c := cron.New()

    // Schedule a job every minute
    c.AddFunc("@every 1m", func() {
        log.Println("Running scheduled task...")
    })

    // Schedule with cron expression
    c.AddFunc("0 0 * * *", func() {
        log.Println("Running daily task...")
    })

    c.Start()

    app.LoadModule(rootModule)
    app.Listen(":3000")
}
```

## Status

| Feature | Status |
|---------|--------|
| Cron scheduling | ⏳ Planned |
| Interval scheduling | ⏳ Planned |
| One-time delayed tasks | ⏳ Planned |
| Job management (start/stop/remove) | ⏳ Planned |

!!! info "Want to contribute?"
    This feature is open for contribution.

package wallet

import (
	"context"
	"sync"
	"time"
)

/*
group controls commands life cycle
command controls execution flow of the internal function
*/

type Command interface {
	Run(ctx context.Context)
	ErrorReceiver(chan<- error)
}

type Group struct {
	wg       sync.WaitGroup
	errors   chan error
	commands []Command
}

func (g *Group) Add(cmd Command) {
	g.commands = append(g.commands, cmd)
}

func (g *Group) Run(ctx context.Context) {
	g.errors = make(chan error, len(g.commands))
	for i := range g.commands {
		command := g.commands[i]
		command.ErrorReceiver(g.errors)
		g.wg.Add(1)
		command.Run(ctx)
	}
}

func (g *Group) Wait() (err error) {
	g.wg.Wait()
	close(g.errors)
	// TODO(dshulyak) merge errors
	for e := range g.errors {
		if err == nil && e != nil {
			err = nil
		}
	}
	return err
}

type FiniteCommandWithFallback struct {
	Period   time.Duration
	Func     func(context.Context) error
	Fallback func(error) (time.Duration, error)
	error    chan<- error
}

func (c *FiniteCommandWithFallback) Run(ctx context.Context) {
	var (
		err    error
		period = c.Period
		timer  = time.NewTicker(period)
	)
	defer timer.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				c.error <- nil
				return
			case <-timer.C:
				err = c.Func(ctx)
				if err == nil {
					c.error <- nil
					return
				}
				period, err = c.Fallback(err)
				if err != nil {
					c.error <- err
					return
				}
				if period != 0 {
					timer.Stop()
					timer = time.NewTicker(period)
				}
			}
		}
	}()
}

// TODO(dshulyak) better way to inject error handler to sub-commands
func (c *FiniteCommandWithFallback) ErrorReceiver(err chan<- error) {
	c.error = err
}

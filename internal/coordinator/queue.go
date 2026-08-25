package coordinator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

var (
	ErrAgentIDRequired  = errors.New("agent id is required")
	ErrTurnIDRequired   = errors.New("turn id is required")
	ErrTurnFuncRequired = errors.New("turn function is required")
)

// TurnQueue serializes work keyed by Agent ID. The same Agent runs FIFO, one
// turn at a time. Different Agents run concurrently. A failed or canceled
// turn does not prevent later turns for that Agent.
type TurnQueue struct {
	mu     sync.Mutex
	agents map[string]*agentTurns
}

type agentTurns struct {
	jobs    []*turnJob
	running bool
}

type turnJob struct {
	ctx     context.Context
	agentID string
	turnID  string
	fn      func(context.Context) error
	result  chan error
}

func NewTurnQueue() *TurnQueue {
	return &TurnQueue{agents: make(map[string]*agentTurns)}
}

func (q *TurnQueue) Enqueue(ctx context.Context, agentID, turnID string, fn func(context.Context) error) error {
	if q == nil {
		return errors.New("turn queue is required")
	}
	if agentID == "" {
		return ErrAgentIDRequired
	}
	if turnID == "" {
		return ErrTurnIDRequired
	}
	if fn == nil {
		return ErrTurnFuncRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job := &turnJob{
		ctx:     ctx,
		agentID: agentID,
		turnID:  turnID,
		fn:      fn,
		result:  make(chan error, 1),
	}
	q.enqueue(job)
	select {
	case err := <-job.result:
		return err
	case <-ctx.Done():
		select {
		case err := <-job.result:
			return err
		default:
			return ctx.Err()
		}
	}
}

func (q *TurnQueue) enqueue(job *turnJob) {
	q.mu.Lock()
	agent := q.agents[job.agentID]
	if agent == nil {
		agent = &agentTurns{}
		q.agents[job.agentID] = agent
	}
	agent.jobs = append(agent.jobs, job)
	start := !agent.running
	if start {
		agent.running = true
	}
	q.mu.Unlock()
	if start {
		go q.work(agent)
	}
}

func (q *TurnQueue) work(agent *agentTurns) {
	for {
		q.mu.Lock()
		if len(agent.jobs) == 0 {
			agent.running = false
			q.mu.Unlock()
			return
		}
		job := agent.jobs[0]
		agent.jobs[0] = nil
		agent.jobs = agent.jobs[1:]
		q.mu.Unlock()
		q.run(job)
	}
}

func (q *TurnQueue) run(job *turnJob) {
	if err := job.ctx.Err(); err != nil {
		slog.Info("turn canceled", "agentId", job.agentID, "turnId", job.turnID)
		job.finish(err)
		return
	}
	slog.Info("turn started", "agentId", job.agentID, "turnId", job.turnID)
	err := invoke(job)
	if err != nil {
		slog.Error("turn failed", "agentId", job.agentID, "turnId", job.turnID, "error", err)
	} else {
		slog.Info("turn finished", "agentId", job.agentID, "turnId", job.turnID)
	}
	job.finish(err)
}

func invoke(job *turnJob) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("turn panicked: %v", recovered)
		}
	}()
	return job.fn(job.ctx)
}

func (j *turnJob) finish(err error) {
	select {
	case j.result <- err:
	default:
	}
}

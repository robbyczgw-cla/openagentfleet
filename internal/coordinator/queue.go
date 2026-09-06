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
	job, err := q.submit(ctx, agentID, turnID, fn)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case err := <-job.result:
		return err
	case <-ctx.Done():
		q.cancelPending(job)
		return <-job.result
	}
}

// Submit reserves the Agent's queue position before returning. The result
// arrives after execution or after the queue skips a canceled turn.
func (q *TurnQueue) Submit(ctx context.Context, agentID, turnID string, fn func(context.Context) error) (<-chan error, error) {
	job, err := q.submit(ctx, agentID, turnID, fn)
	if err != nil {
		return nil, err
	}
	return job.result, nil
}

func (q *TurnQueue) submit(ctx context.Context, agentID, turnID string, fn func(context.Context) error) (*turnJob, error) {
	if q == nil {
		return nil, errors.New("turn queue is required")
	}
	if agentID == "" {
		return nil, ErrAgentIDRequired
	}
	if turnID == "" {
		return nil, ErrTurnIDRequired
	}
	if fn == nil {
		return nil, ErrTurnFuncRequired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job := &turnJob{ctx: ctx, agentID: agentID, turnID: turnID, fn: fn, result: make(chan error, 1)}
	q.enqueue(job)
	return job, nil
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

// A running job owns its terminal state until its callback returns.
func (q *TurnQueue) cancelPending(job *turnJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	agent := q.agents[job.agentID]
	if agent == nil {
		return
	}
	for i, pending := range agent.jobs {
		if pending == job {
			copy(agent.jobs[i:], agent.jobs[i+1:])
			agent.jobs[len(agent.jobs)-1] = nil
			agent.jobs = agent.jobs[:len(agent.jobs)-1]
			job.finish(job.ctx.Err())
			return
		}
	}
}

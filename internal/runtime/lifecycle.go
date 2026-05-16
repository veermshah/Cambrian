package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/db"
	"github.com/veermshah/cambrian/internal/redis"
)

// Lifecycle channels every NodeRunner subscribes to via the swarm. Each
// event is a JSON {agent_id, reason} payload published by the
// LifecycleManager.
const (
	ChannelLifecycleSpawn   = "lifecycle:spawn"
	ChannelLifecycleKill    = "lifecycle:kill"
	ChannelLifecyclePause   = "lifecycle:pause"
	ChannelLifecycleResume  = "lifecycle:resume"
	ChannelLifecyclePromote = "lifecycle:promote"
	ChannelLifecycleDemote  = "lifecycle:demote"
)

// LifecycleEvent is the JSON payload pushed on each lifecycle: channel.
// Reason is free-form ops text; CorrelationID lets the dashboard match a
// transition back to the orchestrator decision that caused it.
type LifecycleEvent struct {
	AgentID       string    `json:"agent_id"`
	Reason        string    `json:"reason,omitempty"`
	NodeClass     string    `json:"node_class,omitempty"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

// LifecycleStore is the DB surface LifecycleManager needs. *db.Queries
// satisfies it; tests use a small fake.
type LifecycleStore interface {
	InsertAgent(ctx context.Context, a db.Agent) (string, error)
	SetAgentStatus(ctx context.Context, agentID, status, killReason string) error
	SetAgentNodeClass(ctx context.Context, agentID, class string) error
}

// LifecycleManager mediates agent state changes. Every transition writes
// the DB first and then publishes a lifecycle:* event so the
// SwarmRuntime (and the dashboard) can react. The DB write is the
// source of truth — Redis is best-effort notification.
type LifecycleManager struct {
	store LifecycleStore
	bus   redis.Client
	log   agent.Logger
	now   func() time.Time
}

// NewLifecycleManager constructs a manager. log may be nil (defaults to
// NopLogger).
func NewLifecycleManager(store LifecycleStore, bus redis.Client, log agent.Logger) *LifecycleManager {
	if log == nil {
		log = agent.NopLogger{}
	}
	return &LifecycleManager{
		store: store,
		bus:   bus,
		log:   log,
		now:   time.Now,
	}
}

// Spawn inserts a new agent row from the proposed genome + wallet and
// publishes lifecycle:spawn. Returns the new agent's UUID. The SwarmRuntime
// listens for spawn events and builds a NodeRunner.
//
// The caller is responsible for serializing the genome's JSON fields
// (strategy_config, bandit_policies, etc.) — keeping the LifecycleStore
// surface narrow lets tests fake it without depending on genome
// marshaling helpers that live in package agent.
func (l *LifecycleManager) Spawn(ctx context.Context, agentRow db.Agent, reason string) (string, error) {
	if l == nil {
		return "", errors.New("lifecycle: nil receiver")
	}
	id, err := l.store.InsertAgent(ctx, agentRow)
	if err != nil {
		return "", fmt.Errorf("lifecycle.Spawn: insert: %w", err)
	}
	l.publish(ctx, ChannelLifecycleSpawn, LifecycleEvent{
		AgentID:   id,
		Reason:    reason,
		NodeClass: agentRow.NodeClass,
		Timestamp: l.now(),
	})
	return id, nil
}

// Kill marks an agent dead. Status='dead', kill_reason set. The
// SwarmRuntime stops its NodeRunner on receipt of lifecycle:kill.
// Spec lines 514–522 enumerate the conditions that trigger Kill;
// enforcing them is the orchestrator's job, not this layer's.
func (l *LifecycleManager) Kill(ctx context.Context, agentID, reason string) error {
	if agentID == "" {
		return errors.New("lifecycle: Kill requires agent_id")
	}
	if err := l.store.SetAgentStatus(ctx, agentID, "dead", reason); err != nil {
		return fmt.Errorf("lifecycle.Kill: %w", err)
	}
	l.publish(ctx, ChannelLifecycleKill, LifecycleEvent{
		AgentID:   agentID,
		Reason:    reason,
		Timestamp: l.now(),
	})
	return nil
}

// Pause halts the agent's monitor/strategist loops without killing it.
// kill_reason carries the pause reason so the dashboard can show why.
func (l *LifecycleManager) Pause(ctx context.Context, agentID, reason string) error {
	if agentID == "" {
		return errors.New("lifecycle: Pause requires agent_id")
	}
	if err := l.store.SetAgentStatus(ctx, agentID, "paused", reason); err != nil {
		return fmt.Errorf("lifecycle.Pause: %w", err)
	}
	l.publish(ctx, ChannelLifecyclePause, LifecycleEvent{
		AgentID:   agentID,
		Reason:    reason,
		Timestamp: l.now(),
	})
	return nil
}

// Resume reverses Pause: status='active'. We don't clear kill_reason —
// it stays as a historical breadcrumb (the killed_at column is the only
// one we touch on dead transitions).
func (l *LifecycleManager) Resume(ctx context.Context, agentID, reason string) error {
	if agentID == "" {
		return errors.New("lifecycle: Resume requires agent_id")
	}
	if err := l.store.SetAgentStatus(ctx, agentID, "active", reason); err != nil {
		return fmt.Errorf("lifecycle.Resume: %w", err)
	}
	l.publish(ctx, ChannelLifecycleResume, LifecycleEvent{
		AgentID:   agentID,
		Reason:    reason,
		Timestamp: l.now(),
	})
	return nil
}

// Promote moves a shadow agent to funded class. Spec line 540: shadow
// agents earn promotion when sustained simulated PnL beats a fixed
// threshold; the orchestrator decides, this method just commits.
func (l *LifecycleManager) Promote(ctx context.Context, agentID, reason string) error {
	if err := l.setClassAndEmit(ctx, agentID, "funded", reason, ChannelLifecyclePromote); err != nil {
		return fmt.Errorf("lifecycle.Promote: %w", err)
	}
	return nil
}

// Demote moves a funded agent to shadow class. Used when an agent's
// realized profit slips below the survival threshold but the orchestrator
// wants to keep it running on paper for evidence.
func (l *LifecycleManager) Demote(ctx context.Context, agentID, reason string) error {
	if err := l.setClassAndEmit(ctx, agentID, "shadow", reason, ChannelLifecycleDemote); err != nil {
		return fmt.Errorf("lifecycle.Demote: %w", err)
	}
	return nil
}

func (l *LifecycleManager) setClassAndEmit(ctx context.Context, agentID, class, reason, channel string) error {
	if agentID == "" {
		return errors.New("lifecycle: agent_id required")
	}
	if err := l.store.SetAgentNodeClass(ctx, agentID, class); err != nil {
		return err
	}
	l.publish(ctx, channel, LifecycleEvent{
		AgentID:   agentID,
		Reason:    reason,
		NodeClass: class,
		Timestamp: l.now(),
	})
	return nil
}

func (l *LifecycleManager) publish(ctx context.Context, channel string, ev LifecycleEvent) {
	if l.bus == nil {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		l.log.Warnw("lifecycle_marshal_failed", "channel", channel, "error", err)
		return
	}
	if err := l.bus.Publish(ctx, channel, payload); err != nil {
		l.log.Warnw("lifecycle_publish_failed",
			"channel", channel, "agent_id", ev.AgentID, "error", err)
	}
}

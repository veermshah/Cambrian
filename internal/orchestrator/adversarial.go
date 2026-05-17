package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/veermshah/cambrian/internal/agent"
	"github.com/veermshah/cambrian/internal/llm"
)

// AdversarialVerdict is the synthesizer's final call. Mirrors the
// QualityVerdict closed set so chunk 21's pipeline can treat them
// uniformly when routing offspring into spawn / drop / revise paths.
type AdversarialVerdict string

const (
	AdversarialApprove AdversarialVerdict = "approve"
	AdversarialReject  AdversarialVerdict = "reject"
	AdversarialRevise  AdversarialVerdict = "revise"
)

// AdversarialResult is what AdversarialReview returns. BullCase /
// BearCase / Synthesis are persisted verbatim into
// offspring_proposals.adversarial_synthesis (spec line 1006) so the
// dashboard can show the full debate without re-running the calls.
type AdversarialResult struct {
	Verdict   AdversarialVerdict
	BullCase  string
	BearCase  string
	Synthesis string
	// CostUSD is the sum of the three call costs. Caller (chunk 21)
	// routes it to the proposing agent's ledger row.
	CostUSD float64
	// ModelUsed echoes the client's model; the three calls share one
	// client so they share one model.
	ModelUsed string
	// InputTokens / OutputTokens are summed across the three calls.
	InputTokens  int
	OutputTokens int
}

const adversarialBullSystem = `You are the BULL advocate for an evolutionary DeFi agent swarm. You are
shown one candidate offspring genome and the current swarm context
(diversity score + the (task_type, chain, model) tuples already in
production). Make the strongest possible case FOR spawning this agent.

Cover, in 2-4 sentences:
  - What novel niche or capability the candidate adds.
  - Why its (task_type, chain, model) combination is likely to be
    profitable given current swarm coverage.
  - The plausible upside if the strategy works.

Output prose only — no JSON, no markdown fences.`

const adversarialBearSystem = `You are the BEAR advocate for an evolutionary DeFi agent swarm. You are
shown one candidate offspring genome and the current swarm context.
Make the strongest possible case AGAINST spawning this agent.

Cover, in 2-4 sentences:
  - Why this candidate may be a near-duplicate of an existing genome,
    or why its niche is already saturated.
  - Where its strategy_config, prompt, or reproduction_policy may be
    internally inconsistent.
  - The plausible downside if the strategy fails (drawdown, cost
    leakage, communication noise).

Output prose only — no JSON, no markdown fences.`

const adversarialSynthesisSystem = `You are the synthesizer for an evolutionary DeFi agent swarm. You are
shown the candidate genome, the BULL case, the BEAR case, and the swarm
context. Decide whether the candidate should spawn.

Return JSON only, no surrounding prose:

  {
    "verdict": "approve" | "reject" | "revise",
    "synthesis": "<2-4 sentence diagnosis weighing bull vs bear>"
  }

Rules:
  - "approve" only when the bull case clearly outweighs the bear case
    AND the candidate fills a genuine gap.
  - "reject" when the bear case identifies a duplicate, an incoherence,
    or a saturated niche the bull case fails to address.
  - "revise" when the candidate could work with bounded adjustments
    (small parameter tweaks, prompt clarifications) that resolve the
    bear's objections.`

// AdversarialReview runs three sequential LLM calls (bull, bear,
// synthesis) and returns the verdict plus all three transcripts. Spec
// lines 427-432; costs ~$0.01-$0.03 per proposal at production model
// pricing. Defensive: a malformed synthesis JSON downgrades to revise
// with the raw output preserved; transport errors propagate so callers
// can retry.
func AdversarialReview(ctx context.Context, client llm.LLMClient, candidate agent.AgentGenome, swarm SwarmContext, maxTokens int) (AdversarialResult, error) {
	if client == nil {
		return AdversarialResult{}, errors.New("adversarial: nil llm client")
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	candidatePayload, err := marshalAdversarialPayload(candidate, swarm)
	if err != nil {
		return AdversarialResult{}, fmt.Errorf("adversarial: build payload: %w", err)
	}

	bull, err := client.Complete(ctx, adversarialBullSystem, candidatePayload, maxTokens)
	if err != nil {
		return AdversarialResult{}, fmt.Errorf("adversarial: bull call: %w", err)
	}
	bullText := strings.TrimSpace(bull.Content)

	bear, err := client.Complete(ctx, adversarialBearSystem, candidatePayload, maxTokens)
	if err != nil {
		return AdversarialResult{}, fmt.Errorf("adversarial: bear call: %w", err)
	}
	bearText := strings.TrimSpace(bear.Content)

	synthUser := fmt.Sprintf(
		"%s\n\nBULL CASE:\n%s\n\nBEAR CASE:\n%s",
		candidatePayload, bullText, bearText,
	)
	synth, err := client.Complete(ctx, adversarialSynthesisSystem, synthUser, maxTokens)
	if err != nil {
		return AdversarialResult{}, fmt.Errorf("adversarial: synthesis call: %w", err)
	}

	result := AdversarialResult{
		BullCase:     bullText,
		BearCase:     bearText,
		CostUSD:      bull.CostUSD + bear.CostUSD + synth.CostUSD,
		ModelUsed:    synth.Model,
		InputTokens:  bull.InputTokens + bear.InputTokens + synth.InputTokens,
		OutputTokens: bull.OutputTokens + bear.OutputTokens + synth.OutputTokens,
	}

	verdict, synthesis, parseErr := parseSynthesis(synth.Content)
	if parseErr != nil {
		result.Verdict = AdversarialRevise
		result.Synthesis = fmt.Sprintf("parse_error: %v; raw=%s", parseErr, truncate(synth.Content, 240))
		return result, nil
	}
	result.Verdict = verdict
	result.Synthesis = synthesis
	return result, nil
}

func marshalAdversarialPayload(candidate agent.AgentGenome, swarm SwarmContext) (string, error) {
	payload := map[string]interface{}{
		"candidate": candidate,
		"swarm": map[string]interface{}{
			"diversity_score":  swarm.DiversityScore,
			"existing_genomes": swarm.ExistingGenomes,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type rawSynthesisResponse struct {
	Verdict   string `json:"verdict"`
	Synthesis string `json:"synthesis"`
}

func parseSynthesis(s string) (AdversarialVerdict, string, error) {
	body := extractJSONObject(s)
	if body == "" {
		return AdversarialRevise, "", errors.New("no JSON object in synthesis")
	}
	var raw rawSynthesisResponse
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&raw); err != nil {
		return AdversarialRevise, "", err
	}
	if raw.Verdict == "" {
		return AdversarialRevise, "", errors.New("missing verdict")
	}
	if raw.Synthesis == "" {
		return AdversarialRevise, "", errors.New("missing synthesis")
	}
	return normalizeAdversarialVerdict(raw.Verdict), raw.Synthesis, nil
}

func normalizeAdversarialVerdict(s string) AdversarialVerdict {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "approve":
		return AdversarialApprove
	case "reject":
		return AdversarialReject
	case "revise":
		return AdversarialRevise
	default:
		return AdversarialRevise
	}
}

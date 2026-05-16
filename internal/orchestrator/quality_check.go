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

// QualityVerdict is the closed set of outcomes the quality check can
// return. Spec line 425: every offspring gets a diversity / coherence /
// gap-coverage review before it's allowed to spawn.
type QualityVerdict string

const (
	// VerdictApprove means the candidate is fit to spawn as-is.
	VerdictApprove QualityVerdict = "approve"
	// VerdictReject means the orchestrator should drop the candidate
	// and refund the proposing agent's reproduction reserve.
	VerdictReject QualityVerdict = "reject"
	// VerdictRevise means the candidate needs the suggested revisions
	// applied before spawning. Defensive parse failures collapse to
	// this verdict — surfaces the issue without forcing a hard reject.
	VerdictRevise QualityVerdict = "revise"
)

// QualityResult is what QualityCheck returns. Costs flow into
// agent_ledgers via the proposing agent's row — spec line 87 says
// "node-level profitability is evaluated as if each node paid its own
// costs", and a quality-check call is part of the cost of reproduction.
type QualityResult struct {
	Verdict            QualityVerdict
	Reasoning          string
	SuggestedRevisions json.RawMessage
	CostUSD            float64
	ModelUsed          string
	InputTokens        int
	OutputTokens       int
}

// SwarmContext is the minimal slice of swarm state the quality check
// LLM needs to judge diversity and gap coverage. The root orchestrator
// (chunk 21) builds this from db.Queries; chunk 19 is the consumer.
type SwarmContext struct {
	// DiversityScore is the current Simpson-style index from chunk 20.
	// Zero is fine — the LLM will treat it as "unknown."
	DiversityScore float64
	// ExistingGenomes is the (task_type, chain, model) summary for
	// every active funded/shadow agent. Used to spot a candidate
	// that's a near-duplicate of an existing genome.
	ExistingGenomes []GenomeSummary
}

// GenomeSummary is the lightweight tuple the quality check considers.
// Full genomes are expensive to ship through the prompt and most fields
// don't influence the diversity / coherence decision.
type GenomeSummary struct {
	AgentID    string `json:"agent_id,omitempty"`
	Name       string `json:"name,omitempty"`
	TaskType   string `json:"task_type"`
	Chain      string `json:"chain"`
	Model      string `json:"model"`
	Generation int    `json:"generation,omitempty"`
}

// rawQualityResponse is the JSON shape we ask the LLM to emit. Keys
// match the prompt template below verbatim — any drift trips the
// defensive parse and collapses to VerdictRevise.
type rawQualityResponse struct {
	Verdict            string          `json:"verdict"`
	Reasoning          string          `json:"reasoning"`
	SuggestedRevisions json.RawMessage `json:"suggested_revisions,omitempty"`
}

const qualityCheckSystem = `You are the quality-check reviewer for an evolutionary DeFi agent swarm.

You receive one proposed offspring genome plus a snapshot of the existing
swarm (diversity score and the (task_type, chain, model) tuples already in
production). Decide whether the candidate is fit to spawn.

Judge on three axes:
  1. Diversity — does the candidate add a (task_type, chain, model)
     combination the swarm is missing, or duplicate something already
     well-covered?
  2. Coherence — are the candidate's strategy_config, strategist_prompt,
     and reproduction_policy internally consistent?
  3. Gap coverage — does the candidate fill a known gap (a chain or
     task type with weak performance / low representation)?

Return JSON only, no surrounding prose:

  {
    "verdict": "approve" | "reject" | "revise",
    "reasoning": "<1-3 sentence diagnosis>",
    "suggested_revisions": { <optional field-level revisions> }
  }

Rules:
  - "approve" only when the candidate is clearly additive.
  - "reject" when the candidate is a near-duplicate or incoherent.
  - "revise" when the candidate could work with bounded adjustments.
  - Do not adjust fields the candidate omits; only suggest revisions
    to fields explicitly present in the candidate.
`

// QualityCheck issues one LLM call against the candidate + swarm context
// and returns the parsed verdict. Defensive parsing: any deviation from
// the expected JSON shape collapses to VerdictRevise with the reasoning
// preserved verbatim from the model output, so the operator can see
// what the model actually said.
//
// CostUSD is read from the LLMResponse; the caller (chunk 21's root
// orchestrator) is responsible for routing it into agent_ledgers under
// the proposing agent's id.
func QualityCheck(ctx context.Context, client llm.LLMClient, candidate agent.AgentGenome, swarm SwarmContext, maxTokens int) (QualityResult, error) {
	if client == nil {
		return QualityResult{}, errors.New("quality_check: nil llm client")
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	user, err := buildQualityUserPrompt(candidate, swarm)
	if err != nil {
		return QualityResult{}, fmt.Errorf("quality_check: build prompt: %w", err)
	}

	resp, err := client.Complete(ctx, qualityCheckSystem, user, maxTokens)
	if err != nil {
		return QualityResult{}, fmt.Errorf("quality_check: llm.Complete: %w", err)
	}

	result := QualityResult{
		CostUSD:      resp.CostUSD,
		ModelUsed:    resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
	}

	raw, parseErr := parseQualityResponse(resp.Content)
	if parseErr != nil {
		// Defensive: an unparseable response is not a fatal LLM error,
		// it just means we can't trust the verdict — so revise.
		result.Verdict = VerdictRevise
		result.Reasoning = fmt.Sprintf("parse_error: %v; raw=%s", parseErr, truncate(resp.Content, 240))
		return result, nil
	}

	result.Verdict = normalizeVerdict(raw.Verdict)
	result.Reasoning = raw.Reasoning
	if result.Verdict == VerdictRevise && result.Reasoning == "" {
		result.Reasoning = "verdict downgraded to revise: " + truncate(raw.Verdict, 80)
	}
	if len(raw.SuggestedRevisions) > 0 {
		result.SuggestedRevisions = raw.SuggestedRevisions
	}
	return result, nil
}

// RewriteStrategistPrompt is the chunk-17 placeholder fulfilled. Mutate
// appends a `// mut-v{generation+1}` marker to the parent prompt; this
// function asks the LLM to produce the actual rewrite. Returns the
// rewritten prompt verbatim — caller swaps it into the child genome.
//
// CostUSD is again surfaced for the caller to attribute to the
// proposing agent's ledger.
func RewriteStrategistPrompt(ctx context.Context, client llm.LLMClient, parentPrompt, mutationMarker string, maxTokens int) (string, float64, error) {
	if client == nil {
		return "", 0, errors.New("quality_check: nil llm client")
	}
	if parentPrompt == "" {
		return "", 0, errors.New("quality_check: empty parent prompt")
	}
	if maxTokens <= 0 {
		maxTokens = 512
	}
	system := promptRewriteSystem
	user := fmt.Sprintf(
		"PARENT PROMPT:\n%s\n\nMUTATION MARKER: %s\n\nRewrite the parent prompt with a single small variation. Output the rewritten prompt only, no commentary, no markdown fences.",
		parentPrompt, mutationMarker,
	)
	resp, err := client.Complete(ctx, system, user, maxTokens)
	if err != nil {
		return "", 0, fmt.Errorf("quality_check: rewrite: %w", err)
	}
	rewritten := strings.TrimSpace(resp.Content)
	if rewritten == "" {
		// Defensive: empty rewrite means the parent stands. Surface the
		// cost so the agent's ledger reflects the wasted call.
		return parentPrompt, resp.CostUSD, nil
	}
	return rewritten, resp.CostUSD, nil
}

const promptRewriteSystem = `You rewrite strategist prompts for DeFi agents. You receive a parent prompt
and a mutation marker like "// mut-v7". Produce a single small variation:
adjust emphasis, swap one constraint, or change one heuristic — never more
than one axis at a time. Preserve the parent's core role and JSON output
contract. Reply with the rewritten prompt only.`

// buildQualityUserPrompt marshals the candidate (minus the wallet) and
// the swarm summary into a compact JSON blob the model can reason over.
func buildQualityUserPrompt(candidate agent.AgentGenome, swarm SwarmContext) (string, error) {
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

// parseQualityResponse extracts the JSON object from the model output
// (mirroring agent.extractJSONObject) and decodes it. Strict: any
// missing required field or extra/unrelated content is a parse error
// the caller treats as VerdictRevise.
func parseQualityResponse(s string) (rawQualityResponse, error) {
	body := extractJSONObject(s)
	if body == "" {
		return rawQualityResponse{}, errors.New("no JSON object found")
	}
	var out rawQualityResponse
	dec := json.NewDecoder(strings.NewReader(body))
	if err := dec.Decode(&out); err != nil {
		return rawQualityResponse{}, err
	}
	if out.Verdict == "" {
		return rawQualityResponse{}, errors.New("missing verdict")
	}
	if out.Reasoning == "" {
		return rawQualityResponse{}, errors.New("missing reasoning")
	}
	return out, nil
}

// normalizeVerdict accepts case-insensitive variants and downgrades
// anything outside the closed set to revise.
func normalizeVerdict(s string) QualityVerdict {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "approve":
		return VerdictApprove
	case "reject":
		return VerdictReject
	case "revise":
		return VerdictRevise
	default:
		return VerdictRevise
	}
}

// extractJSONObject is the same balanced-brace finder used by the
// strategist parser. Duplicated here (rather than imported) to avoid
// reaching into package agent's private helpers — both implementations
// are small enough that drift is a fine tradeoff for decoupling.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inString, escape := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if inString {
			switch c {
			case '\\':
				escape = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

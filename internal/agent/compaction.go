package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/X9st/TianFaSRC/internal/identity"
	"github.com/X9st/TianFaSRC/internal/llm"
)

// compactForModel preserves the original transcript and stores the exact
// reduced view separately. Scope, goals, Todo, Intent and human steer are never
// removed; graph facts are reduced to references when the model window is full.
func (e Engine) compactForModel(ctx context.Context, orgID, taskID, runID, role string, messages []llm.Message) ([]llm.Message, error) {
	profiles, err := e.Profiles.ResolvedChain(ctx, orgID, taskID, role)
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return messages, nil
	}
	maxChars := 0
	for _, p := range profiles {
		available := (p.ContextWindow - p.MaxOutputTokens - 1024) * 4
		if available > maxChars {
			maxChars = available
		}
	}
	if maxChars <= 0 {
		return nil, &llm.BlockedError{Reason: "模型上下文窗口无法容纳输出预留与安全缓冲"}
	}
	total := 0
	for _, m := range messages {
		total += len(m.Content) + 32
	}
	if total <= maxChars {
		return messages, nil
	}
	if len(messages) < 2 {
		return nil, &llm.BlockedError{Reason: "模型上下文窗口不足，且没有可压缩的任务图"}
	}
	allowed := maxChars - (total - len(messages[1].Content))
	if allowed < 1024 {
		return nil, &llm.BlockedError{Reason: "模型上下文窗口不足以保留任务范围、目标和工具观察"}
	}
	view, err := compactGraphContext(messages[1].Content, allowed)
	if err != nil {
		return nil, &llm.BlockedError{Reason: err.Error()}
	}
	sum := sha256.Sum256([]byte(messages[1].Content))
	if _, err = e.Pool.Exec(ctx, `INSERT INTO agent_context_digests(id,run_id,through_ordinal,summary,source_hash) VALUES($1,$2,1,$3,$4) ON CONFLICT(run_id,through_ordinal) DO NOTHING`, identity.NewID(), runID, view, hex.EncodeToString(sum[:])); err != nil {
		return nil, err
	}
	copyOf := append([]llm.Message{}, messages...)
	copyOf[1].Content = "以下是持久化原始任务上下文的压缩视图；原文可在 Run Transcript 检索。\n" + view
	return copyOf, nil
}

func compactGraphContext(raw string, limit int) (string, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return "", err
	}
	graphRaw, ok := body["graph"]
	if !ok {
		return "", fmt.Errorf("上下文超过模型窗口，且无可压缩任务图")
	}
	var graph struct {
		Nodes []struct {
			ID             string  `json:"id"`
			Kind           string  `json:"kind"`
			State          string  `json:"state"`
			Summary        string  `json:"summary"`
			SourceIntentID *string `json:"source_intent_id,omitempty"`
			RunID          *string `json:"run_id,omitempty"`
			ToolCallID     *string `json:"tool_call_id,omitempty"`
		} `json:"nodes"`
		Edges   json.RawMessage `json:"edges"`
		Anchors json.RawMessage `json:"anchors"`
	}
	if err := json.Unmarshal(graphRaw, &graph); err != nil {
		return "", err
	}
	counts := map[string]int{}
	for _, n := range graph.Nodes {
		counts[n.Kind+"/"+n.State]++
	}
	delete(body, "graph")
	core, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	if len(core)+512 > limit {
		return "", fmt.Errorf("任务范围、目标、Todo 与人工指令超过模型上下文窗口")
	}
	for keep := len(graph.Nodes); keep >= 0; {
		if keep > 100 {
			keep = 100
		}
		start := len(graph.Nodes) - keep
		refs := make([]string, 0, start)
		for _, n := range graph.Nodes[:start] {
			if n.Kind == "fact" || n.Kind == "finding" || n.Kind == "digest" {
				refs = append(refs, n.ID)
			}
		}
		view := map[string]any{"context": body, "graph_digest": map[string]any{"counts": counts, "recent_nodes": graph.Nodes[start:], "older_evidence_node_ids": refs, "omitted_edges": len(graph.Edges) > 0, "omitted_anchors": len(graph.Anchors) > 0}}
		encoded, err := json.Marshal(view)
		if err != nil {
			return "", err
		}
		if len(encoded)+120 <= limit {
			return string(encoded), nil
		}
		if keep == 0 {
			break
		}
		keep /= 2
	}
	return "", fmt.Errorf("图引用仍超过模型上下文窗口；请缩小任务或使用更大窗口的 Profile")
}

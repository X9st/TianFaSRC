package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCompactionPreservesCoreAndEvidenceReferences(t *testing.T) {
	nodes := []map[string]string{}
	for i := 0; i < 220; i++ {
		nodes = append(nodes, map[string]string{"id": "fact-" + strings.Repeat("x", 4) + string(rune(1000+i)), "kind": "fact", "state": "confirmed", "summary": strings.Repeat("受控观察", 50)})
	}
	rawBytes, _ := json.Marshal(map[string]any{"task": map[string]any{"id": "task-1"}, "scope": []string{"example.invalid"}, "goals": []string{"goal-1"}, "todos": []string{"step-1"}, "steer": []string{"仅使用授权范围"}, "graph": map[string]any{"nodes": nodes, "edges": []any{}, "anchors": []any{}}})
	view, err := compactGraphContext(string(rawBytes), 10000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view, "example.invalid") || !strings.Contains(view, "仅使用授权范围") || !strings.Contains(view, "older_evidence_node_ids") || len(view) > 10000 {
		t.Fatalf("invalid compressed view: bytes=%d", len(view))
	}
	if !strings.Contains(string(rawBytes), "受控观察") {
		t.Fatal("original changed")
	}
}

package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/seethespark/pr-reviewer/internal/config"
	"github.com/seethespark/pr-reviewer/internal/provider"
	"github.com/seethespark/pr-reviewer/internal/review"
)

func TestAgentPipeline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"content": `{"critical": 1, "high": 0, "medium": 0, "low": 0}
Found a critical SQL injection issue.`}},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	os.Setenv("OLLAMA_API_KEY", "test-key")
	defer os.Unsetenv("OLLAMA_API_KEY")

	ollama := provider.NewOllamaProvider()
	ollama.SetEndpoint(srv.URL + "/api/chat")

	cfg := &config.Config{
		Owner:            "Test",
		Context:          "Test app",
		ProductionStatus: "Dev",
		SecurityLevel:    "Low",
	}
	agent := config.AgentConfig{Subagent: "security-auditor"}
	prompt := review.BuildPrompt(review.PromptInput{Cfg: cfg, Agent: agent, Diff: "diff content"})

	resp, err := ollama.Generate(context.Background(), "test-model", prompt)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	summary, _, err := review.ParseSeverity(resp.Text)
	if err != nil {
		t.Fatalf("parse severity failed: %v", err)
	}
	if summary.Critical != 1 {
		t.Errorf("expected critical=1, got %d", summary.Critical)
	}

	report := review.FormatAgentReport(agent, summary, resp.Text)
	if !strings.Contains(report, "security-auditor") {
		t.Error("report missing agent name")
	}
	if !strings.Contains(report, "wd-auto-review:agent=security-auditor") {
		t.Error("report missing marker")
	}
}

package agent

import (
	"encoding/json"
	"strings"
)

func parseClaudeResult(stdout, stderr string, exitCode int) RunResult {
	result := defaultResult(stdout, stderr, exitCode)
	terminal := false
	for line := range strings.SplitSeq(stdout, "\n") {
		var event struct {
			Type    string   `json:"type"`
			Subtype string   `json:"subtype"`
			IsError bool     `json:"is_error"`
			Result  string   `json:"result"`
			Errors  []string `json:"errors"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Type == "assistant" {
			var texts []string
			for _, block := range event.Message.Content {
				if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
					texts = append(texts, block.Text)
				}
			}
			if len(texts) > 0 {
				result.Summary = strings.Join(texts, "\n")
			}
		}
		if event.Type == "result" {
			terminal = true
			if event.Result != "" {
				result.Summary = event.Result
			}
			if event.IsError || strings.HasPrefix(event.Subtype, "error") {
				result.Status = "failed"
				result.RiskNotes = append(result.RiskNotes, "Claude execution failed: "+event.Subtype)
				if len(event.Errors) > 0 {
					result.Summary = strings.Join(event.Errors, "\n")
				} else if event.Result == "" {
					result.Summary = "Claude execution failed: " + event.Subtype
				}
			}
		}
	}
	if !terminal {
		result.Status = "failed"
		result.RiskNotes = append(result.RiskNotes, "Claude did not emit a terminal result; output may be incomplete.")
	}
	result.Summary = boundedSummary(result.Summary)
	return result
}

func parseCodexResult(stdout, stderr string, exitCode int) RunResult {
	result := defaultResult(stdout, stderr, exitCode)
	terminal := false
	for line := range strings.SplitSeq(stdout, "\n") {
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Status string `json:"status"`
				Error  struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"item"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			continue
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" && event.Item.Text != "" {
			result.Summary = event.Item.Text
		}
		if event.Type == "error" && event.Message != "" {
			result.Summary = event.Message
		}
		// Error events can describe retried requests. Only a terminal failed turn
		// overrides a successful process exit; recovered command failures are normal.
		if event.Type == "turn.failed" {
			terminal = true
			result.Status = "failed"
			message := event.Error.Message
			if message == "" {
				message = event.Message
			}
			if message == "" {
				message = "Codex turn failed"
			}
			result.Summary = message
			result.RiskNotes = append(result.RiskNotes, message)
		}
		if event.Type == "turn.completed" {
			terminal = true
		}
	}
	if !terminal {
		result.Status = "failed"
		result.RiskNotes = append(result.RiskNotes, "Codex did not emit a terminal turn result; output may be incomplete.")
	}
	result.Summary = boundedSummary(result.Summary)
	return result
}

func boundedSummary(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4000 {
		value = value[:4000]
	}
	return value
}

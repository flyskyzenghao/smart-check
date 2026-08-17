package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AIMode ???? AI ????????????
type AIMode string

const (
	aiModeOtherPromotion AIMode = "other_promotion"
	aiModeAuxMarketing   AIMode = "auxiliary_marketing"
)

// AIJudgement ?????????????????? JSON ????????????
type AIJudgement struct {
	Positive           bool    `json:"positive"`
	Confidence         float64 `json:"confidence"`
	Evidence           string  `json:"evidence"`
	Reason             string  `json:"reason"`
	AdministrativeOnly bool    `json:"administrative_only"`
	BusinessRelated    bool    `json:"business_related"`
	Scenario           string  `json:"scenario"`
	UsedFallback       bool    `json:"-"`
	Error              string  `json:"-"`
}

type aiClassifier struct {
	cfg    AIConfig
	client *http.Client
}

func newAIClassifier(cfg AIConfig) *aiClassifier {
	seconds := cfg.TimeoutSeconds
	if seconds < 5 {
		seconds = 30
	}
	return &aiClassifier{
		cfg:    cfg,
		client: &http.Client{Timeout: time.Duration(seconds) * time.Second},
	}
}

func (c *aiClassifier) configured() bool {
	return c != nil && c.cfg.Enabled && strings.TrimSpace(c.cfg.APIKey) != "" && strings.TrimSpace(c.cfg.Endpoint) != ""
}

func normalizeAIEndpoint(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return ""
	}
	if strings.HasSuffix(endpoint, "/chat/completions") {
		return endpoint
	}
	if strings.HasSuffix(endpoint, "/v1") {
		return endpoint + "/chat/completions"
	}
	return endpoint + "/v1/chat/completions"
}

func (c *aiClassifier) classify(ctx context.Context, mode AIMode, scenario, text string) AIJudgement {
	text = strings.TrimSpace(text)
	if text == "" {
		return AIJudgement{}
	}
	// ??? AI ?????????????????????????????
	fallback := heuristicMarketingJudgement(mode, text)
	if !c.configured() {
		fallback.UsedFallback = true
		fallback.Error = "AI???????"
		return fallback
	}

	prompt := buildAIPrompt(mode, scenario, text)
	payload := map[string]interface{}{
		"model":       c.cfg.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "?????????????????????????????????????????? JSON ????? Markdown?"},
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fallback.UsedFallback = true
		fallback.Error = "AI???????"
		return fallback
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, normalizeAIEndpoint(c.cfg.Endpoint), bytes.NewReader(body))
	if err != nil {
		fallback.UsedFallback = true
		fallback.Error = "AI??????: " + err.Error()
		return fallback
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	resp, err := c.client.Do(req)
	if err != nil {
		fallback.UsedFallback = true
		fallback.Error = "AI??????: " + err.Error()
		return fallback
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fallback.UsedFallback = true
		if err != nil {
			fallback.Error = "AI??????: " + err.Error()
		} else {
			fallback.Error = fmt.Sprintf("AI??HTTP %d", resp.StatusCode)
		}
		return fallback
	}
	judgement, ok := parseAIResponse(respBody, mode)
	if !ok {
		fallback.UsedFallback = true
		fallback.Error = "AI??????JSON??"
		return fallback
	}
	if judgement.Confidence <= 0 {
		judgement.Confidence = 0.8
	}
	judgement.UsedFallback = false
	return judgement
}

// classifyScenario ???????????????????????????????
func (c *aiClassifier) classifyScenario(ctx context.Context, text string) AIJudgement {
	text = strings.TrimSpace(text)
	if text == "" || !c.configured() {
		return AIJudgement{UsedFallback: true, Error: "AI???????"}
	}
	prompt := fmt.Sprintf(`???????????????????????????
????????????????????????????????
???????scenario ??????????????? JSON?
{"scenario":"????","confidence":0.0,"evidence":"??","reason":"????"}

?????
%s`, text)
	payload := map[string]interface{}{
		"model":       c.cfg.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "????? JSON??? Markdown??????????????"},
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return AIJudgement{UsedFallback: true, Error: "AI?????????"}
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, normalizeAIEndpoint(c.cfg.Endpoint), bytes.NewReader(body))
	if err != nil {
		return AIJudgement{UsedFallback: true, Error: "AI????????: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	resp, err := c.client.Do(req)
	if err != nil {
		return AIJudgement{UsedFallback: true, Error: "AI????????: " + err.Error()}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err != nil {
			return AIJudgement{UsedFallback: true, Error: "AI????????: " + err.Error()}
		}
		return AIJudgement{UsedFallback: true, Error: fmt.Sprintf("AI????HTTP %d", resp.StatusCode)}
	}
	judgement, ok := parseAIResponse(respBody, "scenario")
	if !ok {
		return AIJudgement{UsedFallback: true, Error: "AI????????JSON??"}
	}
	return judgement
}

func buildAIPrompt(mode AIMode, scenario, text string) string {
	var task string
	switch mode {
	case aiModeAuxMarketing:
		task = "?????????????????????????????????????????????????????/???????????????????????????????????/??/??????????????????????"
	default:
		task = "?????????????????????????????????????????????/??/???????????????????????????????????????????????????/??/??????????????????????"
	}
	return fmt.Sprintf("?????%s\n???%s\n\n?????\n- ?????????????????????????????????????\n- ????????????????????????????\n- ??????????????????????????????????????????/???????????????\n- ??????????????????????????????/????????????????? false?\n- ??????? JSON?{\"positive\":true,\"confidence\":0.0,\"evidence\":\"??????\",\"reason\":\"????\",\"administrative_only\":false,\"business_related\":true}\n\n?????????????\n%s", scenario, task, text)
}

func parseAIResponse(body []byte, mode AIMode) (AIJudgement, bool) {
	var envelope interface{}
	if json.Unmarshal(body, &envelope) != nil {
		return AIJudgement{}, false
	}
	content := ""
	if root, ok := envelope.(map[string]interface{}); ok {
		if choices, ok := root["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if msg, ok := choice["message"].(map[string]interface{}); ok {
					content = extractAIContent(msg["content"])
				}
				if content == "" {
					content, _ = choice["text"].(string)
				}
			}
		}
		if content == "" {
			content, _ = root["content"].(string)
		}
	}
	if content == "" {
		content = string(body)
	}
	content = stripJSONFences(content)
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return AIJudgement{}, false
	}
	var raw map[string]interface{}
	if json.Unmarshal([]byte(content[start:end+1]), &raw) != nil {
		return AIJudgement{}, false
	}
	positive, ok := firstBool(raw, "positive", "is_relevant_marketing", "is_auxiliary_marketing", string(mode))
	if !ok && mode != "scenario" {
		return AIJudgement{}, false
	}
	confidence, _ := toFloat(raw["confidence"])
	evidence, _ := raw["evidence"].(string)
	reason, _ := raw["reason"].(string)
	adminOnly, _ := firstBool(raw, "administrative_only", "admin_only")
	businessRelated, _ := firstBool(raw, "business_related", "relevant")
	scenario, _ := raw["scenario"].(string)
	return AIJudgement{Positive: positive, Confidence: confidence, Evidence: evidence, Reason: reason, AdministrativeOnly: adminOnly, BusinessRelated: businessRelated, Scenario: strings.TrimSpace(scenario)}, true
}

func extractAIContent(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	if parts, ok := value.([]interface{}); ok {
		var b strings.Builder
		for _, part := range parts {
			if m, ok := part.(map[string]interface{}); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func stripJSONFences(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```JSON")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

func firstBool(m map[string]interface{}, keys ...string) (bool, bool) {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch x := v.(type) {
			case bool:
				return x, true
			case string:
				value := strings.ToLower(strings.TrimSpace(x))
				if value == "true" || value == "yes" || value == "?" {
					return true, true
				}
				if value == "false" || value == "no" || value == "?" {
					return false, true
				}
			}
		}
	}
	return false, false
}

// heuristicMarketingJudgement ??? AI ???/?????????????????????????
func heuristicMarketingJudgement(mode AIMode, text string) AIJudgement {
	if onlyCourtesyOrEmoji(text) || isAdministrativeOnly(text) {
		return AIJudgement{Positive: false, Confidence: 0.99, AdministrativeOnly: true, Reason: "???????????"}
	}
	lower := strings.ToLower(text)
	marketingHints := []string{
		"??", "???", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??",
		"??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??",
	}
	for _, hint := range marketingHints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			return AIJudgement{Positive: true, Confidence: 0.68, BusinessRelated: true, Evidence: text, Reason: fmt.Sprintf("???????%s", hint)}
		}
	}
	// ???????????????????????????????????
	if mode == aiModeOtherPromotion {
		for _, hint := range []string{"???", "??", "???", "????", "????", "???"} {
			if strings.Contains(lower, hint) {
				return AIJudgement{Positive: true, Confidence: 0.62, BusinessRelated: true, Evidence: text, Reason: "????????"}
			}
		}
	}
	return AIJudgement{Positive: false, Confidence: 0.62, Reason: "????????????"}
}

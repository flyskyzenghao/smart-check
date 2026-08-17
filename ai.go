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

// AIMode 表示需要 AI 语义判断的业务规则类型。
type AIMode string

const (
	aiModeOtherPromotion AIMode = "other_promotion"
	aiModeAuxMarketing   AIMode = "auxiliary_marketing"
)

// AIJudgement 是模型返回的最小结构。模型输出不符合 JSON 时会回退到本地语义规则。
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
	// 未配置 AI 时仍使用保守的本地语义兜底，避免需求询问被误判为无关信息。
	fallback := heuristicMarketingJudgement(mode, text)
	if !c.configured() {
		fallback.UsedFallback = true
		fallback.Error = "AI未配置或未启用"
		return fallback
	}

	prompt := buildAIPrompt(mode, scenario, text)
	payload := map[string]interface{}{
		"model":       c.cfg.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "你是电信商机质检专家。只依据给定聊天内容判断，不补充聊天中没有的事实。必须只返回一个 JSON 对象，不要 Markdown。"},
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		fallback.UsedFallback = true
		fallback.Error = "AI请求序列化失败"
		return fallback
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, normalizeAIEndpoint(c.cfg.Endpoint), bytes.NewReader(body))
	if err != nil {
		fallback.UsedFallback = true
		fallback.Error = "AI请求创建失败: " + err.Error()
		return fallback
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	resp, err := c.client.Do(req)
	if err != nil {
		fallback.UsedFallback = true
		fallback.Error = "AI网络请求失败: " + err.Error()
		return fallback
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fallback.UsedFallback = true
		if err != nil {
			fallback.Error = "AI响应读取失败: " + err.Error()
		} else {
			fallback.Error = fmt.Sprintf("AI返回HTTP %d", resp.StatusCode)
		}
		return fallback
	}
	judgement, ok := parseAIResponse(respBody, mode)
	if !ok {
		fallback.UsedFallback = true
		fallback.Error = "AI返回不是有效JSON判定"
		return fallback
	}
	if judgement.Confidence <= 0 {
		judgement.Confidence = 0.8
	}
	judgement.UsedFallback = false
	return judgement
}

// classifyScenario 仅在业务字段和本地规则都无法识别时调用，避免模型猜测已知场景。
func (c *aiClassifier) classifyScenario(ctx context.Context, text string) AIJudgement {
	text = strings.TrimSpace(text)
	if text == "" || !c.configured() {
		return AIJudgement{UsedFallback: true, Error: "AI未配置或未启用"}
	}
	prompt := fmt.Sprintf(`你是电信商机质检专家。请根据完整会话判断唯一营销场景。
只能从以下四个值中选择：新装宽带、新装移动、新装专线、存量提值。
如果信息不足，scenario 返回空字符串，不能猜测。只输出 JSON：
{"scenario":"新装宽带","confidence":0.0,"evidence":"原文","reason":"简短理由"}

完整会话：
%s`, text)
	payload := map[string]interface{}{
		"model":       c.cfg.Model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "只输出合法 JSON，不要 Markdown，不要补充会话中没有的事实。"},
			{"role": "user", "content": prompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return AIJudgement{UsedFallback: true, Error: "AI场景请求序列化失败"}
	}
	reqCtx := ctx
	if reqCtx == nil {
		reqCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, normalizeAIEndpoint(c.cfg.Endpoint), bytes.NewReader(body))
	if err != nil {
		return AIJudgement{UsedFallback: true, Error: "AI场景请求创建失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))
	resp, err := c.client.Do(req)
	if err != nil {
		return AIJudgement{UsedFallback: true, Error: "AI场景网络请求失败: " + err.Error()}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if err != nil {
			return AIJudgement{UsedFallback: true, Error: "AI场景响应读取失败: " + err.Error()}
		}
		return AIJudgement{UsedFallback: true, Error: fmt.Sprintf("AI场景返回HTTP %d", resp.StatusCode)}
	}
	judgement, ok := parseAIResponse(respBody, "scenario")
	if !ok {
		return AIJudgement{UsedFallback: true, Error: "AI场景返回不是有效JSON判定"}
	}
	return judgement
}

func buildAIPrompt(mode AIMode, scenario, text string) string {
	var task string
	switch mode {
	case aiModeAuxMarketing:
		task = "判断专员在一线首次介入前、同一自然日内是否做了辅助营销。辅助营销包括需求挖掘、现状询问、使用场景确认、号码/套餐查询、优惠或资费推荐、业务比较、推进办理等。仅礼貌回复、表情、收到/好的/谢谢、到某处找我、记得带证件等行政提醒不算。"
	default:
		task = "判断专员消息是否属于对当前商机促成有帮助的其他信息。包括需求挖掘、现状确认、业务适配、套餐/资费/优惠说明、号码查询、办理推进、风险消除等，即使没有命中固定关键词也要按语义判断。仅礼貌回复、表情、收到/好的/谢谢、到某处找我、记得带证件等行政提醒不算。"
	}
	return fmt.Sprintf("业务场景：%s\n任务：%s\n\n判定要求：\n- 只判断专员消息是否与当前客户业务需求相关；客户和一线消息只用于理解上下文。\n- ‘其他促成信息’不能把场景已定义的固定关键信息重复计数。\n- ‘当日辅助营销’必须发生在一线首次介入前的同一自然日，并且是需求挖掘、现状确认、优惠/资费推荐、业务匹配或办理推进。\n- 仅‘好的、谢谢、收到’、表情、‘记得带身份证’、‘到现场找我/不用排队’等行政或礼貌内容必须判为 false。\n- 必须只输出合法 JSON：{\"positive\":true,\"confidence\":0.0,\"evidence\":\"引用关键原句\",\"reason\":\"简短理由\",\"administrative_only\":false,\"business_related\":true}\n\n完整会话（含角色和时间）：\n%s", scenario, task, text)
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
				if value == "true" || value == "yes" || value == "是" {
					return true, true
				}
				if value == "false" || value == "no" || value == "否" {
					return false, true
				}
			}
		}
	}
	return false, false
}

// heuristicMarketingJudgement 只作为 AI 未配置/调用失败时的保守兜底，专门覆盖需求挖掘与优惠识别。
func heuristicMarketingJudgement(mode AIMode, text string) AIJudgement {
	if onlyCourtesyOrEmoji(text) || isAdministrativeOnly(text) {
		return AIJudgement{Positive: false, Confidence: 0.99, AdministrativeOnly: true, Reason: "仅礼貌、表情或行政提醒"}
	}
	lower := strings.ToLower(text)
	marketingHints := []string{
		"请问", "有没有", "是否", "多少", "需要", "使用", "住宅", "公司", "场景", "人数", "号码", "查下", "查询", "了解", "需求",
		"流量", "宽带", "套餐", "优惠", "价格", "资费", "办理", "安装", "预约", "推荐", "选择", "比较", "现用", "电信", "移动", "联通",
	}
	for _, hint := range marketingHints {
		if strings.Contains(lower, strings.ToLower(hint)) {
			return AIJudgement{Positive: true, Confidence: 0.68, BusinessRelated: true, Evidence: text, Reason: fmt.Sprintf("命中语义线索：%s", hint)}
		}
	}
	// “其他促成信息”可识别明确的业务推进动作；辅助营销不把纯行政动作算入。
	if mode == aiModeOtherPromotion {
		for _, hint := range []string{"提供下", "发我", "帮您查", "可以办理", "为您安排", "确认下"} {
			if strings.Contains(lower, hint) {
				return AIJudgement{Positive: true, Confidence: 0.62, BusinessRelated: true, Evidence: text, Reason: "存在业务推进动作"}
			}
		}
	}
	return AIJudgement{Positive: false, Confidence: 0.62, Reason: "未发现明确营销或促成语义"}
}

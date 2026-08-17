package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func chatMessage(role, text string, minute int) ParsedChatMessage {
	return ParsedChatMessage{
		Role:      role,
		Text:      text,
		Timestamp: time.Unix(int64(minute*60), 0),
		HasTime:   true,
	}
}

func TestInterventionSituation(t *testing.T) {
	tests := []struct {
		name            string
		matched         []string
		hasConversation bool
		want            string
	}{
		{name: "one", matched: []string{"宽带安装地址"}, hasConversation: true, want: "键入1条关键信息"},
		{name: "two", matched: []string{"宽带安装地址", "宽带类型"}, hasConversation: true, want: "键入2条关键信息"},
		{name: "three or more", matched: []string{"宽带安装地址", "宽带类型", "宽带速率要求"}, hasConversation: true, want: "键入3条及以上关键信息"},
		{name: "no key", matched: nil, hasConversation: true, want: "无关键信息介入"},
		{name: "no conversation", matched: nil, hasConversation: false, want: "无相关会话"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := interventionSituation(tt.matched, tt.hasConversation); got != tt.want {
				t.Fatalf("interventionSituation() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsInterruptionRequiresFrontlineCustomerConversation(t *testing.T) {
	tests := []struct {
		name     string
		messages []ParsedChatMessage
		want     bool
	}{
		{
			name: "customer first specialist reply is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("客户", "想办理宽带", 0),
				chatMessage("专员", "请问地址", 1),
			},
			want: false,
		},
		{
			name: "frontline without customer is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("一线", "您好", 0),
				chatMessage("专员", "请问地址", 1),
			},
			want: false,
		},
		{
			name: "specialist within ten minutes is interruption",
			messages: []ParsedChatMessage{
				chatMessage("客户", "想办理宽带", 0),
				chatMessage("一线", "您好，我来了解一下", 1),
				chatMessage("专员", "请问地址", 9),
			},
			want: true,
		},
		{
			name: "specialist after ten minutes is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("客户", "想办理宽带", 0),
				chatMessage("一线", "您好，我来了解一下", 1),
				chatMessage("专员", "请问地址", 11),
			},
			want: false,
		},
		{
			name: "customer waiting for frontline is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("一线", "您好", 0),
				chatMessage("客户", "想办理宽带", 1),
				chatMessage("专员", "请问地址", 2),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInterruption(tt.messages, tt.messages[len(tt.messages)-1]); got != tt.want {
				t.Fatalf("isInterruption() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnalyzeConversationCountsKeyInformation(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("客户", "想装宽带", 0),
		chatMessage("一线", "可以的，请问您需要什么业务", 1),
		chatMessage("客户", "新装宽带", 2),
		chatMessage("专员", "请问宽带安装地址和宽带类型？", 13),
	}
	result, interruption, scenario, matched, roles, situation := analyzeConversation(messages, "新装宽带", 1)
	if result != "有效营销" || interruption != "否" || scenario != "新装宽带" || roles != "一线/专员" {
		t.Fatalf("unexpected conversation result: result=%q interruption=%q scenario=%q roles=%q", result, interruption, scenario, roles)
	}
	if len(matched) != 2 || situation != "键入2条关键信息" {
		t.Fatalf("matched=%v situation=%q, want 2 key rules", matched, situation)
	}
}

func TestOnlyCourtesyOrEmoji(t *testing.T) {
	for _, text := range []string{"/鲜花", "好的，谢谢", "👍", "😀😀"} {
		if !onlyCourtesyOrEmoji(text) {
			t.Fatalf("onlyCourtesyOrEmoji(%q) = false", text)
		}
	}
	if onlyCourtesyOrEmoji("记得带身份证") {
		t.Fatal("business reminder must not be treated as courtesy-only")
	}
}

func TestClassifyMessageRoleUsesFieldNames(t *testing.T) {
	if got := classifyMessageRole(map[string]interface{}{"运营专员名称": "张三"}, nil); got != "专员" {
		t.Fatalf("operator field role = %q, want 专员", got)
	}
	if got := classifyMessageRole(map[string]interface{}{"企微号名称": "一线客服"}, nil); got != "一线" {
		t.Fatalf("frontline field role = %q, want 一线", got)
	}
}

func TestStoredValueRulesRespectTimeWindows(t *testing.T) {
	frontlineAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)
	serviceWithinSevenDays := ParsedChatMessage{Role: "专员", Text: "上次服务问题已解决，已完成闭环", Timestamp: frontlineAt.Add(-6 * 24 * time.Hour), HasTime: true}
	serviceTooOld := ParsedChatMessage{Role: "专员", Text: "上次服务问题已解决，已完成闭环", Timestamp: frontlineAt.Add(-8 * 24 * time.Hour), HasTime: true}
	marketingSameDay := ParsedChatMessage{Role: "专员", Text: "今天做需求挖掘并推荐优惠", Timestamp: frontlineAt.Add(-time.Hour), HasTime: true}
	marketingPreviousDay := ParsedChatMessage{Role: "专员", Text: "昨天做需求挖掘并推荐优惠", Timestamp: frontlineAt.Add(-25 * time.Hour), HasTime: true}
	frontline := ParsedChatMessage{Role: "一线", Text: "您好", Timestamp: frontlineAt, HasTime: true}

	within := matchedScenarioRules([]ParsedChatMessage{serviceWithinSevenDays, marketingSameDay, frontline}, "存量提值")
	if len(within) != 2 || within[0] != "一线介入前7天服务闭环" || within[1] != "一线介入当日辅助营销" {
		t.Fatalf("within window matched = %v", within)
	}

	oldAndPreviousDay := matchedScenarioRules([]ParsedChatMessage{serviceTooOld, marketingPreviousDay, frontline}, "存量提值")
	if len(oldAndPreviousDay) != 0 {
		t.Fatalf("out-of-window matched = %v, want none", oldAndPreviousDay)
	}
}

func TestSemanticMarketingFallbackRecognizesDemandMining(t *testing.T) {
	text := "请问是住宅使用吗？目前有使用电信的卡吗？看看有没有优惠，你提供下号码我查下。"
	judgement := heuristicMarketingJudgement(aiModeOtherPromotion, text)
	if !judgement.Positive {
		t.Fatalf("semantic fallback marked clear marketing questions as negative: %+v", judgement)
	}
	if heuristicMarketingJudgement(aiModeAuxMarketing, "好的，谢谢").Positive {
		t.Fatal("courtesy-only text must not be auxiliary marketing")
	}
}

func TestAnalyzeConversationTreatsScreenshotLikeQuestionsAsMarketing(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("客户", "想了解移动业务", 0),
		chatMessage("一线", "您好，我先和您沟通", 1),
		chatMessage("专员", "请问是住宅使用吗？", 12),
		chatMessage("专员", "目前有使用电信的卡吗？", 13),
		chatMessage("专员", "看看有没有优惠，你提供下号码我查下", 14),
	}
	result, interruption, scenario, matched, _, situation := analyzeConversation(messages, "新装移动", 1)
	if result != "有效营销" || interruption != "否" || scenario != "新装移动" {
		t.Fatalf("unexpected result for demand-mining conversation: result=%q interruption=%q scenario=%q matched=%v", result, interruption, scenario, matched)
	}
	if len(matched) == 0 || situation == "无关键信息介入" {
		t.Fatalf("demand-mining questions should not be marked no-key: matched=%v situation=%q", matched, situation)
	}
}

func TestSemanticMarketingDoesNotDuplicateFixedRules(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("专员", "请问宽带安装地址和宽带类型？", 0),
	}
	matched := matchedScenarioRules(messages, "新装宽带")
	if len(matched) != 2 || matched[0] != "宽带安装地址" || matched[1] != "宽带类型" {
		t.Fatalf("fixed key questions should not be duplicated as other promotion: %v", matched)
	}
}

func TestFixedConditionValuesDoNotBecomeOtherPromotion(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("专员", "请问宽带安装地址，您是家用还是公司用？", 0),
	}
	matched := matchedScenarioRules(messages, "新装宽带")
	if len(matched) != 2 || matched[0] != "宽带安装地址" || matched[1] != "宽带类型" {
		t.Fatalf("fixed address/type values must not duplicate as other promotion: %v", matched)
	}
}

func TestMobileFixedFlowQuestionDoesNotDuplicateAsOtherPromotion(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("专员", "请问每月需要多少流量？", 0),
	}
	matched := matchedScenarioRules(messages, "新装移动")
	if len(matched) != 1 || matched[0] != "流量使用需求" {
		t.Fatalf("fixed flow question must not duplicate as other promotion: %v", matched)
	}
}

func TestAIClassifierParsesOpenAICompatibleResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"positive\":true,\"confidence\":0.93,\"evidence\":\"询问优惠\",\"reason\":\"属于需求挖掘\"}"}}]}`))
	}))
	defer server.Close()
	classifier := newAIClassifier(AIConfig{Enabled: true, Endpoint: server.URL, APIKey: "test-key", Model: "test", TimeoutSeconds: 5})
	got := classifier.classify(context.Background(), aiModeAuxMarketing, "存量提值", "目前有使用电信的卡吗？看看有没有优惠")
	if !got.Positive || got.Confidence < 0.9 || got.Evidence != "询问优惠" {
		t.Fatalf("unexpected AI judgement: %+v", got)
	}
}

func TestAIOtherPromotionUsesFullContextAndCanDenyKeywordCandidate(t *testing.T) {
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if messages, ok := payload["messages"].([]interface{}); ok && len(messages) > 1 {
			if user, ok := messages[1].(map[string]interface{}); ok {
				requestBody, _ = user["content"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"positive\":false,\"confidence\":0.96,\"evidence\":\"\",\"reason\":\"仅固定条件\"}"}}]}`))
	}))
	defer server.Close()
	classifier := newAIClassifier(AIConfig{Enabled: true, Endpoint: server.URL, APIKey: "test-key", Model: "test", TimeoutSeconds: 5})
	messages := []ParsedChatMessage{
		chatMessage("客户", "我想装宽带", 0),
		chatMessage("一线", "您好，我来帮您了解", 1),
		chatMessage("专员", "请问宽带安装地址，后续可以帮您查套餐", 2),
	}
	matched := matchedScenarioRulesWithAI(context.Background(), messages, "新装宽带", classifier)
	if len(matched) != 1 || matched[0] != "宽带安装地址" {
		t.Fatalf("AI negative should deny other-promotion label while keeping fixed label: %v", matched)
	}
	for _, want := range []string{"我想装宽带", "您好，我来帮您了解", "请问宽带安装地址"} {
		if !strings.Contains(requestBody, want) {
			t.Fatalf("AI prompt did not contain full conversation context %q: %s", want, requestBody)
		}
	}
}

func TestGeneric办理DoesNotMeanTransactionCompleted(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("客户", "想了解宽带", 0),
		chatMessage("一线", "可以办理宽带，我先帮您确认需求", 1),
		chatMessage("专员", "请问宽带安装地址？", 12),
	}
	result, _, _, matched, _, _ := analyzeConversation(messages, "新装宽带", 1)
	if result != "有效营销" || len(matched) != 1 {
		t.Fatalf("普通‘可以办理’不应被当作已成交: result=%q matched=%v", result, matched)
	}
}

func TestAdministrativeReminderIsNotMarketing(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("客户", "我明天过来", 0),
		chatMessage("一线", "好的，到时联系", 1),
		chatMessage("专员", "记得带身份证，到现场找我，不用排队", 12),
	}
	result, _, _, matched, _, situation := analyzeConversation(messages, "新装宽带", 1)
	if result != "无关键信息介入" || len(matched) != 0 || situation != "无关键信息介入" {
		t.Fatalf("行政提醒不应算营销: result=%q matched=%v situation=%q", result, matched, situation)
	}
}

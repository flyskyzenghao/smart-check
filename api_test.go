package main

import (
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

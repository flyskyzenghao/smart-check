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
		{name: "one", matched: []string{"??????"}, hasConversation: true, want: "??1?????"},
		{name: "two", matched: []string{"??????", "????"}, hasConversation: true, want: "??2?????"},
		{name: "three or more", matched: []string{"??????", "????", "??????"}, hasConversation: true, want: "??3????????"},
		{name: "no key", matched: nil, hasConversation: true, want: "???????"},
		{name: "no conversation", matched: nil, hasConversation: false, want: "?????"},
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
				chatMessage("??", "?????", 0),
				chatMessage("??", "????", 1),
			},
			want: false,
		},
		{
			name: "frontline without customer is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("??", "??", 0),
				chatMessage("??", "????", 1),
			},
			want: false,
		},
		{
			name: "specialist within ten minutes is interruption",
			messages: []ParsedChatMessage{
				chatMessage("??", "?????", 0),
				chatMessage("??", "?????????", 1),
				chatMessage("??", "????", 9),
			},
			want: true,
		},
		{
			name: "specialist after ten minutes is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("??", "?????", 0),
				chatMessage("??", "?????????", 1),
				chatMessage("??", "????", 11),
			},
			want: false,
		},
		{
			name: "customer waiting for frontline is not interruption",
			messages: []ParsedChatMessage{
				chatMessage("??", "??", 0),
				chatMessage("??", "?????", 1),
				chatMessage("??", "????", 2),
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
		chatMessage("??", "????", 0),
		chatMessage("??", "?????????????", 1),
		chatMessage("??", "????", 2),
		chatMessage("??", "??????????????", 13),
	}
	result, interruption, scenario, matched, roles, situation := analyzeConversation(messages, "????", 1)
	if result != "????" || interruption != "?" || scenario != "????" || roles != "??/??" {
		t.Fatalf("unexpected conversation result: result=%q interruption=%q scenario=%q roles=%q", result, interruption, scenario, roles)
	}
	if len(matched) != 2 || situation != "??2?????" {
		t.Fatalf("matched=%v situation=%q, want 2 key rules", matched, situation)
	}
}

func TestOnlyCourtesyOrEmoji(t *testing.T) {
	for _, text := range []string{"/??", "?????", "??", "????"} {
		if !onlyCourtesyOrEmoji(text) {
			t.Fatalf("onlyCourtesyOrEmoji(%q) = false", text)
		}
	}
	if onlyCourtesyOrEmoji("??????") {
		t.Fatal("business reminder must not be treated as courtesy-only")
	}
}

func TestClassifyMessageRoleUsesFieldNames(t *testing.T) {
	if got := classifyMessageRole(map[string]interface{}{"??????": "??"}, nil); got != "??" {
		t.Fatalf("operator field role = %q, want ??", got)
	}
	if got := classifyMessageRole(map[string]interface{}{"?????": "????"}, nil); got != "??" {
		t.Fatalf("frontline field role = %q, want ??", got)
	}
}

func TestStoredValueRulesRespectTimeWindows(t *testing.T) {
	frontlineAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.Local)
	serviceWithinSevenDays := ParsedChatMessage{Role: "??", Text: "???????????????", Timestamp: frontlineAt.Add(-6 * 24 * time.Hour), HasTime: true}
	serviceTooOld := ParsedChatMessage{Role: "??", Text: "???????????????", Timestamp: frontlineAt.Add(-8 * 24 * time.Hour), HasTime: true}
	marketingSameDay := ParsedChatMessage{Role: "??", Text: "????????????", Timestamp: frontlineAt.Add(-time.Hour), HasTime: true}
	marketingPreviousDay := ParsedChatMessage{Role: "??", Text: "????????????", Timestamp: frontlineAt.Add(-25 * time.Hour), HasTime: true}
	frontline := ParsedChatMessage{Role: "??", Text: "??", Timestamp: frontlineAt, HasTime: true}

	within := matchedScenarioRules([]ParsedChatMessage{serviceWithinSevenDays, marketingSameDay, frontline}, "????")
	if len(within) != 2 || within[0] != "?????7?????" || within[1] != "??????????" {
		t.Fatalf("within window matched = %v", within)
	}

	oldAndPreviousDay := matchedScenarioRules([]ParsedChatMessage{serviceTooOld, marketingPreviousDay, frontline}, "????")
	if len(oldAndPreviousDay) != 0 {
		t.Fatalf("out-of-window matched = %v, want none", oldAndPreviousDay)
	}
}

func TestSemanticMarketingFallbackRecognizesDemandMining(t *testing.T) {
	text := "??????????????????????????????????????"
	judgement := heuristicMarketingJudgement(aiModeOtherPromotion, text)
	if !judgement.Positive {
		t.Fatalf("semantic fallback marked clear marketing questions as negative: %+v", judgement)
	}
	if heuristicMarketingJudgement(aiModeAuxMarketing, "?????").Positive {
		t.Fatal("courtesy-only text must not be auxiliary marketing")
	}
}

func TestAnalyzeConversationTreatsScreenshotLikeQuestionsAsMarketing(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("??", "???????", 0),
		chatMessage("??", "?????????", 1),
		chatMessage("??", "?????????", 12),
		chatMessage("??", "???????????", 13),
		chatMessage("??", "?????????????????", 14),
	}
	result, interruption, scenario, matched, _, situation := analyzeConversation(messages, "????", 1)
	if result != "????" || interruption != "?" || scenario != "????" {
		t.Fatalf("unexpected result for demand-mining conversation: result=%q interruption=%q scenario=%q matched=%v", result, interruption, scenario, matched)
	}
	if len(matched) == 0 || situation == "???????" {
		t.Fatalf("demand-mining questions should not be marked no-key: matched=%v situation=%q", matched, situation)
	}
}

func TestSemanticMarketingDoesNotDuplicateFixedRules(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("??", "??????????????", 0),
	}
	matched := matchedScenarioRules(messages, "????")
	if len(matched) != 2 || matched[0] != "??????" || matched[1] != "????" {
		t.Fatalf("fixed key questions should not be duplicated as other promotion: %v", matched)
	}
}

func TestFixedConditionValuesDoNotBecomeOtherPromotion(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("??", "???????????????????", 0),
	}
	matched := matchedScenarioRules(messages, "????")
	if len(matched) != 2 || matched[0] != "??????" || matched[1] != "????" {
		t.Fatalf("fixed address/type values must not duplicate as other promotion: %v", matched)
	}
}

func TestMobileFixedFlowQuestionDoesNotDuplicateAsOtherPromotion(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("??", "???????????", 0),
	}
	matched := matchedScenarioRules(messages, "????")
	if len(matched) != 1 || matched[0] != "??????" {
		t.Fatalf("fixed flow question must not duplicate as other promotion: %v", matched)
	}
}

func TestAIClassifierParsesOpenAICompatibleResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"positive\":true,\"confidence\":0.93,\"evidence\":\"????\",\"reason\":\"??????\"}"}}]}`))
	}))
	defer server.Close()
	classifier := newAIClassifier(AIConfig{Enabled: true, Endpoint: server.URL, APIKey: "test-key", Model: "test", TimeoutSeconds: 5})
	got := classifier.classify(context.Background(), aiModeAuxMarketing, "????", "??????????????????")
	if !got.Positive || got.Confidence < 0.9 || got.Evidence != "????" {
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"positive\":false,\"confidence\":0.96,\"evidence\":\"\",\"reason\":\"?????\"}"}}]}`))
	}))
	defer server.Close()
	classifier := newAIClassifier(AIConfig{Enabled: true, Endpoint: server.URL, APIKey: "test-key", Model: "test", TimeoutSeconds: 5})
	messages := []ParsedChatMessage{
		chatMessage("??", "?????", 0),
		chatMessage("??", "?????????", 1),
		chatMessage("??", "??????????????????", 2),
	}
	matched := matchedScenarioRulesWithAI(context.Background(), messages, "????", classifier)
	if len(matched) != 1 || matched[0] != "??????" {
		t.Fatalf("AI negative should deny other-promotion label while keeping fixed label: %v", matched)
	}
	for _, want := range []string{"?????", "?????????", "????????"} {
		if !strings.Contains(requestBody, want) {
			t.Fatalf("AI prompt did not contain full conversation context %q: %s", want, requestBody)
		}
	}
}

func TestGeneric??DoesNotMeanTransactionCompleted(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("??", "?????", 0),
		chatMessage("??", "???????????????", 1),
		chatMessage("??", "?????????", 12),
	}
	result, _, _, matched, _, _ := analyzeConversation(messages, "????", 1)
	if result != "????" || len(matched) != 1 {
		t.Fatalf("????????????????: result=%q matched=%v", result, matched)
	}
}

func TestAdministrativeReminderIsNotMarketing(t *testing.T) {
	messages := []ParsedChatMessage{
		chatMessage("??", "?????", 0),
		chatMessage("??", "???????", 1),
		chatMessage("??", "?????????????????", 12),
	}
	result, _, _, matched, _, situation := analyzeConversation(messages, "????", 1)
	if result != "???????" || len(matched) != 0 || situation != "???????" {
		t.Fatalf("?????????: result=%q matched=%v situation=%q", result, matched, situation)
	}
}

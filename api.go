package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"crypto/rand"
)

// ParsedChatMessage ??????????????
type ParsedChatMessage struct {
	Role      string
	Text      string
	Timestamp time.Time
	HasTime   bool
}

const (
	defaultBaseURL = "http://113.125.37.73:8843"
	requestDelay   = 1 * time.Second
	requestTimeout = 30 * time.Second
)

// rsaPublicKeyB64 512-bit RSA ???? SCRM ?? jsencrypt ???
const rsaPublicKeyB64 = "MFwwDQYJKoZIhvcNAQEBBQADSwAwSAJBAKoR8mX0rGKLqzcWmOzbfj64K8ZIgOdH" +
	"nzkXSOVOZbFu/TJhZ7rFAN+eaGkl3C4buccQd/EjEsj9ir7ijT7h96MCAwEAAQ=="

// parseBaseURL ??? URL ??? scheme://host:port
func parseBaseURL(rawURL string) string {
	if rawURL == "" {
		return defaultBaseURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return defaultBaseURL
	}
	return u.Scheme + "://" + u.Host
}

// rsaEncrypt ?? RSA PKCS1v15 ?? + Base64
// ??? crypto/rsa??? Go 1.24+ ?? <1024-bit ???
// ? SCRM ???? 512-bit ??????????
func rsaEncrypt(plaintext string) (string, error) {
	derBytes, err := base64.StdEncoding.DecodeString(rsaPublicKeyB64)
	if err != nil {
		return "", fmt.Errorf("base64 decode public key: %w", err)
	}

	// ???? ASN.1 DER ??? PKIX ??
	// ??: SEQUENCE { SEQUENCE { OID, NULL }, BIT STRING { SEQUENCE { INTEGER(n), INTEGER(e) } } }
	n, e, err := parsePKIXPublicKey(derBytes)
	if err != nil {
		return "", err
	}

	// PKCS1v15 ??: [0x00, 0x02, random_nonzero_bytes..., 0x00, plaintext]
	keyBytes := (n.BitLen() + 7) / 8
	plainBytes := []byte(plaintext)
	if len(plainBytes) > keyBytes-11 {
		return "", fmt.Errorf("plaintext too long for key size")
	}

	padded := make([]byte, keyBytes)
	padded[0] = 0x00
	padded[1] = 0x02

	// ????????
	paddingLen := keyBytes - len(plainBytes) - 3
	for i := 0; i < paddingLen; i++ {
		for {
			b := make([]byte, 1)
			rand.Read(b)
			if b[0] != 0 {
				padded[2+i] = b[0]
				break
			}
		}
	}

	padded[2+paddingLen] = 0x00
	copy(padded[3+paddingLen:], plainBytes)

	// RSA ??: c = m^e mod n
	m := new(big.Int).SetBytes(padded)
	c := new(big.Int).Exp(m, big.NewInt(int64(e)), n)

	// ???????????
	cBytes := c.Bytes()
	result := make([]byte, keyBytes)
	copy(result[keyBytes-len(cBytes):], cBytes)

	return base64.StdEncoding.EncodeToString(result), nil
}

// parsePKIXPublicKey ? DER ??????? RSA ??? N ? E
func parsePKIXPublicKey(der []byte) (*big.Int, int, error) {
	// ?? SEQUENCE
	seq1, _, err := parseASN1Sequence(der)
	if err != nil {
		return nil, 0, err
	}
	// ?????: AlgorithmIdentifier SEQUENCE { OID, NULL }
	// ?????: BIT STRING (??????)
	if len(seq1) < 2 {
		return nil, 0, fmt.Errorf("invalid PKIX structure")
	}

	// ?? BIT STRING (tag=0x03)
	bitString := seq1[1]
	if len(bitString) < 3 || bitString[0] != 0x03 {
		return nil, 0, fmt.Errorf("expected BIT STRING")
	}
	bitStringLen := parseASN1Length(bitString[1:])
	// BIT STRING ??: [unused_bits_count, ...inner_content]
	// unused bits count ?? 0
	bitContent := bitString[2+bitStringLen-len(bitString[2:]):]
	if len(bitContent) < 2 {
		return nil, 0, fmt.Errorf("BIT STRING too short")
	}
	// ?? unused bits ??
	innerDER := bitContent[1:]

	// ?? SEQUENCE { INTEGER(n), INTEGER(e) }
	seq2, _, err := parseASN1Sequence(innerDER)
	if err != nil {
		return nil, 0, err
	}
	if len(seq2) < 2 {
		return nil, 0, fmt.Errorf("RSA key needs N and E")
	}

	// ?? N (INTEGER)
	nBytes := parseASN1Integer(seq2[0])
	n := new(big.Int).SetBytes(nBytes)

	// ?? E (INTEGER)
	eBytes := parseASN1Integer(seq2[1])
	e := 0
	for _, b := range eBytes {
		e = e*256 + int(b)
	}

	return n, e, nil
}

// parseASN1Sequence ?? ASN.1 SEQUENCE????????
func parseASN1Sequence(data []byte) ([][]byte, int, error) {
	if len(data) < 2 || data[0] != 0x30 {
		return nil, 0, fmt.Errorf("not a SEQUENCE (got 0x%02x)", data[0])
	}
	length := parseASN1Length(data[1:])
	totalLen := 2 + length
	if data[1] >= 0x80 {
		totalLen = 2 + (int(data[1]) & 0x7f) + length
	}
	content := data[totalLen-length : totalLen]

	var elements [][]byte
	pos := 0
	for pos < len(content) {
		elemStart := pos
		pos++ // skip tag
		elemLen := parseASN1Length(content[pos:])
		lenBytes := 1
		if content[pos] >= 0x80 {
			lenBytes = 1 + int(content[pos]&0x7f)
		}
		pos += lenBytes + elemLen
		elements = append(elements, content[elemStart:pos])
	}
	return elements, totalLen, nil
}

// parseASN1Length ?? ASN.1 ????
func parseASN1Length(data []byte) int {
	if data[0] < 0x80 {
		return int(data[0])
	}
	numBytes := int(data[0]) & 0x7f
	length := 0
	for i := 0; i < numBytes; i++ {
		length = length*256 + int(data[1+i])
	}
	return length
}

// parseASN1Integer ?? INTEGER ????
func parseASN1Integer(data []byte) []byte {
	if len(data) < 2 || data[0] != 0x02 {
		return nil
	}
	length := parseASN1Length(data[1:])
	headerLen := 2
	if data[1] >= 0x80 {
		headerLen = 2 + int(data[1]&0x7f)
	}
	value := data[headerLen : headerLen+length]
	// ???? 0x00 (ASN.1 ????)
	for len(value) > 1 && value[0] == 0 {
		value = value[1:]
	}
	return value
}

// CaptchaResult ?????
type CaptchaResult struct {
	Img   string `json:"img"`
	UUID  string `json:"uuid"`
	Error string `json:"error"`
}

// fetchCaptcha ?????
func fetchCaptcha(baseURL string) CaptchaResult {
	reqURL := baseURL + "/api/code"
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Get(reqURL)
	if err != nil {
		fmt.Printf("[API] ???????: %v\n", err)
		return CaptchaResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var data struct {
		Code int `json:"code"`
		Data struct {
			Img  string `json:"img"`
			UUID string `json:"uuid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return CaptchaResult{Error: "??????"}
	}
	if data.Code == 200 {
		return CaptchaResult{Img: data.Data.Img, UUID: data.Data.UUID}
	}
	return CaptchaResult{Error: fmt.Sprintf("code=%d", data.Code)}
}

// LoginResult ????
type LoginResult struct {
	Token   string
	Cookies string
	Error   string
}

// doLogin ????
func doLogin(baseURL, username, password, captchaCode, captchaUUID string) LoginResult {
	encryptedPwd, err := rsaEncrypt(password)
	if err != nil {
		return LoginResult{Error: fmt.Sprintf("??????: %v", err)}
	}
	body := map[string]string{
		"username": username,
		"password": encryptedPwd,
		"code":     captchaCode,
		"uuid":     captchaUUID,
	}
	bodyBytes, _ := json.Marshal(body)
	reqURL := baseURL + "/api/auth/accountLogin"
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Post(reqURL, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return LoginResult{Error: fmt.Sprintf("????: %v", err)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return LoginResult{Error: "??????"}
	}
	code, _ := toFloat(data["code"])
	if code == 200 {
		token := extractString(data, "access_token")
		if token == "" {
			token = extractString(data, "token")
		}
		if token == "" {
			if inner, ok := data["data"].(map[string]interface{}); ok {
				token = extractString(inner, "access_token")
				if token == "" {
					token = extractString(inner, "token")
				}
			}
		}
		if token == "" {
			return LoginResult{Error: "????? token"}
		}
		// ?? cookies
		var cookieParts []string
		for _, c := range resp.Cookies() {
			cookieParts = append(cookieParts, c.Name+"="+c.Value)
		}
		return LoginResult{Token: token, Cookies: strings.Join(cookieParts, "; ")}
	}
	msg := extractString(data, "msg")
	if msg == "" {
		msg = "????"
	}
	return LoginResult{Error: fmt.Sprintf("????: %s", msg)}
}

// interruptibleSleep ??????????100ms???? abortCheck
func interruptibleSleep(d time.Duration, abortCheck func() bool) error {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if abortCheck != nil && abortCheck() {
			return fmt.Errorf("aborted")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// apiRequest ?? API ???? token ??????????
func apiRequest(method, reqURL, token, cookieStr string, body map[string]interface{}, params map[string]string, abortCheck func() bool) (map[string]interface{}, error) {
	client := &http.Client{Timeout: requestTimeout}
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, reqURL, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if cookieStr != "" {
		req.Header.Set("Cookie", cookieStr)
	}
	if params != nil {
		q := req.URL.Query()
		for k, v := range params {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	if err := interruptibleSleep(requestDelay, abortCheck); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if len(respBody) == 0 {
		return nil, fmt.Errorf("????????")
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("??????: %v", err)
	}
	return result, nil
}

// getRecordList ????????
func getRecordList(baseURL, token, cookieStr, customerName, startTime, endTime string, page, size int, abortCheck func() bool) (map[string]interface{}, error) {
	reqURL := baseURL + "/api/sessionSummary/record/getRecordList"
	body := map[string]interface{}{
		"customerName":     customerName,
		"classificationId": "",
		"userIds":          []string{},
		"wxGroupIds":       []string{},
		"groupIds":         []string{},
		"accountIds":       []string{},
		"startTime":        startTime,
		"endTime":          endTime,
		"mainFlag":         "",
		"contactType":      "",
		"adminSendFlag":    "",
	}
	params := map[string]string{
		"pageNum":  fmt.Sprintf("%d", page),
		"pageSize": fmt.Sprintf("%d", size),
	}
	return apiRequest("POST", reqURL, token, cookieStr, body, params, abortCheck)
}

// getAllRecordsByTimeRange ?????????????????? (records, remarkIndex, totalCount)
func getAllRecordsByTimeRange(baseURL, token, cookieStr, startTime, endTime string, abortCheck func() bool, progressCb func(page, totalPages, count, total int)) ([]map[string]interface{}, map[string]map[string]interface{}, int) {
	var allRows []map[string]interface{}
	page := 1
	size := 1000
	total := 0
	totalPages := 0

	for {
		if abortCheck != nil && abortCheck() {
			break
		}
		data, err := getRecordList(baseURL, token, cookieStr, "", startTime, endTime, page, size, abortCheck)
		if err != nil {
			if err.Error() == "aborted" {
				break
			}
			fmt.Printf("[API] ?????%d???: %v\n", page, err)
			break
		}
		rows := extractRows(data)
		if page == 1 {
			total, _ = toInt(data["total"])
			if total > 0 {
				totalPages = (total + size - 1) / size
			} else {
				break
			}
		}
		if len(rows) == 0 {
			break
		}
		allRows = append(allRows, rows...)
		if progressCb != nil {
			progressCb(page, totalPages, len(allRows), total)
		}
		if len(rows) < size || len(allRows) >= total {
			break
		}
		page++
		if len(allRows) >= 50000 {
			break
		}
	}

	// ??????
	remarkIndex := make(map[string]map[string]interface{})
	for _, row := range allRows {
		remark := extractString(row, "sessionRemark")
		if remark != "" {
			remarkIndex[remark] = row
		}
	}
	return allRows, remarkIndex, total
}

// findRecordByRemark ??????????ID
func findRecordByRemark(shangjiID string, remarkIndex map[string]map[string]interface{}) map[string]interface{} {
	for remark, record := range remarkIndex {
		if strings.Contains(remark, shangjiID) {
			return record
		}
	}
	return nil
}

// getChatRecords ??????
func getChatRecords(baseURL, token, cookieStr, weUserID, externalUserID, startTime, endTime string, abortCheck func() bool) ([]interface{}, error) {
	reqURL := baseURL + "/api/chatRecord/listContext"
	body := map[string]interface{}{
		"limit":          50,
		"topTime":        "",
		"weName":         "",
		"customerName":   "",
		"weUserId":       weUserID,
		"externalUserId": externalUserID,
		"first":          true,
		"startTime":      startTime,
		"endTime":        endTime,
		"type":           0,
	}
	data, err := apiRequest("POST", reqURL, token, cookieStr, body, nil, abortCheck)
	if err != nil {
		return nil, err
	}
	// data.data ??? []interface{}
	if msgs, ok := data["data"].([]interface{}); ok {
		return msgs, nil
	}
	if msgs, ok := data["rows"].([]interface{}); ok {
		return msgs, nil
	}
	return nil, nil
}

// extractChatMessages ??????????????????????
// ???????????????????????????????????
func extractChatMessages(messages []interface{}) []ParsedChatMessage {
	var out []ParsedChatMessage
	for _, raw := range messages {
		msg, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		chatRecord, ok := msg["chatRecord"].(map[string]interface{})
		if !ok {
			chatRecord = msg
		}
		msgType, hasType := toFloat(chatRecord["msgType"])
		if hasType && msgType != 10 {
			continue
		}
		text := extractString(chatRecord, "searchContent")
		if text == "" {
			if msgDataStr, ok := chatRecord["msgData"].(string); ok {
				var msgData map[string]interface{}
				if json.Unmarshal([]byte(msgDataStr), &msgData) == nil {
					text = extractString(msgData, "text")
				}
			}
		}
		if text == "" {
			for _, key := range []string{"text", "content", "msgContent", "message", "body", "title"} {
				text = extractString(chatRecord, key)
				if text != "" {
					break
				}
			}
		}
		if text == "" {
			continue
		}
		role := classifyMessageRole(msg, chatRecord)
		ts, hasTS := messageTime(msg, chatRecord)
		out = append(out, ParsedChatMessage{Role: role, Text: text, Timestamp: ts, HasTime: hasTS})
	}
	return out
}

func classifyMessageRole(msg, chatRecord map[string]interface{}) string {
	if hasField(map[string]interface{}{"msg": msg, "chat": chatRecord}, "????") {
		return "??"
	}
	if hasField(map[string]interface{}{"msg": msg, "chat": chatRecord}, "?????") {
		return "??"
	}
	for _, source := range []map[string]interface{}{msg, chatRecord} {
		if v, ok := source["senderType"].(string); ok {
			if strings.Contains(v, "??") || strings.Contains(v, "??") {
				return "??"
			}
			if strings.Contains(v, "??") || strings.Contains(v, "??") {
				return "??"
			}
		}
	}
	if sent, ok := chatRecord["isSend"].(bool); ok && sent {
		return "??"
	}
	return "??"
}

func hasField(value interface{}, field string) bool {
	switch x := value.(type) {
	case map[string]interface{}:
		for k, v := range x {
			if k == field || strings.Contains(k, field) {
				return true
			}
			if hasField(v, field) {
				return true
			}
		}
	case []interface{}:
		for _, item := range x {
			if hasField(item, field) {
				return true
			}
		}
	}
	return false
}

func messageTime(values ...map[string]interface{}) (time.Time, bool) {
	keys := []string{"msgTime", "sendTime", "messageTime", "createTime", "timestamp", "time", "talkTime"}
	for _, m := range values {
		for _, key := range keys {
			v, ok := m[key]
			if !ok {
				continue
			}
			if t, ok := parseMessageTime(v); ok {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

func parseMessageTime(v interface{}) (time.Time, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = strings.TrimSpace(x)
	case float64:
		return time.Unix(int64(x)/1000, 0), true
	case int64:
		return time.Unix(x/1000, 0), true
	case int:
		return time.Unix(int64(x)/1000, 0), true
	}
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006/01/02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000
		}
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

// extractChatText ??????????????
func extractChatText(messages []interface{}, customerName, date string) string {
	parsed := extractChatMessages(messages)
	if len(parsed) == 0 {
		return ""
	}
	lines := make([]string, 0, len(parsed)+1)
	for _, m := range parsed {
		lines = append(lines, "["+m.Role+"] "+m.Text)
	}
	if customerName != "" {
		lines = append([]string{fmt.Sprintf("--- ???%s | ???%s ---", customerName, date)}, lines...)
	}
	return strings.Join(lines, "\n")
}

type scenarioRule struct {
	Label                string
	Keywords             []string
	AIMode               AIMode
	BeforeFirstFrontline bool
	LookbackDays         int
	SameDayAsFrontline   bool
}

var scenarioRules = map[string][]scenarioRule{
	"????": {
		{Label: "??????", Keywords: []string{"??????", "????", "????"}},
		{Label: "????", Keywords: []string{"????", "??", "???"}},
		{Label: "??????", Keywords: []string{"????", "1000M", "300M", "??"}},
		{Label: "??????", Keywords: []string{"????", "????"}},
		{Label: "??????", Keywords: []string{"???", "??", "??", "??", "??", "??", "??"}, AIMode: aiModeOtherPromotion},
	},
	"????": {
		{Label: "????????", Keywords: []string{"????", "????", "??????", "????"}},
		{Label: "??????", Keywords: []string{"??", "??", "????"}},
		{Label: "??????", Keywords: []string{"???", "??", "??", "??", "??", "??", "??"}, AIMode: aiModeOtherPromotion},
	},
	"????": {
		{Label: "??????", Keywords: []string{"??????", "????", "????"}},
		{Label: "??????", Keywords: []string{"????", "????"}},
		{Label: "??????", Keywords: []string{"??????", "????"}},
		{Label: "??????", Keywords: []string{"????", "????"}},
		{Label: "??????", Keywords: []string{"???", "??", "??", "??", "??", "??", "??"}, AIMode: aiModeOtherPromotion},
	},
	"????": {
		{Label: "?????7?????", Keywords: []string{"????", "?????", "???", "????", "??", "???", "????", "????", "???"}, BeforeFirstFrontline: true, LookbackDays: 7},
		{Label: "??????????", Keywords: []string{"????", "????", "????", "??", "??", "??", "??", "??"}, AIMode: aiModeAuxMarketing, BeforeFirstFrontline: true, SameDayAsFrontline: true},
		{Label: "??????", Keywords: []string{"????", "????"}},
		{Label: "??????", Keywords: []string{"???", "??", "??", "??", "??", "??"}, AIMode: aiModeOtherPromotion},
	},
}

func detectScenario(text, bizType string) string {
	// ???????????????????????????
	if scenario := normalizeScenario(bizType); scenario != "" {
		return scenario
	}
	s := strings.ToLower(text + " " + bizType)
	orderedHints := []struct {
		name  string
		hints []string
	}{
		{name: "????", hints: []string{"????", "????", "????", "????", "?????"}},
		{name: "????", hints: []string{"????", "????", "????", "????", "???"}},
		{name: "????", hints: []string{"????", "????", "????", "???", "????", "????"}},
		{name: "????", hints: []string{"????", "??", "????", "??", "????", "???"}},
	}
	for _, item := range orderedHints {
		for _, h := range item.hints {
			if strings.Contains(s, strings.ToLower(h)) {
				return item.name
			}
		}
	}
	return ""
}

func normalizeScenario(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	if s == "" {
		return ""
	}
	switch {
	case strings.Contains(s, "??") || strings.Contains(s, "??"):
		return "????"
	case strings.Contains(s, "??") && (strings.Contains(s, "??") || strings.Contains(s, "??") || strings.Contains(s, "??")):
		return "????"
	case strings.Contains(s, "??") || strings.Contains(s, "??") || strings.Contains(s, "??") || strings.Contains(s, "??"):
		return "????"
	case strings.Contains(s, "??") || strings.Contains(s, "??") || strings.Contains(s, "??") || strings.Contains(s, "??") || strings.Contains(s, "???"):
		return "????"
	}
	return ""
}

func detectScenarioWithAI(ctx context.Context, text, bizType string, ai *aiClassifier) string {
	if scenario := detectScenario(text, bizType); scenario != "" {
		return scenario
	}
	if ai == nil {
		return ""
	}
	judgement := ai.classifyScenario(ctx, text)
	if judgement.Confidence >= 0.65 {
		return normalizeScenario(judgement.Scenario)
	}
	return ""
}

func matchedScenarioRules(messages []ParsedChatMessage, scenario string) []string {
	return matchedScenarioRulesWithAI(context.Background(), messages, scenario, nil)
}

func matchedScenarioRulesWithAI(ctx context.Context, messages []ParsedChatMessage, scenario string, ai *aiClassifier) []string {
	if scenario == "" {
		return nil
	}
	var firstFrontline time.Time
	var hasFirstFrontline bool
	for _, message := range messages {
		if message.Role == "??" && message.HasTime && (!hasFirstFrontline || message.Timestamp.Before(firstFrontline)) {
			firstFrontline = message.Timestamp
			hasFirstFrontline = true
		}
	}

	var matched []string
	for _, rule := range scenarioRules[scenario] {
		if rule.AIMode == "" {
			if hasRuleKeyword(messages, scenario, rule, firstFrontline, hasFirstFrontline) {
				matched = append(matched, rule.Label)
			}
			continue
		}

		// AI ?????????????????????????????????????
		aiText := semanticRuleContext(messages, scenario, rule, firstFrontline, hasFirstFrontline)
		if strings.TrimSpace(aiText) == "" {
			continue
		}
		judgement := ai.classify(ctx, rule.AIMode, scenario, aiText)
		if judgement.Positive && !judgement.AdministrativeOnly && judgement.Confidence >= 0.60 {
			matched = append(matched, rule.Label)
		}
	}
	return matched
}

func scopedSpecialistMessages(messages []ParsedChatMessage, rule scenarioRule, firstFrontline time.Time, hasFirstFrontline bool) []ParsedChatMessage {
	var scoped []ParsedChatMessage
	for _, message := range messages {
		if message.Role != "??" || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if rule.BeforeFirstFrontline {
			if !hasFirstFrontline || !message.HasTime || !message.Timestamp.Before(firstFrontline) {
				continue
			}
			if rule.LookbackDays > 0 && message.Timestamp.Before(firstFrontline.Add(-time.Duration(rule.LookbackDays)*24*time.Hour)) {
				continue
			}
			if rule.SameDayAsFrontline && !sameCalendarDay(message.Timestamp, firstFrontline) {
				continue
			}
		}
		scoped = append(scoped, message)
	}
	return scoped
}

func hasRuleKeyword(messages []ParsedChatMessage, scenario string, rule scenarioRule, firstFrontline time.Time, hasFirstFrontline bool) bool {
	for _, message := range scopedSpecialistMessages(messages, rule, firstFrontline, hasFirstFrontline) {
		text := normalizeBusinessText(message.Text)
		for _, keyword := range rule.Keywords {
			if strings.Contains(text, normalizeBusinessText(keyword)) {
				return true
			}
		}
	}
	return false
}

func normalizeBusinessText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, r := range []rune{' ', '\t', '\r', '\n', '?', '?', '?', '?', '?', '?', ',', '.', '!', '?', ':', ';'} {
		text = strings.ReplaceAll(text, string(r), "")
	}
	return text
}

// semanticRuleContext ??????????/????????????
// ????????????????????????????+??????????????
func semanticRuleContext(messages []ParsedChatMessage, scenario string, rule scenarioRule, firstFrontline time.Time, hasFirstFrontline bool) string {
	scoped := scopedSpecialistMessages(messages, rule, firstFrontline, hasFirstFrontline)
	if len(scoped) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("???")
	b.WriteString(scenario)
	b.WriteString("\n?????????")
	b.WriteString(rule.Label)
	b.WriteString("\n???????\n")
	candidateAdded := false
	for _, candidate := range scoped {
		if onlyCourtesyOrEmoji(candidate.Text) || isAdministrativeOnly(candidate.Text) {
			continue
		}
		// ????????????????????????????
		// ???????/??????????????????????
		if rule.AIMode == aiModeOtherPromotion && isFixedOnlySemanticMessage(candidate.Text, scenario) {
			continue
		}
		b.WriteString(formatConversationMessage(candidate))
		b.WriteByte('\n')
		candidateAdded = true
	}
	if !candidateAdded {
		return ""
	}
	b.WriteString("??????\n")
	b.WriteString(formatConversationMessages(messages))
	return b.String()
}

func isFixedOnlySemanticMessage(text, scenario string) bool {
	if !containsNonOtherRuleKeyword(text, scenario) {
		return false
	}
	return !hasDistinctOtherContent(text, scenario)
}

// hasDistinctOtherContent ????????????????????????
// ????????/??/?????????????????????????
func hasDistinctOtherContent(text, scenario string) bool {
	remaining := normalizeBusinessText(text)
	for _, rule := range scenarioRules[scenario] {
		if rule.AIMode == aiModeOtherPromotion {
			continue
		}
		for _, keyword := range rule.Keywords {
			remaining = strings.ReplaceAll(remaining, normalizeBusinessText(keyword), "")
		}
	}
	for _, stop := range []string{
		"??", "?", "?", "?", "?", "??", "?", "?", "?", "??", "??", "??", "??",
		"?", "?", "?", "?", "?", "?", "?", "??", "??", "??", "??", "??", "??",
		"??", "??", "??", "?", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "??", "?",
		"??", "??", "??", "??", "??",
		"?", "??", "?", "??", "????", "????", "????", "????",
		"??", "??", "??", "?", "??", "??", "??", "??",
	} {
		remaining = strings.ReplaceAll(remaining, normalizeBusinessText(stop), "")
	}
	return strings.TrimSpace(remaining) != ""
}

func hasOtherPromotionSignal(text string) bool {
	text = normalizeBusinessText(text)
	for _, hint := range []string{
		"??", "??", "??", "??", "??", "??", "??", "???", "???", "??",
		"??", "??", "??", "??", "???", "??", "??", "??", "??", "??",
		"??", "??", "??", "??", "???", "???", "???",
	} {
		if strings.Contains(text, normalizeBusinessText(hint)) {
			return true
		}
	}
	return false
}

func otherPromotionText(messages []ParsedChatMessage, scenario string) string {
	rule := scenarioRule{Label: "??????", AIMode: aiModeOtherPromotion}
	return semanticRuleContext(messages, scenario, rule, time.Time{}, false)
}

func formatConversationMessage(message ParsedChatMessage) string {
	if message.HasTime {
		return fmt.Sprintf("[%s][%s] %s", message.Role, message.Timestamp.Format("2006-01-02 15:04:05"), strings.TrimSpace(message.Text))
	}
	return fmt.Sprintf("[%s] %s", message.Role, strings.TrimSpace(message.Text))
}

func formatConversationMessages(messages []ParsedChatMessage) string {
	var lines []string
	for _, message := range messages {
		if strings.TrimSpace(message.Text) == "" {
			continue
		}
		lines = append(lines, formatConversationMessage(message))
	}
	return strings.Join(lines, "\n")
}

func containsNonOtherRuleKeyword(text, scenario string) bool {
	for _, rule := range scenarioRules[scenario] {
		if rule.AIMode == aiModeOtherPromotion {
			continue
		}
		for _, keyword := range rule.Keywords {
			if strings.Contains(text, keyword) {
				return true
			}
		}
	}
	return false
}

func sameCalendarDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func onlyCourtesyOrEmoji(text string) bool {
	remaining := text
	for _, kw := range []string{"/??", "/??", "/??", "/??", "/OK", "/?", "??", "??", "??", "???", "?", "?", "?", "ok", "OK", "??", "??", "??", "??", "??", "?", "??", "????", "????", "????", "?OK?", "???"} {
		remaining = strings.ReplaceAll(remaining, kw, "")
	}
	for _, p := range []string{"?", "?", "?", "?", "?", "?", "?", ",", ".", "!", "?", ":", ";", " ", "\t", "~", "?", "?", "/"} {
		remaining = strings.ReplaceAll(remaining, p, "")
	}
	for _, r := range strings.TrimSpace(remaining) {
		if !isEmojiRune(r) {
			return false
		}
	}
	return true
}

// isAdministrativeOnly ????????????????????????????
// ???????????????????????????????????
func isAdministrativeOnly(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if onlyCourtesyOrEmoji(text) {
		return true
	}
	adminPhrases := []string{
		"??????", "????", "?????", "?????", "?????",
		"?????", "????", "????", "????",
		"????", "????", "?????", "??????", "????",
		"??????", "???????",
	}
	remaining := text
	found := false
	for _, phrase := range adminPhrases {
		if strings.Contains(remaining, phrase) {
			found = true
			remaining = strings.ReplaceAll(remaining, phrase, "")
		}
	}
	if !found {
		return false
	}
	for _, p := range []string{"?", "?", "?", "??", "?", "??", "??", "?", "?", "?", "?", "?", ",", ".", "!", "?", "?", " ", "\t", "\r", "\n"} {
		remaining = strings.ReplaceAll(remaining, p, "")
	}
	return strings.TrimSpace(remaining) == ""
}

func isEmojiRune(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) ||
		(r >= 0x2600 && r <= 0x27BF) ||
		r == 0x200D || r == 0xFE0F || (r >= 0x1F3FB && r <= 0x1F3FF)
}

func hasTransactionIntent(text string) bool {
	text = strings.TrimSpace(text)
	// ?????/????/?????????????????????
	for _, phrase := range []string{
		"???", "???", "???", "????", "????", "???",
		"????", "????", "???", "????", "??", "????",
		"????", "????", "???", "?????", "????",
		"?????", "????",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func hasRecoveryContext(text string) bool {
	for _, keyword := range []string{"????", "????", "??", "??", "??", "????", "????"} {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

// isInterruption ???????????????????????
// ???????????????????????????
func isInterruption(messages []ParsedChatMessage, specialist ParsedChatMessage) bool {
	if !specialist.HasTime {
		return false
	}
	var latestCustomer, latestFrontline *ParsedChatMessage
	for i := range messages {
		message := &messages[i]
		if !message.HasTime || message.Timestamp.After(specialist.Timestamp) {
			continue
		}
		switch message.Role {
		case "??":
			if latestCustomer == nil || message.Timestamp.After(latestCustomer.Timestamp) {
				latestCustomer = message
			}
		case "??":
			if latestFrontline == nil || message.Timestamp.After(latestFrontline.Timestamp) {
				latestFrontline = message
			}
		}
	}
	if latestCustomer == nil || latestFrontline == nil {
		return false
	}

	// ?????????????????????????????????
	// ?????????????????????????
	if !latestFrontline.Timestamp.After(latestCustomer.Timestamp) {
		return false
	}
	return specialist.Timestamp.Sub(latestFrontline.Timestamp) >= 0 && specialist.Timestamp.Sub(latestFrontline.Timestamp) < 10*time.Minute
}

func interventionSituation(matched []string, hasConversation bool) string {
	if !hasConversation {
		return "?????"
	}
	switch len(matched) {
	case 1:
		return "??1?????"
	case 2:
		return "??2?????"
	case 3:
		return "??3????????"
	default:
		return "???????"
	}
}

// analyzeConversation ??????????????????????????????
func analyzeConversation(messages []ParsedChatMessage, bizType string, adminSendFlag int) (result, interruption, scenario string, matched []string, roles, situation string) {
	return analyzeConversationWithAI(context.Background(), messages, bizType, adminSendFlag, nil)
}

func analyzeConversationWithAI(ctx context.Context, messages []ParsedChatMessage, bizType string, adminSendFlag int, ai *aiClassifier) (result, interruption, scenario string, matched []string, roles, situation string) {
	if adminSendFlag == 0 || len(messages) == 0 {
		return "???", "?", "", nil, "", "???????"
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].HasTime != messages[j].HasTime {
			return messages[i].HasTime
		}
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})
	var frontline, specialist, customer []ParsedChatMessage
	for _, m := range messages {
		if m.Role == "??" {
			frontline = append(frontline, m)
		}
		if m.Role == "??" {
			specialist = append(specialist, m)
		}
		if m.Role == "??" {
			customer = append(customer, m)
		}
	}
	if len(specialist) == 0 {
		return "???", "?", "", nil, "??", "???????"
	}
	for _, sp := range specialist {
		if isInterruption(append(append(append([]ParsedChatMessage{}, frontline...), customer...), specialist...), sp) {
			scenario = detectScenarioWithAI(ctx, formatConversationMessages(messages), bizType, ai)
			matched = matchedScenarioRulesWithAI(ctx, append(append(append([]ParsedChatMessage{}, frontline...), customer...), specialist...), scenario, ai)
			return "??", "?", scenario, matched, "??/??", interventionSituation(matched, true)
		}
	}
	frontlineText := joinMessages(frontline)
	specialistText := joinMessages(specialist)
	allMessages := append(append(append([]ParsedChatMessage{}, frontline...), customer...), specialist...)
	scenario = detectScenarioWithAI(ctx, formatConversationMessages(allMessages), bizType, ai)
	matched = matchedScenarioRulesWithAI(ctx, allMessages, scenario, ai)
	situation = interventionSituation(matched, true)
	// ????/?????????????????????????????
	if hasTransactionIntent(frontlineText+joinMessages(customer)) && !hasRecoveryContext(frontlineText+specialistText+joinMessages(customer)) {
		return "???????", "?", scenario, matched, "??/??", situation
	}
	if scenario != "" && len(matched) > 0 && !onlyCourtesyOrEmoji(specialistText) && !isAdministrativeOnly(specialistText) {
		return "????", "?", scenario, matched, "??/??", situation
	}
	return "???????", "?", scenario, matched, "??/??", "???????"
}

func joinMessages(messages []ParsedChatMessage) string {
	var text string
	for _, message := range messages {
		text += message.Text + " "
	}
	return text
}

// ---- ???? ----

func toFloat(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func toInt(v interface{}) (int, bool) {
	f, ok := toFloat(v)
	return int(f), ok
}

func extractString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func extractRows(data map[string]interface{}) []map[string]interface{} {
	raw, ok := data["rows"].([]interface{})
	if !ok {
		return nil
	}
	var rows []map[string]interface{}
	for _, r := range raw {
		if row, ok := r.(map[string]interface{}); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

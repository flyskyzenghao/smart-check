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

// ParsedChatMessage 是规则引擎使用的标准化消息。
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

// rsaPublicKeyB64 512-bit RSA 公钥（从 SCRM 前端 jsencrypt 提取）
const rsaPublicKeyB64 = "MFwwDQYJKoZIhvcNAQEBBQADSwAwSAJBAKoR8mX0rGKLqzcWmOzbfj64K8ZIgOdH" +
	"nzkXSOVOZbFu/TJhZ7rFAN+eaGkl3C4buccQd/EjEsj9ir7ijT7h96MCAwEAAQ=="

// parseBaseURL 从完整 URL 中提取 scheme://host:port
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

// rsaEncrypt 手动 RSA PKCS1v15 加密 + Base64
// 不依赖 crypto/rsa，因为 Go 1.24+ 禁止 <1024-bit 密钥，
// 但 SCRM 系统使用 512-bit 密钥，必须手动实现。
func rsaEncrypt(plaintext string) (string, error) {
	derBytes, err := base64.StdEncoding.DecodeString(rsaPublicKeyB64)
	if err != nil {
		return "", fmt.Errorf("base64 decode public key: %w", err)
	}

	// 手动解析 ASN.1 DER 格式的 PKIX 公钥
	// 结构: SEQUENCE { SEQUENCE { OID, NULL }, BIT STRING { SEQUENCE { INTEGER(n), INTEGER(e) } } }
	n, e, err := parsePKIXPublicKey(derBytes)
	if err != nil {
		return "", err
	}

	// PKCS1v15 填充: [0x00, 0x02, random_nonzero_bytes..., 0x00, plaintext]
	keyBytes := (n.BitLen() + 7) / 8
	plainBytes := []byte(plaintext)
	if len(plainBytes) > keyBytes-11 {
		return "", fmt.Errorf("plaintext too long for key size")
	}

	padded := make([]byte, keyBytes)
	padded[0] = 0x00
	padded[1] = 0x02

	// 填充随机非零字节
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

	// RSA 加密: c = m^e mod n
	m := new(big.Int).SetBytes(padded)
	c := new(big.Int).Exp(m, big.NewInt(int64(e)), n)

	// 输出补齐到密钥字节长度
	cBytes := c.Bytes()
	result := make([]byte, keyBytes)
	copy(result[keyBytes-len(cBytes):], cBytes)

	return base64.StdEncoding.EncodeToString(result), nil
}

// parsePKIXPublicKey 从 DER 字节中手动解析 RSA 公钥的 N 和 E
func parsePKIXPublicKey(der []byte) (*big.Int, int, error) {
	// 外层 SEQUENCE
	seq1, _, err := parseASN1Sequence(der)
	if err != nil {
		return nil, 0, err
	}
	// 第一个元素: AlgorithmIdentifier SEQUENCE { OID, NULL }
	// 第二个元素: BIT STRING (包含公钥数据)
	if len(seq1) < 2 {
		return nil, 0, fmt.Errorf("invalid PKIX structure")
	}

	// 解析 BIT STRING (tag=0x03)
	bitString := seq1[1]
	if len(bitString) < 3 || bitString[0] != 0x03 {
		return nil, 0, fmt.Errorf("expected BIT STRING")
	}
	bitStringLen := parseASN1Length(bitString[1:])
	// BIT STRING 内容: [unused_bits_count, ...inner_content]
	// unused bits count 应为 0
	bitContent := bitString[2+bitStringLen-len(bitString[2:]):]
	if len(bitContent) < 2 {
		return nil, 0, fmt.Errorf("BIT STRING too short")
	}
	// 跳过 unused bits 字节
	innerDER := bitContent[1:]

	// 内层 SEQUENCE { INTEGER(n), INTEGER(e) }
	seq2, _, err := parseASN1Sequence(innerDER)
	if err != nil {
		return nil, 0, err
	}
	if len(seq2) < 2 {
		return nil, 0, fmt.Errorf("RSA key needs N and E")
	}

	// 解析 N (INTEGER)
	nBytes := parseASN1Integer(seq2[0])
	n := new(big.Int).SetBytes(nBytes)

	// 解析 E (INTEGER)
	eBytes := parseASN1Integer(seq2[1])
	e := 0
	for _, b := range eBytes {
		e = e*256 + int(b)
	}

	return n, e, nil
}

// parseASN1Sequence 解析 ASN.1 SEQUENCE，返回子元素列表
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

// parseASN1Length 解析 ASN.1 长度字段
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

// parseASN1Integer 提取 INTEGER 的值字节
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
	// 去掉前导 0x00 (ASN.1 正数填充)
	for len(value) > 1 && value[0] == 0 {
		value = value[1:]
	}
	return value
}

// CaptchaResult 验证码结果
type CaptchaResult struct {
	Img   string `json:"img"`
	UUID  string `json:"uuid"`
	Error string `json:"error"`
}

// fetchCaptcha 获取验证码
func fetchCaptcha(baseURL string) CaptchaResult {
	reqURL := baseURL + "/api/code"
	client := &http.Client{Timeout: requestTimeout}
	resp, err := client.Get(reqURL)
	if err != nil {
		fmt.Printf("[API] 验证码请求异常: %v\n", err)
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
		return CaptchaResult{Error: "解析响应失败"}
	}
	if data.Code == 200 {
		return CaptchaResult{Img: data.Data.Img, UUID: data.Data.UUID}
	}
	return CaptchaResult{Error: fmt.Sprintf("code=%d", data.Code)}
}

// LoginResult 登录结果
type LoginResult struct {
	Token   string
	Cookies string
	Error   string
}

// doLogin 执行登录
func doLogin(baseURL, username, password, captchaCode, captchaUUID string) LoginResult {
	encryptedPwd, err := rsaEncrypt(password)
	if err != nil {
		return LoginResult{Error: fmt.Sprintf("密码加密失败: %v", err)}
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
		return LoginResult{Error: fmt.Sprintf("请求异常: %v", err)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var data map[string]interface{}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return LoginResult{Error: "解析响应失败"}
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
			return LoginResult{Error: "登录响应无 token"}
		}
		// 拼接 cookies
		var cookieParts []string
		for _, c := range resp.Cookies() {
			cookieParts = append(cookieParts, c.Name+"="+c.Value)
		}
		return LoginResult{Token: token, Cookies: strings.Join(cookieParts, "; ")}
	}
	msg := extractString(data, "msg")
	if msg == "" {
		msg = "未知错误"
	}
	return LoginResult{Error: fmt.Sprintf("登录失败: %s", msg)}
}

// interruptibleSleep 可中断的延迟等待，每100ms检查一次 abortCheck
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

// apiRequest 通用 API 请求（带 token 鉴权），支持中止检测
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
		return nil, fmt.Errorf("服务器返回空响应")
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}
	return result, nil
}

// getRecordList 查询会话小结列表
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

// getAllRecordsByTimeRange 按时间范围分页获取全部会话小结，返回 (records, remarkIndex, totalCount)
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
			fmt.Printf("[API] 批量查询第%d页失败: %v\n", page, err)
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

	// 构建备注索引
	remarkIndex := make(map[string]map[string]interface{})
	for _, row := range allRows {
		remark := extractString(row, "sessionRemark")
		if remark != "" {
			remarkIndex[remark] = row
		}
	}
	return allRows, remarkIndex, total
}

// findRecordByRemark 通过备注字段匹配商机ID
func findRecordByRemark(shangjiID string, remarkIndex map[string]map[string]interface{}) map[string]interface{} {
	for remark, record := range remarkIndex {
		if strings.Contains(remark, shangjiID) {
			return record
		}
	}
	return nil
}

// getChatRecords 获取聊天记录
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
	// data.data 可能是 []interface{}
	if msgs, ok := data["data"].([]interface{}); ok {
		return msgs, nil
	}
	if msgs, ok := data["rows"].([]interface{}); ok {
		return msgs, nil
	}
	return nil, nil
}

// extractChatMessages 标准化消息角色、正文和时间。字段名和值中出现
// “运营专员”判为专员，出现“企微号名称”判为一线；旧接口字段作为兜底。
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
	if hasField(map[string]interface{}{"msg": msg, "chat": chatRecord}, "运营专员") {
		return "专员"
	}
	if hasField(map[string]interface{}{"msg": msg, "chat": chatRecord}, "企微号名称") {
		return "一线"
	}
	for _, source := range []map[string]interface{}{msg, chatRecord} {
		if v, ok := source["senderType"].(string); ok {
			if strings.Contains(v, "专员") || strings.Contains(v, "运营") {
				return "专员"
			}
			if strings.Contains(v, "一线") || strings.Contains(v, "客服") {
				return "一线"
			}
		}
	}
	if sent, ok := chatRecord["isSend"].(bool); ok && sent {
		return "一线"
	}
	return "客户"
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

// extractChatText 从标准化消息格式化聊天记录。
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
		lines = append([]string{fmt.Sprintf("--- 客户：%s | 时间：%s ---", customerName, date)}, lines...)
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
	"新装宽带": {
		{Label: "宽带安装地址", Keywords: []string{"宽带安装地址", "安装地址", "宽带地址"}},
		{Label: "宽带类型", Keywords: []string{"宽带类型", "家用", "公司用"}},
		{Label: "宽带速率要求", Keywords: []string{"宽带速率", "1000M", "300M", "速率"}},
		{Label: "预约上门时间", Keywords: []string{"预约上门", "上门时间"}},
		{Label: "其他促成信息", Keywords: []string{"有帮助", "帮助", "办理", "套餐", "资费", "价格", "优惠"}, AIMode: aiModeOtherPromotion},
	},
	"新装移动": {
		{Label: "是否有装宽带需求", Keywords: []string{"无装宽带", "宽带需求", "有没有装宽带", "没有宽带"}},
		{Label: "流量使用需求", Keywords: []string{"流量", "每月", "多少流量"}},
		{Label: "其他促成信息", Keywords: []string{"有帮助", "帮助", "办理", "套餐", "资费", "价格", "优惠"}, AIMode: aiModeOtherPromotion},
	},
	"新装专线": {
		{Label: "专线安装地址", Keywords: []string{"专线安装地址", "公司地址", "专线地址"}},
		{Label: "专线产品介绍", Keywords: []string{"专线产品", "产品介绍"}},
		{Label: "专线使用场景", Keywords: []string{"专线使用场景", "使用场景"}},
		{Label: "预约上门时间", Keywords: []string{"预约上门", "上门时间"}},
		{Label: "其他促成信息", Keywords: []string{"有帮助", "帮助", "办理", "套餐", "资费", "价格", "优惠"}, AIMode: aiModeOtherPromotion},
	},
	"存量提值": {
		{Label: "一线介入前7天服务闭环", Keywords: []string{"服务回溯", "问题已解决", "已解决", "处理完成", "闭环", "已处理", "处理完毕", "完成服务", "解决了"}, BeforeFirstFrontline: true, LookbackDays: 7},
		{Label: "一线介入当日辅助营销", Keywords: []string{"需求挖掘", "优惠推荐", "辅助营销", "提值", "套餐", "资费", "价格", "优惠"}, AIMode: aiModeAuxMarketing, BeforeFirstFrontline: true, SameDayAsFrontline: true},
		{Label: "预约上门时间", Keywords: []string{"预约上门", "上门时间"}},
		{Label: "其他促成信息", Keywords: []string{"有帮助", "帮助", "办理", "安装", "续约", "升级"}, AIMode: aiModeOtherPromotion},
	},
}

func detectScenario(text, bizType string) string {
	// 业务分类字段优先，但兼容后台常见的别名和带前后缀名称。
	if scenario := normalizeScenario(bizType); scenario != "" {
		return scenario
	}
	s := strings.ToLower(text + " " + bizType)
	orderedHints := []struct {
		name  string
		hints []string
	}{
		{name: "新装专线", hints: []string{"新装专线", "专线产品", "专线安装", "企业专线", "互联网专线"}},
		{name: "新装宽带", hints: []string{"新装宽带", "宽带安装", "宽带类型", "家庭宽带", "装宽带"}},
		{name: "新装移动", hints: []string{"新装移动", "流量需求", "无装宽带", "手机卡", "移动业务", "移动号码"}},
		{name: "存量提值", hints: []string{"存量提值", "提值", "优惠推荐", "续约", "升级套餐", "老用户"}},
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
	case strings.Contains(s, "专线") || strings.Contains(s, "专网"):
		return "新装专线"
	case strings.Contains(s, "宽带") && (strings.Contains(s, "新装") || strings.Contains(s, "安装") || strings.Contains(s, "家庭")):
		return "新装宽带"
	case strings.Contains(s, "移动") || strings.Contains(s, "手机") || strings.Contains(s, "流量") || strings.Contains(s, "号码"):
		return "新装移动"
	case strings.Contains(s, "存量") || strings.Contains(s, "提值") || strings.Contains(s, "续约") || strings.Contains(s, "升级") || strings.Contains(s, "老用户"):
		return "存量提值"
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
		if message.Role == "一线" && message.HasTime && (!hasFirstFrontline || message.Timestamp.Before(firstFrontline)) {
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

		// AI 规则必须基于完整上下文判断，不能用“含一个固定关键词就整条过滤”的旧逻辑。
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
		if message.Role != "专员" || strings.TrimSpace(message.Text) == "" {
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
	for _, r := range []rune{' ', '\t', '\r', '\n', '，', '。', '！', '？', '：', '；', ',', '.', '!', '?', ':', ';'} {
		text = strings.ReplaceAll(text, string(r), "")
	}
	return text
}

// semanticRuleContext 将候选专员消息和客户/一线上下文一起传给模型。
// 候选消息不再因为同时命中固定条件而被整条丢弃，避免“地址+优惠”只计到地址而漏掉优惠。
func semanticRuleContext(messages []ParsedChatMessage, scenario string, rule scenarioRule, firstFrontline time.Time, hasFirstFrontline bool) string {
	scoped := scopedSpecialistMessages(messages, rule, firstFrontline, hasFirstFrontline)
	if len(scoped) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("场景：")
	b.WriteString(scenario)
	b.WriteString("\n当前要判断的规则：")
	b.WriteString(rule.Label)
	b.WriteString("\n候选专员消息：\n")
	candidateAdded := false
	for _, candidate := range scoped {
		if onlyCourtesyOrEmoji(candidate.Text) || isAdministrativeOnly(candidate.Text) {
			continue
		}
		// 对“其他促成信息”只跳过纯固定条件消息；一条消息同时包含
		// 固定条件和优惠/需求挖掘等内容时仍保留，避免旧逻辑整条漏判。
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
	b.WriteString("完整上下文：\n")
	b.WriteString(formatConversationMessages(messages))
	return b.String()
}

func isFixedOnlySemanticMessage(text, scenario string) bool {
	if !containsNonOtherRuleKeyword(text, scenario) {
		return false
	}
	return !hasDistinctOtherContent(text, scenario)
}

// hasDistinctOtherContent 从消息中去掉当前场景的固定关键信息和常见连接词，
// 只要还剩下“优惠/查询/办理推进”等额外业务内容，才允许计入其他促成信息。
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
		"请问", "请", "问", "您", "我", "还是", "是", "和", "与", "以及", "并且", "另外", "同时",
		"的", "了", "吗", "呢", "吧", "哦", "嗯", "一下", "目前", "现在", "今天", "昨天", "明天",
		"需要", "需求", "想要", "想", "了解", "确认", "看看", "看下", "帮您", "帮我", "可以", "能否", "是否", "多少", "几个", "几",
		"地址", "类型", "速率", "时间", "上门",
		"做", "进行", "并", "推荐", "辅助营销", "需求挖掘", "现状确认", "需求确认",
		"上次", "服务", "问题", "已", "完成", "闭环", "处理", "解决",
	} {
		remaining = strings.ReplaceAll(remaining, normalizeBusinessText(stop), "")
	}
	return strings.TrimSpace(remaining) != ""
}

func hasOtherPromotionSignal(text string) bool {
	text = normalizeBusinessText(text)
	for _, hint := range []string{
		"优惠", "套餐", "资费", "价格", "号码", "查下", "查询", "帮您查", "提供下", "发我",
		"推荐", "比较", "需求", "住宅", "公司用", "现用", "电信", "移动", "联通", "流量",
		"办理", "续约", "升级", "业务", "使用吗", "有没有", "需要吗",
	} {
		if strings.Contains(text, normalizeBusinessText(hint)) {
			return true
		}
	}
	return false
}

func otherPromotionText(messages []ParsedChatMessage, scenario string) string {
	rule := scenarioRule{Label: "其他促成信息", AIMode: aiModeOtherPromotion}
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
	for _, kw := range []string{"/鲜花", "/抱拳", "/握手", "/玫瑰", "/OK", "/强", "好的", "谢谢", "收到", "不客气", "嗯", "好", "行", "ok", "OK", "👍", "🙏", "🌹", "💪", "👌", "✅", "🆗", "【抱拳】", "【玫瑰】", "【握手】", "【OK】", "【强】"} {
		remaining = strings.ReplaceAll(remaining, kw, "")
	}
	for _, p := range []string{"、", "，", "。", "！", "？", "：", "；", ",", ".", "!", "?", ":", ";", " ", "\t", "~", "～", "·", "/"} {
		remaining = strings.ReplaceAll(remaining, p, "")
	}
	for _, r := range strings.TrimSpace(remaining) {
		if !isEmojiRune(r) {
			return false
		}
	}
	return true
}

// isAdministrativeOnly 识别“记得带身份证”“到现场找我、不用排队”等行政提醒。
// 这些内容可以出现在营销会话里，但按业务规则不能计为关键信息或促成信息。
func isAdministrativeOnly(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	if onlyCourtesyOrEmoji(text) {
		return true
	}
	adminPhrases := []string{
		"记得带身份证", "带身份证", "带好身份证", "带上身份证", "携带身份证",
		"记得带证件", "带好证件", "带上证件", "携带证件",
		"不用排队", "无需排队", "到现场找我", "到现场联系我", "到店找我",
		"到营业厅找我", "到营业厅联系我",
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
	for _, p := range []string{"请", "您", "的", "记得", "到", "现场", "联系", "我", "，", "。", "！", "？", ",", ".", "!", "?", "、", " ", "\t", "\r", "\n"} {
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
	// “可以办理/帮您办理/请问办理”是营销推进，不代表客户已经成交。
	for _, phrase := range []string{
		"已成交", "成交了", "已下单", "下单成功", "已经办理", "已办理",
		"确认办理", "确定办理", "已订购", "已经订购", "已订", "订购成功",
		"购买成功", "已经购买", "办好了", "就这个套餐", "我确定要",
		"可以下单了", "确认下单",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func hasRecoveryContext(text string) bool {
	for _, keyword := range []string{"联系不上", "联系不到", "失联", "流失", "挽回", "回访失败", "无法联系"} {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

// isInterruption 只在一线和客户已经形成对话后判断专员是否插入。
// 客户先发、专员直接回复且此前没有一线消息，不属于插话。
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
		case "客户":
			if latestCustomer == nil || message.Timestamp.After(latestCustomer.Timestamp) {
				latestCustomer = message
			}
		case "一线":
			if latestFrontline == nil || message.Timestamp.After(latestFrontline.Timestamp) {
				latestFrontline = message
			}
		}
	}
	if latestCustomer == nil || latestFrontline == nil {
		return false
	}

	// 最新一条必须是客户消息后的“一线回复”。如果客户消息仍是最新消息，
	// 说明专员先于一线回复，不属于一线正在聊天时的插话。
	if !latestFrontline.Timestamp.After(latestCustomer.Timestamp) {
		return false
	}
	return specialist.Timestamp.Sub(latestFrontline.Timestamp) >= 0 && specialist.Timestamp.Sub(latestFrontline.Timestamp) < 10*time.Minute
}

func interventionSituation(matched []string, hasConversation bool) string {
	if !hasConversation {
		return "无相关会话"
	}
	switch len(matched) {
	case 1:
		return "键入1条关键信息"
	case 2:
		return "键入2条关键信息"
	case 3:
		return "键入3条及以上关键信息"
	default:
		return "无关键信息介入"
	}
}

// analyzeConversation 输出审核结果、插话状态、场景、命中条件、参与角色和介入情况。
func analyzeConversation(messages []ParsedChatMessage, bizType string, adminSendFlag int) (result, interruption, scenario string, matched []string, roles, situation string) {
	return analyzeConversationWithAI(context.Background(), messages, bizType, adminSendFlag, nil)
}

func analyzeConversationWithAI(ctx context.Context, messages []ParsedChatMessage, bizType string, adminSendFlag int, ai *aiClassifier) (result, interruption, scenario string, matched []string, roles, situation string) {
	if adminSendFlag == 0 || len(messages) == 0 {
		return "未介入", "否", "", nil, "", "无关键信息介入"
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].HasTime != messages[j].HasTime {
			return messages[i].HasTime
		}
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})
	var frontline, specialist, customer []ParsedChatMessage
	for _, m := range messages {
		if m.Role == "一线" {
			frontline = append(frontline, m)
		}
		if m.Role == "专员" {
			specialist = append(specialist, m)
		}
		if m.Role == "客户" {
			customer = append(customer, m)
		}
	}
	if len(specialist) == 0 {
		return "未介入", "否", "", nil, "一线", "无关键信息介入"
	}
	for _, sp := range specialist {
		if isInterruption(append(append(append([]ParsedChatMessage{}, frontline...), customer...), specialist...), sp) {
			scenario = detectScenarioWithAI(ctx, formatConversationMessages(messages), bizType, ai)
			matched = matchedScenarioRulesWithAI(ctx, append(append(append([]ParsedChatMessage{}, frontline...), customer...), specialist...), scenario, ai)
			return "插话", "是", scenario, matched, "一线/专员", interventionSituation(matched, true)
		}
	}
	frontlineText := joinMessages(frontline)
	specialistText := joinMessages(specialist)
	allMessages := append(append(append([]ParsedChatMessage{}, frontline...), customer...), specialist...)
	scenario = detectScenarioWithAI(ctx, formatConversationMessages(allMessages), bizType, ai)
	matched = matchedScenarioRulesWithAI(ctx, allMessages, scenario, ai)
	situation = interventionSituation(matched, true)
	// 仅当一线/客户明确表现为已成交，且没有联系不上等流失挽回背景时排除。
	if hasTransactionIntent(frontlineText+joinMessages(customer)) && !hasRecoveryContext(frontlineText+specialistText+joinMessages(customer)) {
		return "不纳入有效介入", "否", scenario, matched, "一线/专员", situation
	}
	if scenario != "" && len(matched) > 0 && !onlyCourtesyOrEmoji(specialistText) && !isAdministrativeOnly(specialistText) {
		return "有效营销", "否", scenario, matched, "一线/专员", situation
	}
	return "无关键信息介入", "否", scenario, matched, "一线/专员", "无关键信息介入"
}

func joinMessages(messages []ParsedChatMessage) string {
	var text string
	for _, message := range messages {
		text += message.Text + " "
	}
	return text
}

// ---- 辅助函数 ----

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

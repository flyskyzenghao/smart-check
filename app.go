package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ResultItem ????
type ResultItem struct {
	ID                    string `json:"id"`
	Idx                   int    `json:"idx"`
	ShangjiID             string `json:"shangji_id"`
	CustomerName          string `json:"customer_name"`
	Status                string `json:"status"`
	Intervention          string `json:"intervention"`
	InterventionSituation string `json:"intervention_situation"`
	Interruption          string `json:"interruption"`
	Scenario              string `json:"scenario"`
	MatchedRules          string `json:"matched_rules"`
	Roles                 string `json:"roles"`
	ChatText              string `json:"chat_text"`
	SubmitTime            string `json:"submit_time"`
	BizType               string `json:"biz_type"`
	RowNumber             int    `json:"row_number"`
}

// App ??????????????? Wails ?????????
type App struct {
	ctx       context.Context
	config    *ConfigManager
	baseURL   string
	ai        *aiClassifier
	token     string
	cookies   string
	running   bool
	aborted   bool
	mu        sync.Mutex
	results   []ResultItem
	inputPath string
}

// NewApp ??????
func NewApp() *App {
	return &App{
		config: nil, // startup ????
	}
}

// startup Wails ?????????
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// ?? exe ??????????
	exeDir := "."
	env := runtime.Environment(ctx)
	if env.BuildType == "production" {
		if exePath, err := os.Executable(); err == nil {
			exeDir = filepath.Dir(exePath)
		}
	}
	a.config = NewConfigManager(exeDir)

	// ? config ?? base URL
	cfg := a.config.LoadConfig()
	if serverURL, ok := cfg["server_url"].(string); ok && serverURL != "" {
		a.baseURL = parseBaseURL(serverURL)
	} else {
		a.baseURL = defaultBaseURL
	}
	a.ai = newAIClassifier(a.config.LoadAIConfig())
	fmt.Printf("[App] ?????baseURL=%s?configDir=%s\n", a.baseURL, exeDir)
}

// ==================== Wails ???? ====================

// GetConfig ????????????
func (a *App) GetConfig() map[string]interface{} {
	cfg := a.config.LoadConfig()
	return map[string]interface{}{
		"username":     cfg["username"],
		"login_url":    cfg["login_url"],
		"is_logged_in": a.config.IsTokenValid(),
		"ai":           a.config.PublicAIConfig(),
	}
}

// GetAIConfig ?? AI ???????? API Key ???
func (a *App) GetAIConfig() map[string]interface{} {
	return a.config.PublicAIConfig()
}

// SaveAIConfig ??????? AI ???
func (a *App) SaveAIConfig(enabled bool, endpoint, model, apiKey string, timeoutSeconds int) map[string]interface{} {
	current := a.config.LoadAIConfig()
	if current.Locked {
		return map[string]interface{}{"success": false, "error": "??????? AI ???????"}
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = current.APIKey
	}
	cfg := AIConfig{Enabled: enabled, Locked: current.Locked, Endpoint: strings.TrimSpace(endpoint), Model: strings.TrimSpace(model), APIKey: strings.TrimSpace(apiKey), TimeoutSeconds: timeoutSeconds}
	if cfg.Endpoint == "" {
		cfg.Endpoint = current.Endpoint
	}
	if cfg.Model == "" {
		cfg.Model = current.Model
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = current.TimeoutSeconds
	}
	a.config.SaveAIConfig(cfg)
	a.ai = newAIClassifier(cfg)
	return map[string]interface{}{"success": true, "config": a.config.PublicAIConfig()}
}

// TestAIConfig ??????????????????
func (a *App) TestAIConfig() map[string]interface{} {
	if a.ai == nil || !a.ai.configured() {
		return map[string]interface{}{"success": false, "error": "AI ??????? API Key"}
	}
	judgement := a.ai.classify(context.Background(), aiModeOtherPromotion, "????", "?????????????")
	if judgement.UsedFallback {
		errMsg := judgement.Error
		if errMsg == "" {
			errMsg = "AI???????????????"
		}
		return map[string]interface{}{"success": false, "fallback": true, "error": errMsg, "positive": judgement.Positive, "confidence": judgement.Confidence, "reason": judgement.Reason}
	}
	return map[string]interface{}{"success": true, "fallback": false, "positive": judgement.Positive, "confidence": judgement.Confidence, "evidence": judgement.Evidence, "reason": judgement.Reason}
}

// RefreshCaptcha ?????
func (a *App) RefreshCaptcha() map[string]string {
	result := fetchCaptcha(a.baseURL)
	if result.Img != "" && result.UUID != "" {
		return map[string]string{
			"img":  "data:image/png;base64," + result.Img,
			"uuid": result.UUID,
		}
	}
	errMsg := result.Error
	if errMsg == "" {
		errMsg = "???????"
	}
	return map[string]string{"error": errMsg}
}

// Login ??
func (a *App) Login(username, password, captchaCode, captchaUUID string) map[string]interface{} {
	// ????????
	cfg := a.config.LoadConfig()
	cfg["username"] = username
	a.config.SaveConfig(cfg)

	result := doLogin(a.baseURL, username, password, captchaCode, captchaUUID)
	if result.Token != "" {
		a.token = result.Token
		a.cookies = result.Cookies
		a.config.SaveToken(result.Token, result.Cookies)
		return map[string]interface{}{"success": true}
	}
	errMsg := result.Error
	if errMsg == "" {
		errMsg = "????"
	}
	return map[string]interface{}{"success": false, "error": errMsg}
}

// ClearLogin ??????
func (a *App) ClearLogin() map[string]interface{} {
	a.config.ClearToken()
	a.token = ""
	a.cookies = ""
	return map[string]interface{}{"success": true}
}

// SelectFile ?????????
func (a *App) SelectFile() map[string]string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "?? Excel ??",
		Filters: []runtime.FileFilter{
			{DisplayName: "Excel ??", Pattern: "*.xlsx"},
		},
	})
	if err != nil || path == "" {
		return map[string]string{"path": "", "filename": ""}
	}
	a.inputPath = path
	return map[string]string{
		"path":     path,
		"filename": filepath.Base(path),
	}
}

// StartExtract ?????????????? goroutine ???
func (a *App) StartExtract(startTime, endTime string) map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return map[string]interface{}{"success": false, "error": "???????????"}
	}
	if a.inputPath == "" {
		return map[string]interface{}{"success": false, "error": "???? Excel ??"}
	}
	if !a.config.IsTokenValid() && a.token == "" {
		return map[string]interface{}{"success": false, "error": "????????"}
	}

	a.running = true
	a.aborted = false
	a.results = nil

	go a.doExtract(startTime, endTime)
	return map[string]interface{}{"success": true}
}

// AbortExtract ????
func (a *App) AbortExtract() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return map[string]interface{}{"success": false, "error": "?????????"}
	}
	a.aborted = true
	return map[string]interface{}{"success": true}
}

// ExportResults ????? Excel
func (a *App) ExportResults() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return map[string]interface{}{"success": false, "error": "?????"}
	}
	if len(a.results) == 0 {
		return map[string]interface{}{"success": false, "error": "????????"}
	}
	if a.inputPath == "" {
		return map[string]interface{}{"success": false, "error": "???????"}
	}

	outputDir := filepath.Dir(a.inputPath)
	outputPath, err := WriteOutputExcel(a.inputPath, outputDir, a.results)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{
		"success":  true,
		"path":     outputPath,
		"filename": filepath.Base(outputPath),
	}
}

// isAborted ???????????
func (a *App) isAborted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.aborted
}

// ==================== ???? ====================

func (a *App) emitLog(msg string) {
	runtime.EventsEmit(a.ctx, "log", msg)
}

func (a *App) emitError(msg string) {
	runtime.EventsEmit(a.ctx, "error", msg)
}

func (a *App) emitAborted() {
	runtime.EventsEmit(a.ctx, "aborted")
}

func (a *App) emitExtractStart(total int) {
	runtime.EventsEmit(a.ctx, "extract-start", total)
}

func (a *App) emitExtractComplete(matched, total int) {
	runtime.EventsEmit(a.ctx, "extract-complete", matched, total)
}

func (a *App) emitBatchProgress(page, totalPages, count, total int) {
	runtime.EventsEmit(a.ctx, "batch-progress", page, totalPages, count, total)
}

func (a *App) emitItemUpdate(item ResultItem) {
	runtime.EventsEmit(a.ctx, "item-update", item)
}

// ==================== ?????? ====================

func (a *App) doExtract(startTime, endTime string) {
	defer func() {
		a.mu.Lock()
		a.running = false
		a.aborted = false
		a.mu.Unlock()
	}()

	// 1. ?? Excel
	a.emitLog("???? Excel...")
	customers, emptyRows, err := ReadInputExcel(a.inputPath)
	if err != nil {
		a.emitError(fmt.Sprintf("?? Excel ??: %v", err))
		return
	}
	if len(emptyRows) > 0 {
		rowStrs := make([]string, len(emptyRows))
		for i, r := range emptyRows {
			rowStrs[i] = strconv.Itoa(r)
		}
		a.emitError(fmt.Sprintf("? %s ???ID?????????", strings.Join(rowStrs, "?")))
		return
	}
	if len(customers) == 0 {
		a.emitError("Excel ???????")
		return
	}

	total := len(customers)
	a.emitLog(fmt.Sprintf("? %d ??????", total))
	a.emitExtractStart(total)

	// ?? token
	token := a.token
	cookieStr := a.cookies
	if token == "" {
		tokenData := a.config.LoadToken()
		if tokenData != nil {
			token, _ = tokenData["access_token"].(string)
			cookieStr, _ = tokenData["cookies"].(string)
		}
	}

	// 2. ????
	a.emitLog("??????????...")
	allRecords, remarkIndex, _ := getAllRecordsByTimeRange(
		a.baseURL, token, cookieStr, startTime, endTime, a.isAborted,
		func(page, totalPages, count, total int) {
			if a.isAborted() {
				return
			}
			a.emitBatchProgress(page, totalPages, count, total)
			a.emitLog(fmt.Sprintf("? %d/%d ? | ??? %d/%d ?", page, totalPages, count, total))
		},
	)
	if a.isAborted() {
		a.emitAborted()
		a.emitLog("?????")
		return
	}
	a.emitLog(fmt.Sprintf("???????? %d ???", len(allRecords)))

	// 3. ????
	for i, c := range customers {
		if a.isAborted() {
			break
		}

		itemID := fmt.Sprintf("item_%d", i)
		item := ResultItem{
			ID:           itemID,
			Idx:          i + 1,
			ShangjiID:    c.ShangjiID,
			CustomerName: c.CustomerName,
			Status:       "???",
			Intervention: "-",
			RowNumber:    c.RowNumber,
		}
		a.emitItemUpdate(item)
		a.emitLog(fmt.Sprintf("[%d] %s - ????", i+1, c.CustomerName))

		record := findRecordByRemark(c.ShangjiID, remarkIndex)
		if record == nil {
			item.Status = "???"
			item.Intervention = "???"
			item.InterventionSituation = "?????"
			item.Interruption = "?"
			a.emitItemUpdate(item)
			a.mu.Lock()
			a.results = append(a.results, item)
			a.mu.Unlock()
			continue
		}

		weUserID := extractString(record, "wxUserId")
		externalUserID := extractString(record, "externalUserId")
		submitTime := extractString(record, "createTime")
		bizType := extractString(record, "classification")
		item.SubmitTime = submitTime
		item.BizType = bizType

		// ?? adminSendFlag
		adminSendFlag := 0
		switch v := record["adminSendFlag"].(type) {
		case float64:
			adminSendFlag = int(v)
		case int:
			adminSendFlag = v
		case string:
			adminSendFlag, _ = strconv.Atoi(v)
		case bool:
			if v {
				adminSendFlag = 1
			}
		}

		chatText := ""

		if adminSendFlag == 0 {
			item.Status = "??"
			item.Intervention = "???"
			item.InterventionSituation = "???????"
			item.Interruption = "?"
			a.emitLog(fmt.Sprintf("[%d] adminSendFlag=0 ? ???", i+1))
		} else {
			// ??????
			messages, err := getChatRecords(a.baseURL, token, cookieStr, weUserID, externalUserID, startTime, endTime, a.isAborted)
			if err != nil {
				if err.Error() == "aborted" {
					break
				}
				item.Status = "????"
				item.Intervention = "????"
				a.emitItemUpdate(item)
				a.mu.Lock()
				a.results = append(a.results, item)
				a.mu.Unlock()
				continue
			}
			recordDate := ""
			if len(submitTime) >= 10 {
				recordDate = submitTime[:10]
			}
			chatText = extractChatText(messages, c.CustomerName, recordDate)
			item.Status = "??"
			parsedMessages := extractChatMessages(messages)
			var matchedRules []string
			item.Intervention, item.Interruption, item.Scenario, matchedRules, item.Roles, item.InterventionSituation = analyzeConversationWithAI(context.Background(), parsedMessages, bizType, adminSendFlag, a.ai)
			item.MatchedRules = strings.Join(matchedRules, "?")
			item.ChatText = chatText
			a.emitLog(fmt.Sprintf("[%d] ????: %s?????: %s???: %s???: %s???: %s", i+1, item.Intervention, item.InterventionSituation, item.Interruption, item.Scenario, item.MatchedRules))
		}

		a.emitItemUpdate(item)
		a.mu.Lock()
		a.results = append(a.results, item)
		a.mu.Unlock()
	}

	// ??
	a.mu.Lock()
	if a.aborted {
		a.mu.Unlock()
		a.emitAborted()
		a.emitLog("?????")
	} else {
		matched := 0
		for _, r := range a.results {
			if r.Intervention != "???" && r.Intervention != "????" && r.Intervention != "????" && r.Intervention != "??" {
				matched++
			}
		}
		a.mu.Unlock()
		a.emitExtractComplete(matched, total)
		a.emitLog(fmt.Sprintf("????: %d/%d ??", matched, total))
	}
}

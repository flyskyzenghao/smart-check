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

// ResultItem 提取结果
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

// App 应用结构体，持有全局状态，所有 Wails 绑定方法都挂在这里
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

// NewApp 创建应用实例
func NewApp() *App {
	return &App{
		config: nil, // startup 中初始化
	}
}

// startup Wails 生命周期：应用启动
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 使用 exe 所在目录作为配置目录
	exeDir := "."
	env := runtime.Environment(ctx)
	if env.BuildType == "production" {
		if exePath, err := os.Executable(); err == nil {
			exeDir = filepath.Dir(exePath)
		}
	}
	a.config = NewConfigManager(exeDir)

	// 从 config 加载 base URL
	cfg := a.config.LoadConfig()
	if serverURL, ok := cfg["server_url"].(string); ok && serverURL != "" {
		a.baseURL = parseBaseURL(serverURL)
	} else {
		a.baseURL = defaultBaseURL
	}
	a.ai = newAIClassifier(a.config.LoadAIConfig())
	fmt.Printf("[App] 启动完成，baseURL=%s，configDir=%s\n", a.baseURL, exeDir)
}

// ==================== Wails 绑定方法 ====================

// GetConfig 获取当前配置（前端调用）
func (a *App) GetConfig() map[string]interface{} {
	cfg := a.config.LoadConfig()
	return map[string]interface{}{
		"username":     cfg["username"],
		"login_url":    cfg["login_url"],
		"is_logged_in": a.config.IsTokenValid(),
		"ai":           a.config.PublicAIConfig(),
	}
}

// GetAIConfig 获取 AI 配置摘要，不返回 API Key 明文。
func (a *App) GetAIConfig() map[string]interface{} {
	return a.config.PublicAIConfig()
}

// SaveAIConfig 保存并立即应用 AI 配置。
func (a *App) SaveAIConfig(enabled bool, endpoint, model, apiKey string, timeoutSeconds int) map[string]interface{} {
	current := a.config.LoadAIConfig()
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

// TestAIConfig 对当前配置发送一个最小语义测试请求。
func (a *App) TestAIConfig() map[string]interface{} {
	if a.ai == nil || !a.ai.configured() {
		return map[string]interface{}{"success": false, "error": "AI 未启用或未配置 API Key"}
	}
	judgement := a.ai.classify(context.Background(), aiModeOtherPromotion, "配置测试", "请问目前有什么套餐或优惠？")
	return map[string]interface{}{"success": true, "positive": judgement.Positive, "confidence": judgement.Confidence, "reason": judgement.Reason}
}

// RefreshCaptcha 刷新验证码
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
		errMsg = "验证码获取失败"
	}
	return map[string]string{"error": errMsg}
}

// Login 登录
func (a *App) Login(username, password, captchaCode, captchaUUID string) map[string]interface{} {
	// 保存用户名到配置
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
		errMsg = "登录失败"
	}
	return map[string]interface{}{"success": false, "error": errMsg}
}

// ClearLogin 清除登录状态
func (a *App) ClearLogin() map[string]interface{} {
	a.config.ClearToken()
	a.token = ""
	a.cookies = ""
	return map[string]interface{}{"success": true}
}

// SelectFile 弹出文件选择对话框
func (a *App) SelectFile() map[string]string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择 Excel 文件",
		Filters: []runtime.FileFilter{
			{DisplayName: "Excel 文件", Pattern: "*.xlsx"},
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

// StartExtract 启动后台提取（立即返回，后台 goroutine 执行）
func (a *App) StartExtract(startTime, endTime string) map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return map[string]interface{}{"success": false, "error": "正在提取中，请等待完成"}
	}
	if a.inputPath == "" {
		return map[string]interface{}{"success": false, "error": "请先选择 Excel 文件"}
	}
	if !a.config.IsTokenValid() && a.token == "" {
		return map[string]interface{}{"success": false, "error": "未登录，请先登录"}
	}

	a.running = true
	a.aborted = false
	a.results = nil

	go a.doExtract(startTime, endTime)
	return map[string]interface{}{"success": true}
}

// AbortExtract 中止提取
func (a *App) AbortExtract() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return map[string]interface{}{"success": false, "error": "当前无运行中的任务"}
	}
	a.aborted = true
	return map[string]interface{}{"success": true}
}

// ExportResults 导出结果到 Excel
func (a *App) ExportResults() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.running {
		return map[string]interface{}{"success": false, "error": "正在提取中"}
	}
	if len(a.results) == 0 {
		return map[string]interface{}{"success": false, "error": "没有可导出的结果"}
	}
	if a.inputPath == "" {
		return map[string]interface{}{"success": false, "error": "未选择输入文件"}
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

// isAborted 线程安全地检查中止标志
func (a *App) isAborted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.aborted
}

// ==================== 事件推送 ====================

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

// ==================== 后台提取逻辑 ====================

func (a *App) doExtract(startTime, endTime string) {
	defer func() {
		a.mu.Lock()
		a.running = false
		a.aborted = false
		a.mu.Unlock()
	}()

	// 1. 读取 Excel
	a.emitLog("正在读取 Excel...")
	customers, emptyRows, err := ReadInputExcel(a.inputPath)
	if err != nil {
		a.emitError(fmt.Sprintf("读取 Excel 失败: %v", err))
		return
	}
	if len(emptyRows) > 0 {
		rowStrs := make([]string, len(emptyRows))
		for i, r := range emptyRows {
			rowStrs[i] = strconv.Itoa(r)
		}
		a.emitError(fmt.Sprintf("第 %s 行商机ID为空，请补充后再试", strings.Join(rowStrs, "、")))
		return
	}
	if len(customers) == 0 {
		a.emitError("Excel 中没有商机数据")
		return
	}

	total := len(customers)
	a.emitLog(fmt.Sprintf("共 %d 条商机待处理", total))
	a.emitExtractStart(total)

	// 获取 token
	token := a.token
	cookieStr := a.cookies
	if token == "" {
		tokenData := a.config.LoadToken()
		if tokenData != nil {
			token, _ = tokenData["access_token"].(string)
			cookieStr, _ = tokenData["cookies"].(string)
		}
	}

	// 2. 批量查询
	a.emitLog("开始批量查询会话小结...")
	allRecords, remarkIndex, _ := getAllRecordsByTimeRange(
		a.baseURL, token, cookieStr, startTime, endTime, a.isAborted,
		func(page, totalPages, count, total int) {
			if a.isAborted() {
				return
			}
			a.emitBatchProgress(page, totalPages, count, total)
			a.emitLog(fmt.Sprintf("第 %d/%d 批 | 已获取 %d/%d 条", page, totalPages, count, total))
		},
	)
	if a.isAborted() {
		a.emitAborted()
		a.emitLog("提取已中止")
		return
	}
	a.emitLog(fmt.Sprintf("批量查询完成，共 %d 条记录", len(allRecords)))

	// 3. 逐条匹配
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
			Status:       "查询中",
			Intervention: "-",
			RowNumber:    c.RowNumber,
		}
		a.emitItemUpdate(item)
		a.emitLog(fmt.Sprintf("[%d] %s - 开始匹配", i+1, c.CustomerName))

		record := findRecordByRemark(c.ShangjiID, remarkIndex)
		if record == nil {
			item.Status = "未匹配"
			item.Intervention = "无记录"
			item.InterventionSituation = "无相关会话"
			item.Interruption = "否"
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

		// 解析 adminSendFlag
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
			item.Status = "成功"
			item.Intervention = "未介入"
			item.InterventionSituation = "无关键信息介入"
			item.Interruption = "否"
			a.emitLog(fmt.Sprintf("[%d] adminSendFlag=0 → 未介入", i+1))
		} else {
			// 获取聊天记录
			messages, err := getChatRecords(a.baseURL, token, cookieStr, weUserID, externalUserID, startTime, endTime, a.isAborted)
			if err != nil {
				if err.Error() == "aborted" {
					break
				}
				item.Status = "聊天失败"
				item.Intervention = "获取失败"
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
			item.Status = "成功"
			parsedMessages := extractChatMessages(messages)
			var matchedRules []string
			item.Intervention, item.Interruption, item.Scenario, matchedRules, item.Roles, item.InterventionSituation = analyzeConversationWithAI(context.Background(), parsedMessages, bizType, adminSendFlag, a.ai)
			item.MatchedRules = strings.Join(matchedRules, "、")
			item.ChatText = chatText
			a.emitLog(fmt.Sprintf("[%d] 介入分析: %s，介入情况: %s，插话: %s，场景: %s，命中: %s", i+1, item.Intervention, item.InterventionSituation, item.Interruption, item.Scenario, item.MatchedRules))
		}

		a.emitItemUpdate(item)
		a.mu.Lock()
		a.results = append(a.results, item)
		a.mu.Unlock()
	}

	// 完成
	a.mu.Lock()
	if a.aborted {
		a.mu.Unlock()
		a.emitAborted()
		a.emitLog("提取已中止")
	} else {
		matched := 0
		for _, r := range a.results {
			if r.Intervention != "无记录" && r.Intervention != "处理失败" && r.Intervention != "获取失败" && r.Intervention != "错误" {
				matched++
			}
		}
		a.mu.Unlock()
		a.emitExtractComplete(matched, total)
		a.emitLog(fmt.Sprintf("提取完成: %d/%d 匹配", matched, total))
	}
}

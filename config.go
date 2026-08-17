package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	// AppVersion 应用版本号，构建产物和配置文件都引用此值
	AppVersion = "4.2.0"

	// encryptKey Token 加密密钥
	encryptKey = "sc-dfi882dfy"

	tokenValiditySeconds = 12 * 3600
)

// ConfigManager 管理配置和 Token（合并为单文件）
type ConfigManager struct {
	configDir  string
	configPath string
}

// AIConfig 是本地可配置的 OpenAI-compatible 模型连接配置。
// APIKey 只在本地配置文件中加密保存，不会写入导出 Excel 或运行日志。
type AIConfig struct {
	Enabled        bool   `json:"enabled"`
	Endpoint       string `json:"endpoint"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func defaultAIConfig() AIConfig {
	return AIConfig{
		Enabled:        true,
		Endpoint:       "https://api.siliconflow.cn/v1",
		Model:          "Qwen/Qwen2.5-72B-Instruct",
		TimeoutSeconds: 30,
	}
}

// NewConfigManager 创建配置管理器
func NewConfigManager(configDir string) *ConfigManager {
	cm := &ConfigManager{
		configDir:  configDir,
		configPath: filepath.Join(configDir, fmt.Sprintf("SmartCheck-%s.json", AppVersion)),
	}
	cm.migrateOldConfig()
	cm.ensureDefaultConfig()
	return cm
}

// ==================== 加密 / 解密 ====================

func deriveKey(password string) []byte {
	h := sha256.Sum256([]byte(password))
	return h[:]
}

func encrypt(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	block, err := aes.NewCipher(deriveKey(encryptKey))
	if err != nil {
		return plaintext
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return plaintext
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return plaintext
	}
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext)
}

func decrypt(encoded string) string {
	if encoded == "" {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return encoded
	}
	block, err := aes.NewCipher(deriveKey(encryptKey))
	if err != nil {
		return encoded
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return encoded
	}
	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return encoded
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return encoded
	}
	return string(plaintext)
}

// ==================== 文件读写（内部） ====================

// loadRaw 读取合并后的 JSON 文件
func (cm *ConfigManager) loadRaw() map[string]interface{} {
	data, err := os.ReadFile(cm.configPath)
	if err != nil {
		return make(map[string]interface{})
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return make(map[string]interface{})
	}
	return cfg
}

// saveRaw 写入合并后的 JSON 文件
func (cm *ConfigManager) saveRaw(cfg map[string]interface{}) {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(cm.configPath, data, 0644)
}

// ==================== 公开接口（保持与 app.go 兼容） ====================

// LoadConfig 读取配置（不含 token 字段）
func (cm *ConfigManager) LoadConfig() map[string]interface{} {
	raw := cm.loadRaw()
	return map[string]interface{}{
		"username":   raw["username"],
		"login_url":  raw["login_url"],
		"server_url": raw["server_url"],
	}
}

// SaveConfig 保存配置（不影响 token 字段）
func (cm *ConfigManager) SaveConfig(cfg map[string]interface{}) {
	raw := cm.loadRaw()
	raw["username"] = cfg["username"]
	raw["login_url"] = cfg["login_url"]
	raw["server_url"] = cfg["server_url"]
	cm.saveRaw(raw)
}

// LoadAIConfig 读取 AI 配置。环境变量可作为便携部署时的兜底配置。
func (cm *ConfigManager) LoadAIConfig() AIConfig {
	raw := cm.loadRaw()
	cfg := defaultAIConfig()
	if v, ok := raw["ai_enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := raw["ai_endpoint"].(string); ok && v != "" {
		cfg.Endpoint = v
	}
	if v, ok := raw["ai_model"].(string); ok && v != "" {
		cfg.Model = v
	}
	if v, ok := raw["ai_timeout_seconds"].(float64); ok && int(v) > 0 {
		cfg.TimeoutSeconds = int(v)
	}
	if encoded, ok := raw["ai_api_key"].(string); ok {
		cfg.APIKey = decrypt(encoded)
	}
	if env := os.Getenv("SMARTCHECK_AI_ENDPOINT"); env != "" {
		cfg.Endpoint = env
	}
	if env := os.Getenv("SMARTCHECK_AI_MODEL"); env != "" {
		cfg.Model = env
	}
	if env := os.Getenv("SMARTCHECK_AI_API_KEY"); env != "" {
		cfg.APIKey = env
	}
	if os.Getenv("SMARTCHECK_AI_ENABLED") == "1" || os.Getenv("SMARTCHECK_AI_ENABLED") == "true" {
		cfg.Enabled = true
	}
	if cfg.TimeoutSeconds < 5 {
		cfg.TimeoutSeconds = 30
	}
	return cfg
}

// SaveAIConfig 保存 AI 配置，API Key 使用与登录 token 相同的本地 AES-GCM 密钥加密。
func (cm *ConfigManager) SaveAIConfig(cfg AIConfig) {
	raw := cm.loadRaw()
	raw["ai_enabled"] = cfg.Enabled
	raw["ai_endpoint"] = cfg.Endpoint
	raw["ai_model"] = cfg.Model
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	raw["ai_timeout_seconds"] = cfg.TimeoutSeconds
	raw["ai_api_key"] = encrypt(cfg.APIKey)
	cm.saveRaw(raw)
}

// PublicAIConfig 返回可给前端展示的 AI 配置，不返回 API Key 明文。
func (cm *ConfigManager) PublicAIConfig() map[string]interface{} {
	cfg := cm.LoadAIConfig()
	return map[string]interface{}{
		"enabled":         cfg.Enabled,
		"endpoint":        cfg.Endpoint,
		"model":           cfg.Model,
		"timeout_seconds": cfg.TimeoutSeconds,
		"has_api_key":     cfg.APIKey != "",
	}
}

// LoadToken 读取 token（自动解密）
func (cm *ConfigManager) LoadToken() map[string]interface{} {
	raw := cm.loadRaw()
	at, _ := raw["access_token"].(string)
	if at == "" {
		return nil
	}
	ck, _ := raw["cookies"].(string)
	return map[string]interface{}{
		"access_token": decrypt(at),
		"cookies":      decrypt(ck),
		"timestamp":    raw["timestamp"],
	}
}

// SaveToken 保存 token（自动加密）
func (cm *ConfigManager) SaveToken(accessToken, cookies string) {
	raw := cm.loadRaw()
	raw["access_token"] = encrypt(accessToken)
	raw["cookies"] = encrypt(cookies)
	raw["timestamp"] = time.Now().Unix()
	cm.saveRaw(raw)
}

// IsTokenValid 检查 token 是否在有效期内（12小时）
func (cm *ConfigManager) IsTokenValid() bool {
	tokenData := cm.LoadToken()
	if tokenData == nil {
		return false
	}
	at, ok := tokenData["access_token"].(string)
	if !ok || at == "" {
		return false
	}
	ts, ok := tokenData["timestamp"].(float64)
	if !ok {
		return false
	}
	return (float64(time.Now().Unix()) - ts) < float64(tokenValiditySeconds)
}

// ClearToken 清除 token（保留其他配置）
func (cm *ConfigManager) ClearToken() {
	raw := cm.loadRaw()
	delete(raw, "access_token")
	delete(raw, "cookies")
	delete(raw, "timestamp")
	cm.saveRaw(raw)
}

// ==================== 初始化 ====================

// ensureDefaultConfig 首次运行时创建默认配置文件
func (cm *ConfigManager) ensureDefaultConfig() {
	if _, err := os.Stat(cm.configPath); err == nil {
		return
	}
	defaultCfg := map[string]interface{}{
		"username":   "",
		"login_url":  "http://113.125.37.73:8843/scrm/login",
		"server_url": "http://113.125.37.73:8843/scrm/login",
	}
	data, err := json.MarshalIndent(defaultCfg, "", "  ")
	if err != nil {
		fmt.Printf("[WARN] 创建默认配置文件失败: %v\n", err)
		return
	}
	os.WriteFile(cm.configPath, data, 0644)
}

// migrateOldConfig 从旧版 config.json 迁移配置（仅首次）
func (cm *ConfigManager) migrateOldConfig() {
	if _, err := os.Stat(cm.configPath); err == nil {
		return // 新文件已存在，无需迁移
	}
	paths := []string{
		filepath.Join(cm.configDir, fmt.Sprintf("SmartCheck-%s.json", "4.1.0")),
		filepath.Join(cm.configDir, "config.json"),
	}
	for _, oldPath := range paths {
		oldData, err := os.ReadFile(oldPath)
		if err != nil {
			continue
		}
		var oldCfg map[string]interface{}
		if err := json.Unmarshal(oldData, &oldCfg); err != nil {
			continue
		}
		cm.saveRaw(oldCfg)
		fmt.Printf("[Config] 已从 %s 迁移配置到 %s\n", filepath.Base(oldPath), filepath.Base(cm.configPath))
		return
	}
}

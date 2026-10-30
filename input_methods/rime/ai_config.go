package rime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gaboolic/moqi-ime/imecore"
	"github.com/gaboolic/moqi-ime/imecore/aikey"
)

const aiConfigFileName = "ai_config.json"

type aiGenerateRequest struct {
	PreviousCommit string
	Composition    string
	Candidates     []string
	Prompt         string
}

type aiFileConfig struct {
	API     aiAPIConfig        `json:"api"`
	Actions []aiActionFileSpec `json:"actions"`
}

type aiAPIConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type aiActionFileSpec struct {
	Name   string `json:"name"`
	Hotkey string `json:"hotkey"`
	Prompt string `json:"prompt"`
}

type aiRuntimeConfig struct {
	API     aiAPIConfig
	Actions []aiAction
}

type aiAction struct {
	Name    string
	Hotkey  string
	Prompt  string
	KeyCode int
	Ctrl    bool
	Alt     bool
	Shift   bool
}

func loadAIConfig() (*aiRuntimeConfig, error) {
	if err := ensureUserAIConfigCopied(); err != nil {
		return nil, err
	}
	for _, path := range aiConfigSearchPaths() {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read AI config %s: %w", path, err)
		}
		return parseAIConfigJSON(data)
	}
	return envAIConfig(), nil
}

func aiConfigSearchPaths() []string {
	paths := make([]string, 0, 3)
	if path := userAIConfigPath(); path != "" {
		paths = append(paths, path)
	}
	if path := legacyUserAIConfigPath(); path != "" {
		paths = append(paths, path)
	}
	if path := bundledAIConfigPath(); path != "" {
		paths = append(paths, path)
	}
	return paths
}

func userAIConfigPath() string {
	root := moqiAppDataDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, aiConfigFileName)
}

func legacyUserAIConfigPath() string {
	root := moqiAppDataDir()
	if root == "" {
		return ""
	}
	return filepath.Join(root, defaultSchemeSetName, aiConfigFileName)
}

func bundledAIConfigPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exePath), "input_methods", "rime", aiConfigFileName)
}

func ensureUserAIConfigCopied() error {
	return syncAIConfigFromBundled(userAIConfigPath(), bundledAIConfigPath())
}

// syncAIConfigFromBundled 首次运行整份复制；已有用户配置时只做增量合并：
// api 三个键缺失或为空串才补，同名 action 不覆盖，缺的 action 追加。
// 用通用 map 操作而不是结构体回写，用户的未知键（比如内置的选择性加入键
// 「base_url删掉我即可用AI功能」）不会在重写时被静默丢掉。
func syncAIConfigFromBundled(dstPath, srcPath string) error {
	if dstPath == "" || srcPath == "" {
		return nil
	}
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		return copyAIConfigIfMissing(dstPath, srcPath)
	} else if err != nil {
		return fmt.Errorf("stat AI config %s: %w", dstPath, err)
	}

	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read bundled AI config %s: %w", srcPath, err)
	}
	dstData, err := os.ReadFile(dstPath)
	if err != nil {
		return fmt.Errorf("read user AI config %s: %w", dstPath, err)
	}
	srcRoot, ok := decodeJSONObject(srcData)
	if !ok {
		return nil // 内置配置坏了不该阻塞输入法启动
	}
	dstRoot, ok := decodeJSONObject(dstData)
	if !ok {
		return nil // 用户手改出了语法错，交给后续加载流程报错，不在这里重写文件
	}

	apiMerged, apiChanged := mergeAIAPI(srcRoot["api"], dstRoot["api"])
	actionsMerged, actionsChanged := mergeAIActions(srcRoot["actions"], dstRoot["actions"])
	if !apiChanged && !actionsChanged {
		return nil
	}
	if apiChanged {
		dstRoot["api"] = apiMerged
	}
	if actionsChanged {
		dstRoot["actions"] = actionsMerged
	}

	out, err := json.MarshalIndent(dstRoot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode AI config: %w", err)
	}
	if err := os.WriteFile(dstPath, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write AI config %s: %w", dstPath, err)
	}
	return nil
}

var aiAPIFillKeys = []string{"base_url", "api_key", "model"}

// mergeAIAPI 只补用户侧缺失/空串的 api 键，其余键（含未知键）原样保留。
func mergeAIAPI(bundledRaw, userRaw json.RawMessage) (json.RawMessage, bool) {
	bundled, ok := decodeJSONObject(bundledRaw)
	if !ok {
		return nil, false
	}
	user, ok := decodeJSONObject(userRaw)
	if !ok {
		return nil, false
	}
	changed := false
	for _, key := range aiAPIFillKeys {
		srcVal, exists := bundled[key]
		if !exists || isBlankJSONString(srcVal) {
			continue
		}
		if curVal, exists := user[key]; exists && !shouldFillAPIValue(key, curVal, srcVal) {
			continue
		}
		user[key] = srcVal
		changed = true
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(user)
	if err != nil {
		return nil, false
	}
	return out, true
}

// mergeAIActions 按 name 追加用户缺的 action，同名的绝不覆盖。
func mergeAIActions(bundledRaw, userRaw json.RawMessage) (json.RawMessage, bool) {
	var bundled []aiActionFileSpec
	if len(bundledRaw) == 0 || json.Unmarshal(bundledRaw, &bundled) != nil {
		return nil, false
	}
	var user []json.RawMessage
	if len(userRaw) > 0 {
		_ = json.Unmarshal(userRaw, &user)
	}
	seen := make(map[string]bool, len(user))
	for _, raw := range user {
		var probe struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &probe)
		seen[probe.Name] = true
	}
	changed := false
	for _, action := range bundled {
		if seen[action.Name] {
			continue
		}
		encoded, err := json.Marshal(action)
		if err != nil {
			continue
		}
		user = append(user, encoded)
		seen[action.Name] = true
		changed = true
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(user)
	if err != nil {
		return nil, false
	}
	return out, true
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, bool) {
	obj := map[string]json.RawMessage{}
	if len(data) == 0 {
		return obj, true
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, false
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	return obj, true
}

func isBlankJSONString(raw json.RawMessage) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false // 非字符串（手改坏了）不当作空，避免被覆盖
	}
	return strings.TrimSpace(s) == ""
}

// shouldFillAPIValue 判断用户侧这个 api 键要不要被随包的当前值替换：
//   - 空串 → 补；
//   - 旧版内置配置的「删除我…」占位符 → 换成真 key；
//   - 混淆值 → 是随包分发的 key，换 key 时要跟着换（用户不可能手填这种格式）；
//     已经和随包值一样就不动，免得每次启动重写配置文件；
//   - 其余（用户自己填的明文 key，任何格式）→ 永远不覆盖。
func shouldFillAPIValue(key string, cur, bundled json.RawMessage) bool {
	if isBlankJSONString(cur) {
		return true
	}
	if key != "api_key" {
		return false
	}
	var curStr string
	if json.Unmarshal(cur, &curStr) != nil {
		return false // 手改坏了（非字符串），不猜
	}
	if strings.HasPrefix(strings.TrimSpace(curStr), "删除我") {
		return true
	}
	if !aikey.IsMasked(curStr) {
		return false
	}
	var bundledStr string
	return json.Unmarshal(bundled, &bundledStr) != nil || curStr != bundledStr
}

func copyAIConfigIfMissing(dstPath, srcPath string) error {
	if dstPath == "" || srcPath == "" {
		return nil
	}
	if _, err := os.Stat(dstPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat AI config %s: %w", dstPath, err)
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open bundled AI config %s: %w", srcPath, err)
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("create AI config directory %s: %w", filepath.Dir(dstPath), err)
	}

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("create user AI config %s: %w", dstPath, err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy AI config to %s: %w", dstPath, err)
	}
	return nil
}

func envAIConfig() *aiRuntimeConfig {
	api := envAIAPIConfig()
	if api.BaseURL == "" || api.APIKey == "" || api.Model == "" {
		return nil
	}
	return &aiRuntimeConfig{
		API:     api,
		Actions: []aiAction{defaultAIAction()},
	}
}

func envAIAPIConfig() aiAPIConfig {
	return aiAPIConfig{
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("MOQI_AI_BASE_URL")), "/"),
		APIKey:  strings.TrimSpace(os.Getenv("MOQI_AI_API_KEY")),
		Model:   strings.TrimSpace(os.Getenv("MOQI_AI_MODEL")),
	}
}

func fillAIAPIConfigFromEnv(api aiAPIConfig) aiAPIConfig {
	envAPI := envAIAPIConfig()
	if api.BaseURL == "" {
		api.BaseURL = envAPI.BaseURL
	}
	if api.APIKey == "" {
		api.APIKey = envAPI.APIKey
	}
	if api.Model == "" {
		api.Model = envAPI.Model
	}
	return api
}

func parseAIConfigJSON(data []byte) (*aiRuntimeConfig, error) {
	var raw aiFileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode AI config JSON: %w", err)
	}

	cfg := &aiRuntimeConfig{
		API: aiAPIConfig{
			BaseURL: strings.TrimRight(strings.TrimSpace(raw.API.BaseURL), "/"),
			APIKey:  strings.TrimSpace(raw.API.APIKey),
			Model:   strings.TrimSpace(raw.API.Model),
		},
	}
	cfg.API.APIKey = aikey.Resolve(cfg.API.APIKey)
	cfg.API = fillAIAPIConfigFromEnv(cfg.API)
	if cfg.API.BaseURL == "" {
		// 隐私默认：内置配置把键名写成「base_url删掉我即可用AI功能」，JSON 解析后
		// base_url 为空 → AI 关闭、绝不联网。给用户一条能照着做的提示而不是参数报错。
		if optIn := aiOptInKeyName(data); optIn != "" {
			return nil, &aiOptInDisabledError{optInKey: optIn}
		}
	}
	if cfg.API.BaseURL == "" || cfg.API.APIKey == "" || cfg.API.Model == "" {
		return nil, fmt.Errorf("AI config requires api.base_url, api.api_key and api.model")
	}

	if len(raw.Actions) == 0 {
		cfg.Actions = []aiAction{defaultAIAction()}
		return cfg, nil
	}

	cfg.Actions = make([]aiAction, 0, len(raw.Actions))
	for i, spec := range raw.Actions {
		action, err := newAIAction(spec)
		if err != nil {
			return nil, fmt.Errorf("parse AI action %d: %w", i, err)
		}
		cfg.Actions = append(cfg.Actions, action)
	}
	return cfg, nil
}

func newAIAction(spec aiActionFileSpec) (aiAction, error) {
	hotkey := strings.TrimSpace(spec.Hotkey)
	if hotkey == "" {
		hotkey = defaultAIAction().Hotkey
	}
	action, err := parseAIHotkey(hotkey)
	if err != nil {
		return aiAction{}, err
	}
	action.Name = strings.TrimSpace(spec.Name)
	if action.Name == "" {
		action.Name = "AI"
	}
	action.Prompt = strings.TrimSpace(spec.Prompt)
	if action.Prompt == "" {
		action.Prompt = defaultAIUserPromptTemplate()
	}
	return action, nil
}

func defaultAIAction() aiAction {
	action, _ := parseAIHotkey(fmt.Sprintf("Ctrl+Shift+%s", string(rune(aiHotkeyKeyCode))))
	action.Name = "写好评"
	action.Prompt = defaultAIUserPromptTemplate()
	return action
}

func defaultAIActions(cfg *aiRuntimeConfig) []aiAction {
	if cfg == nil || len(cfg.Actions) == 0 {
		return nil
	}
	actions := make([]aiAction, len(cfg.Actions))
	copy(actions, cfg.Actions)
	return actions
}

func defaultAIUserPromptTemplate() string {
	return "请围绕“{{composition}}”生成最多 3 条适合直接发布的中文好评，每条 20 字左右。"
}

// aiOptInKeyName 找出 api 对象里「base_url 开头但不是 base_url」的键名（内置配置的
// 选择性加入开关），没有则返回空串。
// aiOptInDisabledError 表示"用户还没做选择性加入"，不是配置坏了：
// 调用方应把它当日志噪音级别处理，不要每次都打 error。
type aiOptInDisabledError struct {
	optInKey string
}

func (e *aiOptInDisabledError) Error() string {
	return fmt.Sprintf("AI 功能未启用：打开 ai_config.json，把键名「%s」删成「base_url」即可开启", e.optInKey)
}

func isAIOptInDisabled(err error) bool {
	var target *aiOptInDisabledError
	return errors.As(err, &target)
}

// aiOptInKeyName 找出 api 对象里「base_url 开头但不是 base_url」的键名（内置配置的
// 选择性加入开关），没有则返回空串。多个候选时取字典序最小者以保证稳定。
func aiOptInKeyName(data []byte) string {
	root, ok := decodeJSONObject(data)
	if !ok {
		return ""
	}
	api, ok := decodeJSONObject(root["api"])
	if !ok {
		return ""
	}
	name := ""
	for key := range api {
		if key == "base_url" || !strings.HasPrefix(key, "base_url") {
			continue
		}
		if name == "" || key < name {
			name = key
		}
	}
	return name
}

func parseAIHotkey(hotkey string) (aiAction, error) {
	parts := strings.Split(hotkey, "+")
	action := aiAction{
		Hotkey: strings.TrimSpace(hotkey),
	}
	if len(parts) == 0 {
		return aiAction{}, fmt.Errorf("empty hotkey")
	}

	var mainKey string
	for _, part := range parts {
		token := strings.ToUpper(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		switch token {
		case "CTRL", "CONTROL":
			action.Ctrl = true
		case "ALT", "MENU":
			action.Alt = true
		case "SHIFT":
			action.Shift = true
		default:
			if mainKey != "" {
				return aiAction{}, fmt.Errorf("hotkey %q has multiple main keys", hotkey)
			}
			mainKey = token
		}
	}
	if mainKey == "" {
		return aiAction{}, fmt.Errorf("hotkey %q is missing main key", hotkey)
	}

	keyCode, err := parseAIHotkeyMainKey(mainKey)
	if err != nil {
		return aiAction{}, err
	}
	action.KeyCode = keyCode
	action.Hotkey = normalizedAIHotkey(action)
	return action, nil
}

func parseAIHotkeyMainKey(token string) (int, error) {
	if len(token) == 1 {
		ch := token[0]
		if ch >= 'A' && ch <= 'Z' {
			return int(ch), nil
		}
		if ch >= '0' && ch <= '9' {
			return int(ch), nil
		}
	}
	return 0, fmt.Errorf("unsupported AI hotkey key %q", token)
}

func normalizedAIHotkey(action aiAction) string {
	parts := make([]string, 0, 4)
	if action.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if action.Alt {
		parts = append(parts, "Alt")
	}
	if action.Shift {
		parts = append(parts, "Shift")
	}
	parts = append(parts, strings.ToUpper(string(rune(action.KeyCode))))
	return strings.Join(parts, "+")
}

func (action aiAction) matches(req *imecore.Request) bool {
	if req == nil || req.KeyCode != action.KeyCode {
		return false
	}
	return req.KeyStates.IsKeyDown(vkControl) == action.Ctrl &&
		req.KeyStates.IsKeyDown(vkMenu) == action.Alt &&
		req.KeyStates.IsKeyDown(vkShift) == action.Shift
}

// logAIConfigError 把"用户尚未选择性加入"降为 debug 噪音：
// 这是设计上的默认状态，不该每次启动都往 error 日志里写一条失败。
func logAIConfigError(prefix string, err error) {
	if isAIOptInDisabled(err) {
		debugLogf("%s（AI 当前关闭，等待用户选择性加入）: %v", prefix, err)
		return
	}
	log.Printf("%s: %v", prefix, err)
}

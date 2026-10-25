//go:build windows || android

package rime

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/gaboolic/moqi-ime/imecore"
)

type nativeBackend struct {
	sessionID RimeSessionId
}

var (
	rimeInitOnce sync.Once
	rimeInitOK   bool
	rimeRuntime  nativeRuntimeState
)

type nativeRuntimeState struct {
	mu                  sync.Mutex
	opMu                sync.RWMutex
	redeploying         bool
	pendingNotification *imecore.TrayNotification
}

var rimeRedeployFunc = RimeRedeploy

func (s *nativeRuntimeState) tryBeginOperation() bool {
	s.mu.Lock()
	redeploying := s.redeploying
	s.mu.Unlock()
	if redeploying {
		return false
	}

	s.opMu.RLock()

	s.mu.Lock()
	redeploying = s.redeploying
	s.mu.Unlock()
	if redeploying {
		s.opMu.RUnlock()
		return false
	}
	return true
}

func (s *nativeRuntimeState) endOperation() {
	s.opMu.RUnlock()
}

func (s *nativeRuntimeState) startRedeploy(sharedDir, userDir string) bool {
	s.mu.Lock()
	if s.redeploying {
		s.mu.Unlock()
		return false
	}
	s.redeploying = true
	s.pendingNotification = nil
	s.mu.Unlock()

	go func() {
		s.opMu.Lock()
		success := rimeRedeployFunc(sharedDir, userDir, APP, APP_VERSION)
		s.opMu.Unlock()

		s.mu.Lock()
		s.redeploying = false
		s.pendingNotification = deployTrayNotification(success)
		s.mu.Unlock()
	}()

	return true
}

func (s *nativeRuntimeState) consumeNotification() *imecore.TrayNotification {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingNotification == nil {
		return nil
	}
	notification := s.pendingNotification
	s.pendingNotification = nil
	return notification
}

func (s *nativeRuntimeState) isRedeploying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.redeploying
}

func resetNativeRuntimeStateForTest() {
	rimeRuntime.mu.Lock()
	rimeRuntime.redeploying = false
	rimeRuntime.pendingNotification = nil
	rimeRuntime.mu.Unlock()
}

func newNativeBackend() rimeBackend {
	return &nativeBackend{}
}

func (b *nativeBackend) Initialize(sharedDir, userDir string, firstRun bool) bool {
	initStart := time.Now()
	executedOnce := false
	rimeInitOnce.Do(func() {
		executedOnce = true
		onceStart := time.Now()
		debugLogf("nativeBackend.Initialize 一次性初始化开始 firstRun=%t sharedDir=%q userDir=%q", firstRun, sharedDir, userDir)
		// 日志目录体积兜底清理（正常不会触发，见 cleanupOversizedLogDir）。
		cleanupOversizedLogDir(rimeLogDir())
		// 首次部署前同步 local_behavior 生成的 patch（大语法模型等），
		// 否则全新安装要手动托盘「刷新配置」一次才生效（2026-10-21 缺口）。
		// 用 ensure 变体：初始化竞态下扫描无果时不落盘也绝不删除已有配置。
		ensureFrostCustomPatch(userDir, sharedDir)
		rimeInitOK = RimeInit(sharedDir, userDir, APP, APP_VERSION, firstRun)
		debugLogf("nativeBackend.Initialize 一次性初始化完成 elapsed=%s success=%t", time.Since(onceStart), rimeInitOK)
		if !rimeInitOK {
			log.Println("RIME 初始化失败，原生后端不可用")
		} else {
			// 陌生 gram 模型（哈希不在预设/缓存）：校准罚分（约 5 秒，读候选
			// 不提交不写用户词典）。**必须同步**跑在 Initialize 里：此时管道
			// 尚未开始服务、引擎单线程独占——rime.dll 不允许跨线程并发会话
			// （2026-10-25 S5 崩溃实证：goroutine 校准与主线程预热并发，
			// 两条栈同时崩在 librime 内部）。一次性成本由登录预热吸收。
			// 出厂模型与手填值走部署期烘焙，这里直接跳过（零开销）。
			calibrateGrammarPenaltyIfNeeded(sharedDir, userDir)
		}
	})
	debugLogf("nativeBackend.Initialize 返回 elapsed=%s success=%t executedOnce=%t", time.Since(initStart), rimeInitOK, executedOnce)
	return rimeInitOK
}

func (b *nativeBackend) Redeploy(sharedDir, userDir string) bool {
	b.DestroySession()
	if !rimeRuntime.startRedeploy(sharedDir, userDir) {
		log.Println("RIME 已在重新部署中")
		return true
	}
	return true
}

func (b *nativeBackend) Available() bool {
	return rimeInitOK && !rimeRuntime.isRedeploying()
}

func (b *nativeBackend) ConsumeNotification() *imecore.TrayNotification {
	return rimeRuntime.consumeNotification()
}

func (b *nativeBackend) SyncUserData() bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	b.DestroySession()
	if !SyncUserData() {
		log.Println("RIME 同步用户数据失败")
		return false
	}
	JoinMaintenanceThread()
	return true
}

func (b *nativeBackend) ensureSessionLocked() bool {
	if b.sessionID != 0 && FindSession(b.sessionID) {
		return true
	}
	sessionID, ok := StartSession()
	if ok {
		b.sessionID = sessionID
	}
	return ok
}

func (b *nativeBackend) HasSession() bool {
	if b.sessionID == 0 {
		return false
	}
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	return b.sessionID != 0 && FindSession(b.sessionID)
}

func (b *nativeBackend) EnsureSession() bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	return b.ensureSessionLocked()
}

func (b *nativeBackend) DestroySession() {
	if !rimeRuntime.tryBeginOperation() {
		b.sessionID = 0
		return
	}
	defer rimeRuntime.endOperation()
	if b.sessionID != 0 {
		EndSession(b.sessionID)
		b.sessionID = 0
	}
}

func (b *nativeBackend) ClearComposition() {
	if !rimeRuntime.tryBeginOperation() {
		return
	}
	defer rimeRuntime.endOperation()
	if b.sessionID != 0 {
		ClearComposition(b.sessionID)
	}
}

func (b *nativeBackend) ProcessKey(req *imecore.Request, translatedKeyCode, modifiers int) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return ProcessKey(b.sessionID, translatedKeyCode, modifiers)
}

func (b *nativeBackend) State() rimeState {
	state := rimeState{}
	if !rimeRuntime.tryBeginOperation() {
		return state
	}
	defer rimeRuntime.endOperation()
	if b.sessionID == 0 {
		return state
	}
	if commit, ok := GetCommit(b.sessionID); ok {
		state.CommitString = commit.Text
	}
	if composition, ok := GetComposition(b.sessionID); ok {
		state.Composition = composition.Preedit
		state.CursorPos = composition.CursorPos
		state.SelStart = composition.SelStart
		state.SelEnd = composition.SelEnd
	}
	state.RawInput = GetInput(b.sessionID)
	if menu, ok := GetMenu(b.sessionID); ok {
		candidates := make([]candidateItem, 0, len(menu.Candidates))
		for _, candidate := range menu.Candidates {
			candidates = append(candidates, candidateItem{
				Text:    candidate.Text,
				Comment: candidate.Comment,
			})
		}
		state.Candidates = candidates
		state.PageNo = menu.PageNo
		state.CandidateCursor = menu.HighlightedCandidateIndex
		state.SelectKeys = menu.SelectKeys
	}
	state.AsciiMode = b.GetOption("ascii_mode")
	state.FullShape = b.GetOption("full_shape")
	return state
}

func (b *nativeBackend) SetOption(name string, value bool) {
	if !rimeRuntime.tryBeginOperation() {
		return
	}
	defer rimeRuntime.endOperation()
	if b.ensureSessionLocked() {
		SetOption(b.sessionID, name, value)
	}
}

func (b *nativeBackend) GetOption(name string) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return GetOption(b.sessionID, name)
}

func (b *nativeBackend) SaveOptions() []string {
	if !rimeRuntime.tryBeginOperation() {
		return nil
	}
	defer rimeRuntime.endOperation()

	if b.ensureSessionLocked() {
		if schemaID := GetCurrentSchema(b.sessionID); schemaID != "" {
			if options := GetSchemaConfigStringList(schemaID, "switcher/save_options"); len(options) > 0 {
				return options
			}
		}
	}
	return GetConfigStringList("default", "switcher/save_options")
}

func (b *nativeBackend) SchemaSwitches() []RimeSwitch {
	if !rimeRuntime.tryBeginOperation() {
		return nil
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return nil
	}
	schemaID := GetCurrentSchema(b.sessionID)
	if schemaID == "" {
		return nil
	}
	return GetSchemaSwitches(schemaID)
}

func (b *nativeBackend) SchemaList() []RimeSchema {
	if !rimeRuntime.tryBeginOperation() {
		return nil
	}
	defer rimeRuntime.endOperation()
	return GetSchemaList()
}

func (b *nativeBackend) CurrentSchemaID() string {
	if !rimeRuntime.tryBeginOperation() {
		return ""
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return ""
	}
	return GetCurrentSchema(b.sessionID)
}

func (b *nativeBackend) SelectSchema(schemaID string) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return SelectSchema(b.sessionID, schemaID)
}

func (b *nativeBackend) SetCandidatePageSize(pageSize int) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	schemaID := GetCurrentSchema(b.sessionID)
	if schemaID == "" {
		return false
	}
	return SetSchemaPageSize(schemaID, pageSize)
}

func (b *nativeBackend) SelectCandidate(index int) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return SelectCandidate(b.sessionID, index)
}

func (b *nativeBackend) HighlightCandidate(index int) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return HighlightCandidate(b.sessionID, index)
}

func (b *nativeBackend) ChangePage(backward bool) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return ChangePage(b.sessionID, backward)
}

func (b *nativeBackend) DeleteCandidateOnCurrentPage(index int) bool {
	if !rimeRuntime.tryBeginOperation() {
		return false
	}
	defer rimeRuntime.endOperation()
	if !b.ensureSessionLocked() {
		return false
	}
	return DeleteCandidateOnCurrentPage(b.sessionID, index)
}

// InitWithDirs 用显式目录初始化原生后端（评估器/工具用，绕过 exe 路径推导）。
// 目录布局与正式运行一致：rime.dll 位于 sharedDir 的上级。
func (ime *IME) InitWithDirs(sharedDir, userDir string) bool {
	firstRun := false
	info, err := os.Stat(userDir)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(userDir, 0o700); err != nil {
			return false
		}
		firstRun = true
	} else if err != nil || !info.IsDir() {
		return false
	}
	real := newNativeBackend()
	if real == nil || !real.Initialize(sharedDir, userDir, firstRun) {
		return false
	}
	ime.backend = real
	ime.sharedDirOverride = sharedDir
	return true
}

// SetNativeInput 评测专用快路径：把整串原始输入一次性写入当前会话。
// 逐键 ProcessKey 对每个字母触发一次完整翻译（30 字母 = 30 次），整串注入
// 只翻译一次，整句评测因此快约 48 倍（单帧口径实测）。
// 不用于产品按键路径（绕过按键事件语义）。
func (ime *IME) SetNativeInput(input string) bool {
	if native, ok := ime.backend.(*nativeBackend); ok && native.sessionID != 0 {
		return SetInput(native.sessionID, input)
	}
	return false
}

// DebugSessionID 暴露原生会话（评估器诊断用）。
func (ime *IME) DebugSessionID() RimeSessionId {
	if native, ok := ime.backend.(*nativeBackend); ok {
		return native.sessionID
	}
	return 0
}

package rime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gaboolic/moqi-ime/imecore"
)

const (
	maxSchemeSetArchiveBytes   = 200 << 20
	maxSchemeSetExtractedBytes = 500 << 20
)

type schemeSetDownloadPackage struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
}

type schemeSetHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type schemeSetDownloadState struct {
	mu      sync.Mutex
	running bool
}

var (
	sharedSchemeSetDownloadState schemeSetDownloadState
	schemeSetDownloadHTTPClient  schemeSetHTTPDoer = &http.Client{Timeout: 2 * time.Minute}
	schemeSetDownloadPromptFunc                    = promptSchemeSetDownloadPackage
)

func resetSchemeSetDownloadStateForTest() {
	sharedSchemeSetDownloadState.mu.Lock()
	sharedSchemeSetDownloadState.running = false
	sharedSchemeSetDownloadState.mu.Unlock()
	schemeSetDownloadHTTPClient = &http.Client{Timeout: 2 * time.Minute}
	schemeSetDownloadPromptFunc = promptSchemeSetDownloadPackage
}

func (ime *IME) downloadSchemeSetAsync(req *imecore.Request, resp *imecore.Response) bool {
	if !requireOutbound() {
		if resp != nil {
			resp.TrayNotification = trayNotification(outboundBlockedMessage, imecore.TrayNotificationIconError)
		}
		return false
	}
	sharedSchemeSetDownloadState.mu.Lock()
	if sharedSchemeSetDownloadState.running {
		sharedSchemeSetDownloadState.mu.Unlock()
		if resp != nil {
			resp.TrayNotification = trayNotification("方案集下载已在进行中", imecore.TrayNotificationIconInfo)
		}
		return false
	}
	sharedSchemeSetDownloadState.running = true
	sharedSchemeSetDownloadState.mu.Unlock()

	if resp != nil {
		resp.TrayNotification = trayNotification("打开方案集下载窗口...", imecore.TrayNotificationIconInfo)
	}

	go func() {
		defer func() {
			sharedSchemeSetDownloadState.mu.Lock()
			sharedSchemeSetDownloadState.running = false
			sharedSchemeSetDownloadState.mu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		pkg, err := schemeSetDownloadPromptFunc(ctx)
		if err != nil {
			log.Printf("下载方案集失败: %v", err)
			ime.sendAsyncTrayNotification(trayNotification("下载方案集失败: "+shortErrorMessage(err), imecore.TrayNotificationIconError))
			return
		}
		displayName := strings.TrimSpace(pkg.Name)
		if displayName == "" {
			displayName = inferSchemeSetNameFromURL(pkg.URL)
		}
		debugLogf("开始下载方案集 name=%q url=%q", displayName, pkg.URL)
		ime.sendAsyncTrayNotification(trayNotification("开始下载方案集: "+displayName, imecore.TrayNotificationIconInfo))

		name, err := installSchemeSetPackage(ctx, pkg)
		if err != nil {
			log.Printf("下载方案集失败: %v", err)
			ime.sendAsyncTrayNotification(trayNotification("下载方案集失败: "+shortErrorMessage(err), imecore.TrayNotificationIconError))
			return
		}
		debugLogf("方案集下载完成 name=%q", name)
		ime.sendAsyncTrayNotification(trayNotification("方案集已下载，正在部署: "+name, imecore.TrayNotificationIconInfo))

		ime.mu.Lock()
		activateResp := imecore.NewResponse(0, true)
		ok := ime.activateDownloadedSchemeSetLocked(name, req, activateResp)
		ime.mu.Unlock()
		if !ok {
			ime.sendAsyncTrayNotification(trayNotification("方案集已下载，部署失败", imecore.TrayNotificationIconError))
			return
		}
		ime.sendAsyncTrayNotification(trayNotification("方案集下载安装成功: "+name, imecore.TrayNotificationIconInfo))
	}()

	return true
}

func (ime *IME) activateDownloadedSchemeSetLocked(name string, req *imecore.Request, resp *imecore.Response) bool {
	current := currentSchemeSetName()
	if !saveCurrentSchemeSetName(name) {
		return false
	}
	resp.TrayNotification = schemeSetTrayNotification("方案集切换中...", imecore.TrayNotificationIconInfo)
	if ime.redeploy(req, resp) {
		ime.schemeSetVersion = bumpSchemeSetVersion()
		return true
	}
	_ = saveCurrentSchemeSetName(current)
	resp.TrayNotification = schemeSetTrayNotification("方案集切换失败", imecore.TrayNotificationIconError)
	return false
}

func installSchemeSetPackage(ctx context.Context, pkg schemeSetDownloadPackage) (string, error) {
	if strings.TrimSpace(pkg.Name) == "" {
		pkg.Name = inferSchemeSetNameFromURL(pkg.URL)
	}
	downloadURLs := schemeSetDownloadURLs(pkg.URL)
	name, err := validateDownloadedSchemeSetName(pkg.Name)
	if err != nil {
		return "", err
	}
	if len(downloadURLs) == 0 {
		return "", errors.New("方案集下载地址为空")
	}

	archive, err := downloadFirstAvailableURL(ctx, downloadURLs)
	if err != nil {
		return "", err
	}
	if err := verifySchemeSetArchiveHash(archive, pkg.SHA256); err != nil {
		return "", err
	}

	root := moqiAppDataDir()
	if root == "" {
		return "", errors.New("未找到 APPDATA")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建墨奇用户目录失败: %w", err)
	}

	stagingDir, err := os.MkdirTemp(root, ".scheme-set-download-*")
	if err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	if err := extractSchemeSetZip(archive, stagingDir); err != nil {
		return "", err
	}
	if err := installExtractedSchemeSet(root, name, stagingDir); err != nil {
		return "", err
	}
	return name, nil
}

func downloadURL(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建下载请求失败: %w", err)
	}
	resp, err := schemeSetDownloadHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取下载内容失败: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, errors.New("下载内容过大")
	}
	return data, nil
}

func validateDownloadedSchemeSetName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", errors.New("方案集名称为空")
	}
	normalized := normalizeSchemeSetName(trimmed)
	if normalized != trimmed {
		return "", fmt.Errorf("非法方案集名称: %s", name)
	}
	return normalized, nil
}

func verifySchemeSetArchiveHash(data []byte, expected string) error {
	expected = strings.TrimSpace(strings.ToLower(expected))
	if expected == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("方案集校验失败")
	}
	return nil
}

func downloadFirstAvailableURL(ctx context.Context, rawURLs []string) ([]byte, error) {
	var lastErr error
	for _, rawURL := range rawURLs {
		archive, err := downloadURL(ctx, rawURL, maxSchemeSetArchiveBytes)
		if err == nil {
			return archive, nil
		}
		lastErr = err
		log.Printf("下载方案集地址失败 url=%s err=%v", rawURL, err)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("方案集下载地址为空")
}

func schemeSetDownloadURLs(rawURL string) []string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return []string{rawURL}
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return []string{rawURL}
	}
	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	if owner == "" || repo == "" {
		return []string{rawURL}
	}
	if len(parts) >= 4 && parts[2] == "archive" {
		return []string{rawURL}
	}
	if len(parts) >= 4 && parts[2] == "tree" && parts[3] != "" {
		return []string{githubArchiveURL(owner, repo, parts[3])}
	}
	return []string{
		githubArchiveURL(owner, repo, "main"),
		githubArchiveURL(owner, repo, "master"),
	}
}

func normalizeSchemeSetDownloadURL(rawURL string) string {
	urls := schemeSetDownloadURLs(rawURL)
	if len(urls) == 0 {
		return rawURL
	}
	return urls[0]
}

func githubArchiveURL(owner, repo, branch string) string {
	return fmt.Sprintf("https://github.com/%s/%s/archive/refs/heads/%s.zip", owner, repo, branch)
}

func inferSchemeSetNameFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return defaultSchemeSetName
	}
	base := path.Base(parsed.Path)
	base = strings.TrimSuffix(base, ".zip")
	base = strings.TrimSuffix(base, ".tar")
	base = strings.TrimSuffix(base, ".gz")
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == "/" {
		base = parsed.Hostname()
	}
	base = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		default:
			return r
		}
	}, base)
	base = strings.Trim(base, " .-")
	if base == "" {
		return defaultSchemeSetName
	}
	return base
}

func extractSchemeSetZip(data []byte, dest string) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("打开方案集压缩包失败: %w", err)
	}
	prefix := commonZipTopLevel(reader.File)
	var extracted uint64
	filesExtracted := 0
	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
		}
		cleanName, ok := cleanArchivePath(name)
		if !ok {
			return fmt.Errorf("方案集压缩包包含非法路径: %s", file.Name)
		}
		if cleanName == "" {
			continue
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(cleanName)), 0o755); err != nil {
				return fmt.Errorf("创建目录失败: %w", err)
			}
			continue
		}
		extracted += file.UncompressedSize64
		if extracted > maxSchemeSetExtractedBytes {
			return errors.New("方案集解压后内容过大")
		}
		if err := extractSchemeSetFile(file, dest, cleanName); err != nil {
			return err
		}
		filesExtracted++
	}
	if filesExtracted == 0 {
		return errors.New("方案集压缩包为空")
	}
	return nil
}

func extractSchemeSetFile(file *zip.File, dest, cleanName string) error {
	target := filepath.Join(dest, filepath.FromSlash(cleanName))
	if !isPathInside(dest, target) {
		return fmt.Errorf("方案集压缩包包含非法路径: %s", file.Name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("读取压缩包文件失败: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.FileInfo().Mode().Perm())
	if err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("解压文件失败: %w", err)
	}
	return nil
}

func commonZipTopLevel(files []*zip.File) string {
	var prefix string
	for _, file := range files {
		name := strings.TrimLeft(strings.ReplaceAll(file.Name, "\\", "/"), "/")
		if name == "" {
			continue
		}
		parts := strings.SplitN(name, "/", 2)
		if len(parts) < 2 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
			return ""
		}
		if prefix == "" {
			prefix = parts[0] + "/"
			continue
		}
		if parts[0]+"/" != prefix {
			return ""
		}
	}
	return prefix
}

func cleanArchivePath(name string) (string, bool) {
	name = strings.TrimLeft(name, "/")
	if name == "" {
		return "", true
	}
	cleanName := path.Clean(name)
	if cleanName == "." {
		return "", true
	}
	if cleanName == ".." || strings.HasPrefix(cleanName, "../") || path.IsAbs(cleanName) {
		return "", false
	}
	return cleanName, true
}

func installExtractedSchemeSet(root, name, stagingDir string) error {
	targetDir := filepath.Join(root, name)
	if !isPathInside(root, targetDir) {
		return fmt.Errorf("非法方案集安装目录: %s", name)
	}

	backupDir := ""
	if _, err := os.Stat(targetDir); err == nil {
		backupDir = filepath.Join(root, fmt.Sprintf(".%s.backup-%d", name, time.Now().UnixNano()))
		if err := os.Rename(targetDir, backupDir); err != nil {
			return fmt.Errorf("备份已有方案集失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查已有方案集失败: %w", err)
	}

	if err := os.Rename(stagingDir, targetDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, targetDir)
		}
		return fmt.Errorf("安装方案集失败: %w", err)
	}
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func isPathInside(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
}

func shortErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	runes := []rune(msg)
	if len(runes) <= 48 {
		return msg
	}
	return string(runes[:48]) + "..."
}

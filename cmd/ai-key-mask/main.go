// ai-key-mask 生成内置 AI key 的混淆值、写回 ai_config.json，并校验文件里的值
// 能否还原。输出全部是英文：中文在 GBK 控制台会变乱码。
//
//	go build -o ai-key-mask.exe ./cmd/ai-key-mask
//
// 用法：
//
//	ai-key-mask.exe <key>                       打印混淆值
//	ai-key-mask.exe <key> -f ai_config.json     写入 api.api_key（换 key 用这个）
//	ai-key-mask.exe -check -f ai_config.json    校验文件里的值能否还原
//	ai-key-mask.exe -check <value>              校验命令行给的值
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gaboolic/moqi-ime/imecore/aikey"
)

const optInKeyName = "base_url"

func main() {
	// 手写解析：flag 包在第一个位置参数处停止，`<key> -f file` 这种自然写法会失效。
	var file string
	var check bool
	var positional []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--file":
			i++
			if i >= len(args) {
				fail("-f needs a path")
			}
			file = args[i]
		case "-check", "--check":
			check = true
		case "-h", "--help":
			usage()
			return
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 && file == "" {
		usage()
		os.Exit(2)
	}

	if check {
		if len(positional) > 1 {
			fail("-check takes at most one value")
		}
		value := ""
		if len(positional) == 1 {
			value = positional[0]
		}
		os.Exit(runCheck(file, value))
	}
	if len(positional) == 0 {
		fail("missing key: pass the plaintext key, or use -check")
	}
	if len(positional) > 1 {
		fail("pass exactly one key")
	}
	os.Exit(runMask(positional[0], file))
}

func usage() {
	out := os.Stderr
	fmt.Fprintln(out, "ai-key-mask - build or verify the masked key in ai_config.json")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  ai-key-mask.exe <key>                      print the masked value")
	fmt.Fprintln(out, "  ai-key-mask.exe <key> -f ai_config.json    write it into api.api_key")
	fmt.Fprintln(out, "  ai-key-mask.exe -check -f ai_config.json   verify the value in that file")
	fmt.Fprintln(out, "  ai-key-mask.exe -check <value>             verify a value given here")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Values without the "+aikey.Prefix+" prefix are treated as the user's own plaintext key.")
}

func runMask(key, file string) int {
	key = strings.TrimSpace(key)
	if key == "" {
		fail("empty key")
	}
	if aikey.IsMasked(key) {
		fail("input already looks like a masked value; pass the plaintext key")
	}
	masked := aikey.Mask(key)
	if got := aikey.Resolve(masked); got != key {
		fail("internal check failed: masked value does not resolve back to the input")
	}
	fmt.Printf("plaintext : len=%d sha256=%s\n", len(key), fingerprint(key))
	fmt.Printf("masked    : %s\n", masked)
	fmt.Println("self-check: ok (resolves back to the same key)")

	if file == "" {
		return 0
	}
	if err := writeKey(file, masked); err != nil {
		fail("%v", err)
	}
	fmt.Printf("written   : %s (api.api_key)\n", file)
	fmt.Println("note      : restart the backend (or re-deploy) to reload the key")
	return 0
}

func runCheck(file, value string) int {
	if file != "" && value != "" {
		fail("pass either -f ai_config.json or a value, not both")
	}
	source := "command line"
	if file != "" {
		got, err := readKey(file)
		if err != nil {
			fail("%v", err)
		}
		value = got
		source = file
	}
	if value == "" {
		fail("nothing to check: pass -f ai_config.json or a value")
	}

	resolved := aikey.Resolve(value)
	fmt.Printf("source    : %s\n", source)
	broken := false
	switch {
	case aikey.IsMasked(value):
		fmt.Printf("stored    : masked (%s..., len %d)\n", head(value, 12), len(value))
	case strings.HasPrefix(value, aikey.Prefix):
		// 有前缀却还原不出来=混淆值被改坏了，按原样发出去只会 401，必须报出来。
		broken = true
		fmt.Println("stored    : BROKEN masked value (prefix ok, base64/checksum bad)")
	default:
		fmt.Println("stored    : plaintext (no prefix) - the user's own key")
	}
	fmt.Printf("resolved  : len=%d sha256=%s\n", len(resolved), fingerprint(resolved))
	if broken {
		fmt.Println("verdict   : FAIL - rewrite it with: ai-key-mask.exe <key> -f <file>")
		return 1
	}
	if !utf8.ValidString(resolved) {
		fmt.Println("verdict   : WARN - resolved value is not valid UTF-8, check the source key")
		return 1
	}
	fmt.Printf("verdict   : ok (%s)\n", describe(resolved))
	return 0
}

// readKey 取 ai.api_key 的原始值，顺带提示 AI 是否还被选择性加入的键名关着。
func readKey(path string) (string, error) {
	_, api, err := loadObjects(path)
	if err != nil {
		return "", err
	}
	var key string
	if raw, ok := api["api_key"]; ok {
		if err := json.Unmarshal(raw, &key); err != nil {
			return "", fmt.Errorf("%s: api.api_key is not a string: %w", path, err)
		}
	}
	if key == "" {
		return "", fmt.Errorf("%s: api.api_key is missing or empty", path)
	}
	if name := optInKeyNameIn(api); name != "" {
		fmt.Printf("state     : AI disabled (api key name is %q, rename it to %q to enable)\n", name, optInKeyName)
	}
	return key, nil
}

func writeKey(path, masked string) error {
	root, api, err := loadObjects(path)
	if err != nil {
		return fmt.Errorf("%w (refusing to rewrite)", err)
	}
	encoded, err := json.Marshal(masked)
	if err != nil {
		return err
	}
	api["api_key"] = encoded
	if apiRaw, err := json.Marshal(api); err != nil {
		return err
	} else {
		root["api"] = apiRaw
	}
	out, err := marshalOrdered(root, []string{"api", "actions"})
	if err != nil {
		return err
	}
	return replaceFile(path, out)
}

// loadObjects 读出顶层对象与它的 api 对象；读和写共用，免得两处各解析一遍。
func loadObjects(path string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	api := map[string]json.RawMessage{}
	if raw, ok := root["api"]; ok {
		if err := json.Unmarshal(raw, &api); err != nil {
			return nil, nil, fmt.Errorf("%s: api is not an object: %w", path, err)
		}
	}
	return root, api, nil
}

// replaceFile 先写同目录临时文件再改名：中途失败不会留下半个 JSON——产品启动时
// 读到坏配置就等于 AI 用不了。
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ai_config.*.json")
	if err != nil {
		return fmt.Errorf("create temp file next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// marshalOrdered 按给定顺序输出顶层键，其余键排在后面、按字典序：
// 直接 MarshalIndent 一个 map 会把 api 排到 actions 后面，手改文件时不好找。
func marshalOrdered(root map[string]json.RawMessage, preferred []string) ([]byte, error) {
	keys := make([]string, 0, len(root))
	seen := make(map[string]bool, len(root))
	for _, k := range preferred {
		if _, ok := root[k]; ok {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	rest := make([]string, 0, len(root))
	for k := range root {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		name, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		value, err := json.MarshalIndent(root[k], "  ", "  ")
		if err != nil {
			return nil, err
		}
		b.WriteString("\n  ")
		b.Write(name)
		b.WriteString(": ")
		b.Write(value)
	}
	b.WriteString("\n}\n")
	return []byte(b.String()), nil
}

// optInKeyNameIn 找出「base_url 开头但不是 base_url」的键名（产品侧的启用开关）。
func optInKeyNameIn(api map[string]json.RawMessage) string {
	name := ""
	for key := range api {
		if key == optInKeyName || !strings.HasPrefix(key, optInKeyName) {
			continue
		}
		if name == "" || key < name {
			name = key
		}
	}
	return name
}

func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

func describe(key string) string {
	n := utf8.RuneCountInString(key)
	if n == len(key) {
		return fmt.Sprintf("ASCII, %d chars", n)
	}
	return fmt.Sprintf("non-ASCII, %d chars", n)
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

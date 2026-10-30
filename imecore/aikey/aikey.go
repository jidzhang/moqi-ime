// Package aikey 处理内置 AI key 的混淆形态：随包分发的 key 存混淆值，
// 用户自己填的 key 保持明文。
package aikey

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// Prefix 是混淆值的命名空间：没有哪家厂商的 key 以 MQ- 开头，所以既能认出自己的
// 值，又不像 "moqi-encrypted-v1:" 那样明示"这是密文、快来解"。
const Prefix = "MQ-"

const tagLen = 4

// mask 只负责让字符串不像一把现成的 key，不是密钥材料——Resolve 就在下面，
// 代码开源，谁都能还原。爬虫拿到这串字符直接调 API 只会得到 401。
var mask = []byte("MoqiAI2026")

// Mask 把明文 key 编成随包分发的混淆值：base64(XOR(key) + sha256(key)[:4])。
func Mask(key string) string {
	plain := []byte(key)
	tag := sha256.Sum256(plain)
	blob := append(xor(plain), tag[:tagLen]...)
	return Prefix + base64.StdEncoding.EncodeToString(blob)
}

// Resolve 还原内置 key。任何不符合混淆格式的值——用户自己的明文 key（哪家厂商
// 的都有，含非 ASCII）、base64 坏掉、校验对不上——都原样返回，绝不猜。
func Resolve(raw string) string {
	if !strings.HasPrefix(raw, Prefix) {
		return raw
	}
	blob, err := base64.StdEncoding.DecodeString(raw[len(Prefix):])
	if err != nil || len(blob) <= tagLen {
		return raw
	}
	plain := xor(blob[:len(blob)-tagLen])
	tag := sha256.Sum256(plain)
	if !bytes.Equal(blob[len(blob)-tagLen:], tag[:tagLen]) {
		return raw
	}
	return string(plain)
}

// IsMasked 判断一个值是否是本包的混淆格式（前缀 + 校验都对得上）。
func IsMasked(raw string) bool {
	return Resolve(raw) != raw
}

// xor 对称：掩码一次得密文，再掩码一次还原。
func xor(data []byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ mask[i%len(mask)]
	}
	return out
}

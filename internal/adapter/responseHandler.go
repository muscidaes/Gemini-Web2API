package adapter

import (
	"io"
	"log"
	"strings"

	"github.com/tidwall/gjson"
)

// extractImageURLsFromResponse 通过递归搜索响应体中的所有有效图片 URL
func extractImageURLsFromResponse(reader io.Reader) []string {
	var urls []string

	content, _ := io.ReadAll(reader)

	log.Printf("content, %s", content)

	// 用于去重的辅助 map
	seen := make(map[string]bool)

	// 定义递归搜索函数
	var searchForURLs func(res gjson.Result)
	searchForURLs = func(res gjson.Result) {
		// 1. 如果当前节点是字符串
		if res.Type == gjson.String {
			val := res.String()
			valTrimmed := strings.TrimSpace(val)

			// 检查是否是目标图片 URL
			// 启发式规则：包含 googleusercontent.com，并且绝对不是用户头像 (/profile/)
			if strings.HasPrefix(valTrimmed, "http") &&
				strings.Contains(valTrimmed, "googleusercontent.com") &&
				!strings.Contains(valTrimmed, "/profile/picture/") {

				// 去重并添加到结果中
				if !seen[valTrimmed] {
					seen[valTrimmed] = true
					urls = append(urls, valTrimmed)
				}
				return
			}

			// 检查是否是“字符串化的 JSON” (Stringified JSON)
			// Gemini 的 payload 经常把复杂的 JSON 数组或对象压缩成一个字符串
			if (strings.HasPrefix(valTrimmed, "[") && strings.HasSuffix(valTrimmed, "]")) ||
				(strings.HasPrefix(valTrimmed, "{") && strings.HasSuffix(valTrimmed, "}")) {

				parsedInner := gjson.Parse(valTrimmed)
				// 如果解析成功且确实是 JSON 结构，则继续递归
				if parsedInner.IsArray() || parsedInner.IsObject() {
					searchForURLs(parsedInner)
				}
			}
			return
		}

		// 2. 如果当前节点是数组或对象，遍历其子节点并递归
		if res.IsArray() || res.IsObject() {
			res.ForEach(func(_, value gjson.Result) bool {
				searchForURLs(value)
				return true // 返回 true 以继续遍历
			})
		}
	}

	// 逐行处理 payload (处理多段下发的流式 JSON)
	for _, line := range strings.Split(string(content), "\n") {
		// 清理 Google 特有的安全前缀
		line = strings.TrimPrefix(line, ")]}'")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 解析最外层 JSON，并将其送入递归扫描器
		outer := gjson.Parse(line)
		searchForURLs(outer)
	}

	return urls
}

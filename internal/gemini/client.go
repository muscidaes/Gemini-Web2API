package gemini

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gemini-web2api/internal/browser"

	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

const (
	EndpointGoogle   = "https://www.google.com"
	EndpointInit     = "https://gemini.google.com/app"
	EndpointGenerate = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
)

// ModelHeaders maps model names to their specific required headers.
// You can add new models here by inspecting the 'x-goog-ext-525001261-jspb' header in browser DevTools.
var ModelHeaders = map[string]string{
	"gemini-2.5-flash":                   `[1,null,null,null,"71c2d248d3b102ff"]`,
	"gemini-3.1-pro-preview":             `[1,null,null,null,"e6fa609c3fa255c0"]`,
	"gemini-3-flash-preview":             `[1,null,null,null,"e051ce1aa80aa576"]`,
	"gemini-3-flash-preview-no-thinking": `[1,null,null,null,"56fdd199312815e2"]`,
	"gemini-2.5-flash-image":             `[1,null,null,null,"56fdd199312815e2",null,null,0,[4],null,null,2]`,
	"gemini-3-pro-image-preview":         `[1,null,null,null,"e051ce1aa80aa576",null,null,0,[4],null,null,2]`,
}

type Client struct {
	httpClient tls_client.HttpClient
	Cookies    map[string]string
	SNlM0e     string
	VersionBL  string
	FSID       string
	ReqID      int
	AccountID  string
	ProxyURL   string
}

func NewClient(cookies map[string]string, proxyURL string) (*Client, error) {
	profile := GetRandomProfile()

	options := GetClientOptions(profile, proxyURL)

	options = append(options, tls_client.WithForceHttp1())

	// 使用包含降级选项的 options 创建 client
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}

	u, _ := url.Parse("https://gemini.google.com")
	var cookieList []*http.Cookie
	for k, v := range cookies {
		cookieList = append(cookieList, &http.Cookie{
			Name:   k,
			Value:  v,
			Domain: ".google.com",
			Path:   "/",
		})
	}
	client.SetCookies(u, cookieList)

	return &Client{
		httpClient: client,
		Cookies:    cookies,
		ReqID:      GenerateReqID(),
		ProxyURL:   strings.TrimSpace(proxyURL),
	}, nil
}

func (c *Client) Init() error {
	req, _ := http.NewRequest(http.MethodGet, EndpointInit, nil)
	req.Header.Set("User-Agent", GetCurrentUserAgent())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", getLangHeader())
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("account '%s' failed to visit init page: %v", c.displayAccountID(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("account '%s' init page returned status: %d", c.displayAccountID(), resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyString := string(bodyBytes)

	reSN := regexp.MustCompile(`"SNlM0e":"(.*?)"`)
	matchSN := reSN.FindStringSubmatch(bodyString)
	if len(matchSN) < 2 {
		return fmt.Errorf("SNlM0e token not found in response. This usually means cookies are invalid or expired (account: %s)", c.displayAccountID())
	}
	c.SNlM0e = matchSN[1]

	reBL := regexp.MustCompile(`"bl":"(.*?)"`)
	matchBL := reBL.FindStringSubmatch(bodyString)
	if len(matchBL) >= 2 {
		c.VersionBL = matchBL[1]
	} else {
		reBL2 := regexp.MustCompile(`data-bl="(.*?)"`)
		matchBL2 := reBL2.FindStringSubmatch(bodyString)
		if len(matchBL2) >= 2 {
			c.VersionBL = matchBL2[1]
		}
	}

	// 直接匹配 BL 字串格式
	if c.VersionBL == "" {
		reBL3 := regexp.MustCompile(`boq_assistant-bard-web-server_[a-zA-Z0-9._]+`)
		matchBL3 := reBL3.FindString(bodyString)
		if matchBL3 != "" {
			c.VersionBL = matchBL3
		}
	}

	if c.VersionBL == "" {
		snippet := bodyString
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		log.Printf("Warning: Could not extract 'bl' version, using fallback. Response preview: %s", snippet)
		c.VersionBL = "boq_assistant-bard-web-server_20260218.05_p0"
	} else {
		log.Printf("Extracted BL Version: %s", c.VersionBL)
	}

	reSID := regexp.MustCompile(`"f.sid":"(.*?)"`)
	matchSID := reSID.FindStringSubmatch(bodyString)
	if len(matchSID) >= 2 {
		c.FSID = matchSID[1]
	}

	return nil
}

func (c *Client) RefreshCookies() error {
	displayID := c.displayAccountID()
	log.Printf("账号 '%s' 的旧 Cookie 已失效，正在从 .env 移除并尝试从浏览器重新获取...", displayID)

	// 1. 先构造一个清空旧 Cookie 的 map 并保存（模拟“移除”动作）
	suffix := ""
	if c.AccountID != "" {
		suffix = "_" + c.AccountID
	}
	clearMap := make(map[string]string)
	clearMap["__Secure-1PSID"+suffix] = ""
	clearMap["__Secure-1PSIDTS"+suffix] = ""
	browser.SaveToEnv(clearMap)

	// 2. 从浏览器加载新 Cookie
	newCookies, err := browser.LoadCookiesFromBrowser()
	if err != nil {
		return fmt.Errorf("自动获取新 Cookie 失败: %v", err)
	}

	// 3. 将新 Cookie 保存到 .env 并装载到当前客户端
	saveMap := make(map[string]string)
	saveMap["__Secure-1PSID"+suffix] = newCookies["__Secure-1PSID"]
	saveMap["__Secure-1PSIDTS"+suffix] = newCookies["__Secure-1PSIDTS"]
	browser.SaveToEnv(saveMap)

	c.Cookies = newCookies
	u, _ := url.Parse("https://gemini.google.com")
	var cookieList []*http.Cookie
	for k, v := range newCookies {
		cookieList = append(cookieList, &http.Cookie{
			Name:   k,
			Value:  v,
			Domain: ".google.com",
			Path:   "/",
		})
	}
	c.httpClient.SetCookies(u, cookieList)

	log.Printf("账号 '%s' 已装载新获取的 Cookie，正在进行重新初始化验证...", displayID)
	return c.Init()
}

func (c *Client) StreamGenerateContent(ctx context.Context, prompt string, model string, files []FileData, meta *ChatMetadata) (io.ReadCloser, error) {
	resp, err := c.doGenerateContentRequest(ctx, prompt, model, files, meta)
	if err != nil {
		return nil, err
	}

	// 如果状态码不为 200，触发 Cookie 刷新
	if resp.StatusCode != http.StatusOK {
		preview := readBodyPreview(resp.Body)
		statusCode := resp.StatusCode
		resp.Body.Close()
		log.Printf("账号 '%s' 请求失败，状态码 %d，响应预览: %s", c.displayAccountID(), statusCode, preview)

		if err := c.RefreshCookies(); err != nil {
			log.Printf("账号 '%s' 刷新 Cookie 失败: %v", c.displayAccountID(), err)
			return nil, fmt.Errorf("generate request failed with status: %d and refresh failed: %v", statusCode, err)
		}

		// 刷新成功后重试一次
		log.Printf("账号 '%s' Cookie 刷新成功，正在重试请求...", c.displayAccountID())
		resp, err = c.doGenerateContentRequest(ctx, prompt, model, files, meta)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			preview = readBodyPreview(resp.Body)
			resp.Body.Close()
			log.Printf("账号 '%s' 重试请求仍然失败，状态码 %d，响应预览: %s", c.displayAccountID(), resp.StatusCode, preview)
			return nil, fmt.Errorf("generate request failed with status: %d after refresh", resp.StatusCode)
		}
	}

	return resp.Body, nil
}

func (c *Client) doGenerateContentRequest(ctx context.Context, prompt string, model string, files []FileData, meta *ChatMetadata) (*http.Response, error) {
	payload := BuildGeneratePayload(prompt, c.ReqID, files, meta)
	c.ReqID++

	form := url.Values{}
	form.Set("f.req", payload)
	form.Set("at", c.SNlM0e)
	data := form.Encode()

	req, _ := http.NewRequest(http.MethodPost, EndpointGenerate, strings.NewReader(data))
	req = req.WithContext(ctx)

	q := req.URL.Query()
	q.Add("bl", c.VersionBL)
	q.Add("_reqid", fmt.Sprintf("%d", c.ReqID))
	q.Add("rt", "c")
	if c.FSID != "" {
		q.Add("f.sid", c.FSID)
	}
	req.URL.RawQuery = q.Encode()

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	req.Header.Set("User-Agent", GetCurrentUserAgent())
	req.Header.Set("Origin", "https://gemini.google.com")
	req.Header.Set("Referer", "https://gemini.google.com/")
	req.Header.Set("X-Same-Domain", "1")
	req.Header.Set("Accept-Language", getLangHeader())
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	if headerVal, ok := ModelHeaders[model]; ok {
		req.Header.Set("x-goog-ext-525001261-jspb", headerVal)
	} else {
		log.Printf("Warning: Unknown model '%s', using default header (gemini-2.5-flash).", model)
		req.Header.Set("x-goog-ext-525001261-jspb", ModelHeaders["gemini-2.5-flash"])
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *Client) FetchImage(imageURL string) ([]byte, error) {
	maxRedirects := 5
	currentURL := imageURL

	for i := 0; i < maxRedirects; i++ {
		u, _ := url.Parse(currentURL)
		var cookieList []*http.Cookie
		for k, v := range c.Cookies {
			cookieList = append(cookieList, &http.Cookie{
				Name:   k,
				Value:  v,
				Domain: u.Host,
				Path:   "/",
			})
		}
		c.httpClient.SetCookies(u, cookieList)

		req, _ := http.NewRequest(http.MethodGet, currentURL, nil)
		req.Header.Set("User-Agent", GetCurrentUserAgent())
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			resp.Body.Close()
			if location == "" {
				return nil, fmt.Errorf("redirect with no Location header")
			}
			currentURL = location
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("image fetch failed with status: %d", resp.StatusCode)
		}

		return io.ReadAll(resp.Body)
	}

	return nil, fmt.Errorf("too many redirects")
}

func (c *Client) FetchImage1(ctx context.Context, imageURL string) ([]byte, error) {
	return c.doFetchImageWithRetry(ctx, imageURL, true)
}

func (c *Client) doFetchImageWithRetry(ctx context.Context, imageURL string, retry bool) ([]byte, error) {
	maxRedirects := 5
	currentURL := imageURL

	// 1. 基础延迟 1 秒
	baseDelay := time.Second

	// 2. 额外生成 0 ~ 1 秒之间的随机时间 (纳秒级精度)
	// rand.Int63n 接受一个 int64 类型的值，并返回 [0, n) 范围的随机数
	extraDelay := time.Duration(rand.Int63n(int64(time.Second)))

	// 3. 计算总延迟时间 (1秒 ~ 2秒)
	totalDelay := baseDelay + extraDelay

	// 执行延迟 (支持 Context 取消)
	fmt.Printf("开始睡眠，随机延迟时间为: %v\n", totalDelay)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(totalDelay):
	}

	for i := 0; i < maxRedirects; i++ {
		req, err := http.NewRequest(http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, fmt.Errorf("创建请求失败: %w", err)
		}
		req = req.WithContext(ctx)

		// 1. 每次重定向都重新注入 Cookie (保留你的原逻辑)
		for k, v := range c.Cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}

		// 2. 设置伪装 Headers
		req.Header.Set("User-Agent", GetCurrentUserAgent())
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
		req.Header.Set("Referer", "https://googleusercontent.com/")

		// 3. 解决 unexpected EOF 的杀手锏：禁用连接复用
		req.Close = true

		// 发起请求
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("请求执行失败: %w", err)
		}

		// 4. 手动处理 3xx 重定向
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			resp.Body.Close() // 关键：丢弃当前响应体，释放资源

			if location == "" {
				return nil, fmt.Errorf("遇到重定向状态码 %d，但没有 Location 头", resp.StatusCode)
			}

			// 处理 Location 可能是相对路径的情况 (兼容性更好)
			u, err := resp.Request.URL.Parse(location)
			if err != nil {
				return nil, fmt.Errorf("解析重定向 URL 失败: %w", err)
			}

			currentURL = u.String()
			continue // 拿着新的 URL 进入下一次循环
		}

		// 5. 处理最终结果，必须 defer 关闭成功后的 Body
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			if retry && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) {
				log.Printf("账号 '%s' 下载图片返回 %d，尝试刷新 Cookie 并重试...", c.displayAccountID(), resp.StatusCode)
				if refreshErr := c.RefreshCookies(); refreshErr == nil {
					return c.doFetchImageWithRetry(ctx, imageURL, false)
				}
			}
			return nil, fmt.Errorf("图片获取失败，最终状态码: %d", resp.StatusCode)
		}

		// 6. 安全读取数据
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("读取图片流时中断 (unexpected EOF): %w", err)
		}

		return data, nil
	}

	return nil, fmt.Errorf("重定向次数超过限制 (%d 次)", maxRedirects)
}

func (c *Client) FetchImage2(imageURL string) ([]byte, error) {
	maxRetries := 3 // 整个流程最多重试 3 次

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// 如果是重试，稍微等一下，避免被 Google 的高频检测拦截
			time.Sleep(time.Duration(attempt) * time.Second)
			fmt.Printf("第 %d 次尝试重新下载图片: %s\n", attempt+1, imageURL)
		}

		data, err := c.doFetchImage(imageURL)
		if err == nil {
			return data, nil // 成功下载，直接返回
		}
		lastErr = err

		// 只有遇到 EOF 或者网络相关的错误时才重试
		if !strings.Contains(err.Error(), "EOF") && !strings.Contains(err.Error(), "connection") {
			return nil, err // 如果是 404 等明确的错误，就不重试了
		}
	}

	return nil, fmt.Errorf("重试 %d 次后仍然失败, 最后一次错误: %w", maxRetries, lastErr)
}

// 这是你之前的核心逻辑，稍微做了一点防断流的升级
func (c *Client) doFetchImage(imageURL string) ([]byte, error) {
	maxRedirects := 5
	currentURL := imageURL

	for i := 0; i < maxRedirects; i++ {
		req, err := http.NewRequest(http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, fmt.Errorf("创建请求失败: %w", err)
		}

		for k, v := range c.Cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}

		req.Header.Set("User-Agent", GetCurrentUserAgent())
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
		req.Header.Set("Referer", "https://googleusercontent.com/")

		// 🌟 关键新增：强制要求服务器返回未压缩的原始数据 (identity)，防止代理软件解压失败导致 EOF
		req.Header.Set("Accept-Encoding", "identity")

		req.Close = true

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("请求执行失败: %w", err)
		}

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			resp.Body.Close()

			if location == "" {
				return nil, fmt.Errorf("遇到重定向状态码 %d，但没有 Location 头", resp.StatusCode)
			}

			u, err := resp.Request.URL.Parse(location)
			if err != nil {
				return nil, fmt.Errorf("解析重定向 URL 失败: %w", err)
			}
			currentURL = u.String()
			continue
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("图片获取失败，最终状态码: %d", resp.StatusCode)
		}

		// 🌟 增加对 ioutil.ReadAll 的保护
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			// 哪怕发生了 EOF，如果其实已经读到了数据，我们可以选择“原谅”它
			if len(data) > 0 {
				fmt.Printf("警告: 读取时发生 %v, 但已获取 %d bytes, 尝试继续...\n", err, len(data))
				return data, nil
			}
			return nil, fmt.Errorf("读取图片流时中断 (unexpected EOF): %w", err)
		}

		return data, nil
	}

	return nil, fmt.Errorf("重定向次数超过限制")
}

func GetLanguage() string {
	lang := os.Getenv("LANGUAGE")
	if lang == "" {
		lang = "en"
	}
	return lang
}

func getLangHeader() string {
	lang := GetLanguage()
	return lang + ",en;q=0.9"
}

func (c *Client) displayAccountID() string {
	if strings.TrimSpace(c.AccountID) == "" {
		return "default"
	}
	return c.AccountID
}

func readBodyPreview(body io.ReadCloser) string {
	if body == nil {
		return ""
	}

	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Sprintf("读取响应失败: %v", err)
	}

	preview := strings.TrimSpace(string(data))
	runes := []rune(preview)
	if len(runes) > 500 {
		preview = string(runes[:500])
	}

	if preview == "" {
		return "<empty>"
	}

	return preview
}

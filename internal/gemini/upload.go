package gemini

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"mime/multipart"

	http "github.com/bogdanfinn/fhttp"
)

const (
	EndpointUpload = "https://content-push.googleapis.com/upload"
	UploadPushID   = "feeds/mcudyrk2a4khkz"
)

func (c *Client) UploadFile(ctx context.Context, data []byte, filename string) (string, error) {
	return c.doUploadFile(ctx, data, filename, true)
}

func (c *Client) doUploadFile(ctx context.Context, data []byte, filename string, retry bool) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %v", err)
	}

	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("failed to write file data: %v", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, EndpointUpload, &buf)
	if err != nil {
		return "", err
	}
	req = req.WithContext(ctx)

	req.Header.Set("Push-ID", UploadPushID)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", GetCurrentUserAgent())
	req.Header.Set("Origin", "https://gemini.google.com")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		if retry {
			log.Printf("账号 '%s' 图片上传失败 (状态码 %d)，尝试刷新 Cookie 并重试...", c.displayAccountID(), resp.StatusCode)
			if refreshErr := c.RefreshCookies(); refreshErr == nil {
				return c.doUploadFile(ctx, data, filename, false)
			} else {
				log.Printf("账号 '%s' 刷新 Cookie 失败: %v", c.displayAccountID(), refreshErr)
			}
		}
		return "", fmt.Errorf("upload failed with status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Provider, BaseURL, APIKey, Model, AppID, SystemPrompt string
	Timeout                                               time.Duration
}

type Client struct{ HTTP *http.Client }

func New() *Client { return &Client{HTTP: &http.Client{Timeout: 45 * time.Second}} }

func (c *Client) Generate(ctx context.Context, cfg Config, userMessage string) (string, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "anthropic":
		return c.anthropic(ctx, cfg, userMessage)
	case "gemini":
		return c.gemini(ctx, cfg, userMessage)
	case "dashscope_app":
		return c.dashscopeApp(ctx, cfg, userMessage)
	case "openai", "openai_compatible", "":
		return c.openAI(ctx, cfg, userMessage)
	default:
		return "", fmt.Errorf("不支持的AI服务商: %s", cfg.Provider)
	}
}

func (c *Client) openAI(ctx context.Context, cfg Config, message string) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	messages := []map[string]string{}
	if cfg.SystemPrompt != "" {
		messages = append(messages, map[string]string{"role": "system", "content": cfg.SystemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": message})
	body := map[string]any{"model": cfg.Model, "messages": messages, "temperature": 0.4, "stream": false}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, base+"/chat/completions", cfg.APIKey, nil, body, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("AI服务未返回回复内容")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func (c *Client) anthropic(ctx context.Context, cfg Config, message string) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com"
	}
	body := map[string]any{"model": cfg.Model, "max_tokens": 1024, "messages": []map[string]string{{"role": "user", "content": message}}}
	if cfg.SystemPrompt != "" {
		body["system"] = cfg.SystemPrompt
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	headers := map[string]string{"x-api-key": cfg.APIKey, "anthropic-version": "2023-06-01"}
	if err := c.requestJSON(ctx, http.MethodPost, base+"/v1/messages", "", headers, body, &out); err != nil {
		return "", err
	}
	if len(out.Content) == 0 || strings.TrimSpace(out.Content[0].Text) == "" {
		return "", errors.New("AI服务未返回回复内容")
	}
	return strings.TrimSpace(out.Content[0].Text), nil
}

func (c *Client) gemini(ctx context.Context, cfg Config, message string) (string, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	prompt := message
	if cfg.SystemPrompt != "" {
		prompt = cfg.SystemPrompt + "\n\n" + message
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", base, url.PathEscape(cfg.Model), url.QueryEscape(cfg.APIKey))
	body := map[string]any{"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}}}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, endpoint, "", nil, body, &out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", errors.New("AI服务未返回回复内容")
	}
	return strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text), nil
}

func (c *Client) dashscopeApp(ctx context.Context, cfg Config, message string) (string, error) {
	endpoint := cfg.BaseURL
	if endpoint == "" {
		endpoint = "https://dashscope.aliyuncs.com/api/v1/apps/{app_id}/completion"
	}
	appID := cfg.AppID
	if appID == "" {
		appID = cfg.Model
	}
	endpoint = strings.ReplaceAll(endpoint, "{app_id}", url.PathEscape(appID))
	prompt := message
	if cfg.SystemPrompt != "" {
		prompt = cfg.SystemPrompt + "\n\n" + message
	}
	body := map[string]any{"input": map[string]any{"prompt": prompt}, "parameters": map[string]any{}}
	var out struct {
		Output struct {
			Text string `json:"text"`
		} `json:"output"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, endpoint, cfg.APIKey, nil, body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Output.Text) == "" {
		return "", errors.New("AI服务未返回回复内容")
	}
	return strings.TrimSpace(out.Output.Text), nil
}

func (c *Client) Models(ctx context.Context, cfg Config) ([]map[string]string, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "anthropic" || provider == "dashscope_app" {
		return []map[string]string{}, nil
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		if provider == "gemini" {
			base = "https://generativelanguage.googleapis.com"
		} else {
			base = "https://api.openai.com/v1"
		}
	}
	endpoint := base + "/models"
	if provider == "gemini" {
		endpoint += "?key=" + url.QueryEscape(cfg.APIKey)
	}
	var raw map[string]any
	if err := c.requestJSON(ctx, http.MethodGet, endpoint, cfg.APIKey, nil, nil, &raw); err != nil {
		return nil, err
	}
	models := []map[string]string{}
	values, _ := raw["data"].([]any)
	if provider == "gemini" {
		values, _ = raw["models"].([]any)
	}
	for _, value := range values {
		item, _ := value.(map[string]any)
		id := strings.TrimPrefix(fmt.Sprint(item["id"]), "models/")
		if id == "<nil>" || id == "" {
			id = strings.TrimPrefix(fmt.Sprint(item["name"]), "models/")
		}
		if id != "" && id != "<nil>" {
			models = append(models, map[string]string{"id": id, "name": id})
		}
	}
	return models, nil
}

func (c *Client) requestJSON(ctx context.Context, method, endpoint, bearer string, headers map[string]string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("AI接口返回HTTP %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	if err = json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("AI接口响应解析失败: %w", err)
	}
	return nil
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

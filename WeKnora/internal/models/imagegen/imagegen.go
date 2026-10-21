// Package imagegen implements image output independently of chat/vision input.
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/models/api"
	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	_ "golang.org/x/image/webp"
)

const MaxImageBytes = 20 << 20
const maxPixels = 32 << 20
const maxResponseBytes = (MaxImageBytes+2)/3*4 + 1<<20

type Request struct{ Prompt, Size, Quality string }

// Result holds actual decoded image bytes, never a short-lived upstream URL.
type Result struct {
	Data          []byte
	MIMEType      string
	Width, Height int
	RevisedPrompt string
}
type Generator interface {
	Generate(context.Context, Request) (*Result, error)
	GetModelName() string
	GetModelID() string
}
type Config struct {
	ModelID, ModelName, BaseURL, APIKey, Provider string
	ExtraConfig                                   map[string]string
	CustomHeaders                                 map[string]string
}

func ConfigFromModel(m *types.Model) *Config {
	if m == nil {
		return nil
	}
	return &Config{ModelID: m.ID, ModelName: m.Name, BaseURL: m.Parameters.BaseURL, APIKey: m.Parameters.APIKey, Provider: m.Parameters.Provider, ExtraConfig: m.Parameters.ExtraConfig, CustomHeaders: m.Parameters.CustomHeaders}
}

type generator struct {
	endpoint                      api.Endpoint
	size, quality, responseFormat string
	timeout                       time.Duration
}

func New(config *Config) (Generator, error) {
	if config == nil {
		return nil, fmt.Errorf("image generation config is nil")
	}
	resolved, err := modelruntime.Resolve(modelruntime.Ref{Provider: config.Provider, Model: config.ModelName, BaseURL: config.BaseURL, ModelType: types.ModelTypeImageGeneration, Extra: config.ExtraConfig})
	if err != nil {
		return nil, err
	}
	if resolved.Vendor.RequiresAuth && strings.TrimSpace(config.APIKey) == "" && strings.TrimSpace(resolved.Vendor.DefaultAPIKey) == "" {
		return nil, fmt.Errorf("image generation API key is required")
	}
	if err := secutils.ValidateURLForSSRF(resolved.BaseURL); err != nil {
		return nil, fmt.Errorf("image generation endpoint is not allowed")
	}
	u, err := url.Parse(resolved.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("image generation base URL must not contain credentials, query or fragment")
	}
	endpoint, err := resolved.Endpoint(types.ModelTypeImageGeneration, modelruntime.Connection{ModelID: config.ModelID, Credentials: api.Credentials{APIKey: config.APIKey}, Headers: config.CustomHeaders, Extra: config.ExtraConfig})
	if err != nil {
		return nil, err
	}
	// This protocol is only advertised by OpenAI and explicitly compatible generic endpoints.
	endpoint.URL = ""
	if strings.HasSuffix(strings.TrimRight(endpoint.BaseURL, "/"), "/images/generations") {
		endpoint.URL = strings.TrimRight(endpoint.BaseURL, "/")
	}
	seconds := 300
	if configured := strings.TrimSpace(config.ExtraConfig["image_timeout_seconds"]); configured != "" {
		seconds, _ = strconv.Atoi(configured)
	}
	return &generator{endpoint: endpoint, size: strings.TrimSpace(config.ExtraConfig["image_size"]), quality: strings.TrimSpace(config.ExtraConfig["image_quality"]), responseFormat: strings.TrimSpace(config.ExtraConfig["image_response_format"]), timeout: time.Duration(seconds) * time.Second}, nil
}
func (g *generator) GetModelName() string { return g.endpoint.Model }
func (g *generator) GetModelID() string   { return g.endpoint.ModelID }
func (g *generator) Generate(ctx context.Context, input Request) (*Result, error) {
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" || utf8.RuneCountInString(prompt) > 64000 {
		return nil, fmt.Errorf("image prompt must contain 1 to 64000 characters")
	}
	size, quality := strings.TrimSpace(input.Size), strings.TrimSpace(input.Quality)
	if size == "" {
		size = g.size
	}
	if quality == "" {
		quality = g.quality
	}
	if err := modelruntime.ValidateImageGenerationParameters(map[string]string{"image_size": size, "image_quality": quality}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	body := map[string]any{"model": g.endpoint.Model, "prompt": prompt, "n": 1}
	if size != "" {
		body["size"] = size
	}
	if quality != "" {
		body["quality"] = quality
	}
	if g.responseFormat != "" {
		body["response_format"] = g.responseFormat
	}
	req, _, err := g.endpoint.NewRequest(ctx, g.endpoint.Resolve("/images/generations"), body, false)
	if err != nil {
		return nil, fmt.Errorf("prepare image generation request: %w", err)
	}
	// Never automatically retry a request that may have incurred a charge.
	response, err := g.endpoint.Do(req)
	if err != nil {
		if status, ok := err.(*api.HTTPError); ok {
			return nil, fmt.Errorf("image generation provider returned HTTP %d", status.StatusCode)
		}
		return nil, fmt.Errorf("image generation request failed; upstream completion is unknown")
	}
	defer response.Body.Close()
	raw, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read image generation response: %w", err)
	}
	var output struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			URL           string `json:"url"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, fmt.Errorf("invalid image generation response")
	}
	if len(output.Data) != 1 {
		return nil, fmt.Errorf("image generation must return exactly one image")
	}
	item := output.Data[0]
	var data []byte
	if item.B64JSON != "" {
		if len(item.B64JSON) > (MaxImageBytes+2)/3*4 {
			return nil, fmt.Errorf("image exceeds byte limit")
		}
		data, err = base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 image")
		}
	} else if item.URL != "" {
		data, err = g.download(ctx, item.URL)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("provider response contains no image")
	}
	result, err := ValidateImage(data)
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(item.RevisedPrompt) <= 64000 {
		result.RevisedPrompt = item.RevisedPrompt
	}
	return result, nil
}
func (g *generator) download(ctx context.Context, target string) ([]byte, error) {
	if err := secutils.ValidateURLForSSRF(target); err != nil {
		return nil, fmt.Errorf("generated image URL is not allowed")
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil {
		return nil, fmt.Errorf("invalid generated image URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid generated image URL")
	}
	// A fresh request deliberately carries neither the model API key nor custom headers.
	client := api.HTTPClient
	if g.endpoint.Client != nil {
		client = g.endpoint.Client
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download generated image failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download generated image returned HTTP %d", response.StatusCode)
	}
	return readBounded(response.Body, MaxImageBytes)
}
func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("image response exceeds byte limit")
	}
	return data, nil
}

// ValidateImage verifies actual bytes before storage; declared MIME is not trusted.
func ValidateImage(data []byte) (*Result, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return nil, fmt.Errorf("image is empty or exceeds byte limit")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("provider did not return a supported image")
	}
	mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}[format]
	if mime == "" || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > maxPixels {
		return nil, fmt.Errorf("image format or dimensions exceed supported bounds")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("generated image is corrupt")
	}
	return &Result{Data: data, MIMEType: mime, Width: config.Width, Height: config.Height}, nil
}

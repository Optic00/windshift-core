package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"windshift/internal/llm"
	"windshift/internal/repository"
)

// Broker-side LLM metering is authoritative: ProxyLLM parses terminal SSE usage
// chunks and records token counts/cost, not untrusted agent reports.

// maxMeterLineBytes prevents pathological SSE lines from growing memory unbounded.
const maxMeterLineBytes = 1 << 20 // 1 MiB

// sseUsage is the parsed usage tail of a streamed chat completion. Cost is set
// only when the provider returns it inline (OpenRouter with usage.include).
type sseUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Cost             *float64
}

// usageMeteringBody wraps an SSE response body, scanning `data:` lines for the
// terminal usage chunk. It is a transparent pass-through — bytes are returned
// to the proxy unchanged — and invokes onClose exactly once (on EOF or Close)
// with the last usage seen, or nil if the stream carried none.
type usageMeteringBody struct {
	rc      io.ReadCloser
	line    bytes.Buffer
	dropped bool // current line exceeded the cap; ignore until newline
	usage   *sseUsage
	onClose func(*sseUsage)
	once    sync.Once
}

func (b *usageMeteringBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.scan(p[:n])
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *usageMeteringBody) Close() error {
	b.finish()
	return b.rc.Close()
}

func (b *usageMeteringBody) finish() {
	b.once.Do(func() { b.onClose(b.usage) })
}

func (b *usageMeteringBody) scan(p []byte) {
	for _, c := range p {
		if c == '\n' {
			if !b.dropped {
				b.processLine(b.line.Bytes())
			}
			b.line.Reset()
			b.dropped = false
			continue
		}
		if b.dropped {
			continue
		}
		if b.line.Len() >= maxMeterLineBytes {
			b.line.Reset()
			b.dropped = true
			continue
		}
		b.line.WriteByte(c)
	}
}

func (b *usageMeteringBody) processLine(raw []byte) {
	line := bytes.TrimSpace(raw)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	var chunk struct {
		Usage *struct {
			PromptTokens     int      `json:"prompt_tokens"`
			CompletionTokens int      `json:"completion_tokens"`
			TotalTokens      int      `json:"total_tokens"`
			Cost             *float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &chunk); err != nil || chunk.Usage == nil {
		return
	}
	b.usage = &sseUsage{
		PromptTokens:     chunk.Usage.PromptTokens,
		CompletionTokens: chunk.Usage.CompletionTokens,
		TotalTokens:      chunk.Usage.TotalTokens,
		Cost:             chunk.Usage.Cost,
	}
}

// countImageParts counts image_url content parts across all messages in a
// chat-completions request body, so the broker can price view_image usage when
// the provider doesn't return an inline cost. A string-content message has no
// parts and contributes zero.
func countImageParts(body []byte) int {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return 0
	}
	count := 0
	for _, m := range req.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			continue // string content (not an array) → no image parts
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				count++
			}
		}
	}
	return count
}

// meterLLMResponse returns a ReverseProxy ModifyResponse hook that meters a
// streamed chat completion. It wraps the SSE body so the usage tail is captured
// as bytes flow to the agent; non-streaming (JSON) responses are left untouched
// (cost unknown). Persistence runs on stream end against a detached context so
// the row survives the request context being canceled as the stream closes.
func (h *RunnerBrokerHandler) meterLLMResponse(runID int, model string, pricing *llm.Pricing, images int) func(*http.Response) error {
	return func(resp *http.Response) error {
		if h.usage == nil {
			return nil
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			return nil
		}
		resp.Body = &usageMeteringBody{
			rc: resp.Body,
			onClose: func(u *sseUsage) {
				if u == nil {
					return
				}
				rec := repository.LLMUsageRecord{
					RunID:            runID,
					Model:            model,
					PromptTokens:     u.PromptTokens,
					CompletionTokens: u.CompletionTokens,
					TotalTokens:      u.TotalTokens,
				}
				switch {
				case u.Cost != nil:
					rec.CostUSD = u.Cost
					rec.CostSource = "provider"
				case pricing != nil:
					c := pricing.CostUSD(u.PromptTokens, u.CompletionTokens, images)
					rec.CostUSD = &c
					rec.CostSource = "computed"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := h.usage.Insert(ctx, rec); err != nil {
					slog.Warn("persist llm usage", slog.Int("run_id", runID), slog.Any("error", err))
				}
			},
		}
		return nil
	}
}

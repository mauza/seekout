package pricing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLM calls an OpenAI-compatible chat completions endpoint (the LiteLLM
// gateway). Structured output is a forced call of one tool whose parameters
// are the output schema: the gateway bridges the ChatGPT models to the
// Responses API and drops response_format, but it keeps tools.
type LLM struct {
	URL  string // base URL ending in /v1
	Key  string
	HTTP *http.Client
}

// Tool is the one function the model must call; Parameters is its JSON
// schema and the shape Call decodes into out.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Call sends one system + user turn and decodes the forced tool call's
// arguments into out.
func (c *LLM) Call(ctx context.Context, model, system, user string, tool Tool, out any) error {
	body, err := json.Marshal(map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
			},
		}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]string{"name": tool.Name}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 4 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("llm: read: %w", err)
	}
	var res struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("llm: HTTP %d: undecodable body: %.200s", resp.StatusCode, raw)
	}
	if res.Error != nil {
		return fmt.Errorf("llm: HTTP %d: %s", resp.StatusCode, res.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llm: HTTP %d", resp.StatusCode)
	}
	for _, ch := range res.Choices {
		for _, tc := range ch.Message.ToolCalls {
			if tc.Function.Name != tool.Name {
				continue
			}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), out); err != nil {
				return fmt.Errorf("llm: %s arguments: %w", tool.Name, err)
			}
			return nil
		}
	}
	return errors.New("llm: the model did not call " + tool.Name)
}

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ParsedIntent struct {
	Action      string  `json:"action"`      // "send", "swap", "bridge", "rule", "balance", "status", "help"
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`    // "USDC", "EURC", "USYC"
	Recipient   string  `json:"recipient"`   // "0x..." or label
	SourceChain string  `json:"sourceChain"` // for bridge e.g. "Ethereum"
	TargetChain string  `json:"targetChain"` // "Arc"
	TriggerRate float64 `json:"triggerRate"` // for rules e.g. 0.95
	Reasoning   string  `json:"reasoning"`
}

type AIParser struct {
	GeminiAPIKey string
	AnthropicKey string
	HTTPClient   *http.Client
}

func NewAIParser() *AIParser {
	return &AIParser{
		GeminiAPIKey: os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY"),
		AnthropicKey: os.Getenv("ANTHROPIC_API_KEY"),
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

const systemPrompt = `You are Rova AI, an autonomous stablecoin execution agent on Arc Testnet (Chain ID 5042002).
Parse the user's intent into a JSON object matching this exact schema:
{
  "action": "send" | "swap" | "bridge" | "rule" | "balance" | "status" | "help",
  "amount": number,
  "currency": "USDC" | "EURC" | "USYC",
  "recipient": "0x... or label",
  "sourceChain": "Ethereum" | "Arbitrum" | "Optimism" | "Base" | "Arc",
  "targetChain": "Arc",
  "triggerRate": number,
  "reasoning": "One line explanation"
}
Return ONLY minified valid JSON. No markdown backticks, no markdown text.`

func (p *AIParser) ParseIntent(ctx context.Context, userInput string) (*ParsedIntent, error) {
	if p.GeminiAPIKey != "" {
		intent, err := p.callGemini(ctx, userInput)
		if err == nil && intent != nil {
			return intent, nil
		}
	}

	if p.AnthropicKey != "" {
		intent, err := p.callAnthropic(ctx, userInput)
		if err == nil && intent != nil {
			return intent, nil
		}
	}

	return p.failsafeParse(userInput), nil
}

func (p *AIParser) callGemini(ctx context.Context, input string) (*ParsedIntent, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent?key=%s", p.GeminiAPIKey)
	promptText := fmt.Sprintf("%s\n\nUser Input: \"%s\"", systemPrompt, input)

	reqPayload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": promptText},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("gemini API error (%d)", resp.StatusCode)
	}

	respBytes, _ := io.ReadAll(resp.Body)
	var geminiRes struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(respBytes, &geminiRes); err != nil {
		return nil, err
	}

	if len(geminiRes.Candidates) == 0 || len(geminiRes.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty response from gemini")
	}

	rawJSON := geminiRes.Candidates[0].Content.Parts[0].Text
	return cleanAndUnmarshalJSON(rawJSON)
}

func (p *AIParser) callAnthropic(ctx context.Context, input string) (*ParsedIntent, error) {
	url := "https://api.anthropic.com/v1/messages"
	reqPayload := map[string]interface{}{
		"model":      "claude-3-5-sonnet-20240620",
		"max_tokens": 1024,
		"system":     systemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": input},
		},
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.AnthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("anthropic API error (%d)", resp.StatusCode)
	}

	respBytes, _ := io.ReadAll(resp.Body)
	var anthropicRes struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(respBytes, &anthropicRes); err != nil {
		return nil, err
	}

	if len(anthropicRes.Content) == 0 {
		return nil, fmt.Errorf("empty response from anthropic")
	}

	return cleanAndUnmarshalJSON(anthropicRes.Content[0].Text)
}

func cleanAndUnmarshalJSON(raw string) (*ParsedIntent, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var intent ParsedIntent
	if err := json.Unmarshal([]byte(cleaned), &intent); err != nil {
		return nil, err
	}
	return &intent, nil
}

func (p *AIParser) failsafeParse(input string) *ParsedIntent {
	text := strings.ToLower(strings.TrimSpace(input))

	intent := &ParsedIntent{
		Action:      "help",
		Currency:    "USDC",
		TargetChain: "Arc",
		Reasoning:   "Parsed via Rova Failsafe Intent Matcher",
	}

	if strings.Contains(text, "balance") || strings.Contains(text, "wallet") {
		intent.Action = "balance"
		return intent
	}

	if strings.Contains(text, "status") || strings.Contains(text, "rule") || strings.Contains(text, "watcher") {
		intent.Action = "status"
		return intent
	}

	reAmount := regexp.MustCompile(`\$?\b(\d+(\.\d+)?)\b`)
	if match := reAmount.FindStringSubmatch(text); len(match) > 1 {
		if amt, err := strconv.ParseFloat(match[1], 64); err == nil {
			intent.Amount = amt
		}
	}

	reAddr := regexp.MustCompile(`0x[a-fA-F0-9]{40}`)
	if addr := reAddr.FindString(input); addr != "" {
		intent.Recipient = addr
	} else {
		intent.Recipient = "0x71C7656EC7ab88b098defB751B7401B5f6d8976F"
	}

	if strings.Contains(text, "swap") || strings.Contains(text, "eurc") {
		intent.Action = "swap"
		if intent.Amount == 0 {
			intent.Amount = 100
		}
		return intent
	}

	if strings.Contains(text, "bridge") || strings.Contains(text, "cctp") {
		intent.Action = "bridge"
		intent.SourceChain = "Ethereum"
		if intent.Amount == 0 {
			intent.Amount = 200
		}
		return intent
	}

	if strings.Contains(text, "send") || strings.Contains(text, "transfer") || intent.Amount > 0 {
		intent.Action = "send"
		if intent.Amount == 0 {
			intent.Amount = 50
		}
		return intent
	}

	return intent
}

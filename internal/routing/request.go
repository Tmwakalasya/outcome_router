package routing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/outcome-router/outcome-router/internal/domain"
)

const maxRequestBytes = 32 << 20

type Request struct {
	Raw            map[string]json.RawMessage
	Metadata       domain.RoutingMetadata
	Features       domain.Features
	RequestedModel string
	Stream         bool
}

func ParseRequest(body []byte) (*Request, error) {
	if len(body) == 0 {
		return nil, errors.New("request body is empty")
	}
	if len(body) > maxRequestBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxRequestBytes)
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode request: %w", err)
	}

	var messages []map[string]json.RawMessage
	if value, ok := raw["messages"]; !ok {
		return nil, errors.New("messages is required")
	} else if err := json.Unmarshal(value, &messages); err != nil || len(messages) == 0 {
		return nil, errors.New("messages must be a non-empty array")
	}

	var metadata domain.RoutingMetadata
	if value, ok := raw["routing"]; ok {
		if err := json.Unmarshal(value, &metadata); err != nil {
			return nil, fmt.Errorf("decode routing metadata: %w", err)
		}
		delete(raw, "routing")
	}
	if metadata.Workflow == "" {
		metadata.Workflow = "default"
	}
	if metadata.Step == "" {
		metadata.Step = "response"
	}
	if metadata.RiskClass == "" {
		metadata.RiskClass = domain.RiskNormal
	}

	request := &Request{Raw: raw, Metadata: metadata}
	_ = json.Unmarshal(raw["model"], &request.RequestedModel)
	_ = json.Unmarshal(raw["stream"], &request.Stream)

	features := domain.Features{
		MessageCount:         len(messages),
		ExpectedOutputTokens: 512,
		Risk:                 riskValue(metadata.RiskClass),
	}
	for _, message := range messages {
		features.InputTokens += estimateContentTokens(message["content"], &features.HasVision)
		if name := message["name"]; len(name) > 0 {
			features.InputTokens += len(name) / 4
		}
	}
	if value, ok := raw["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(value, &tools) == nil {
			features.ToolCount = len(tools)
			features.InputTokens += len(value) / 4
		}
	}
	if _, ok := raw["response_format"]; ok {
		features.HasStructuredOutput = true
	}
	if value, ok := raw["max_completion_tokens"]; ok {
		_ = json.Unmarshal(value, &features.ExpectedOutputTokens)
	} else if value, ok := raw["max_tokens"]; ok {
		_ = json.Unmarshal(value, &features.ExpectedOutputTokens)
	}
	if features.ExpectedOutputTokens <= 0 {
		features.ExpectedOutputTokens = 512
	}
	features.InputTokens = int(math.Max(1, float64(features.InputTokens+8*len(messages))))
	request.Features = features
	return request, nil
}

func (r *Request) BodyForModel(upstreamModel string, stream *bool) ([]byte, error) {
	body := make(map[string]json.RawMessage, len(r.Raw))
	for key, value := range r.Raw {
		body[key] = value
	}
	modelJSON, _ := json.Marshal(upstreamModel)
	body["model"] = modelJSON
	if stream != nil {
		streamJSON, _ := json.Marshal(*stream)
		body["stream"] = streamJSON
	}
	return json.Marshal(body)
}

func estimateContentTokens(raw json.RawMessage, hasVision *bool) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return max(1, len([]rune(text))/4)
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return len(raw) / 4
	}
	tokens := 0
	for _, part := range parts {
		var kind string
		_ = json.Unmarshal(part["type"], &kind)
		switch strings.ToLower(kind) {
		case "text", "input_text":
			var value string
			_ = json.Unmarshal(part["text"], &value)
			tokens += max(1, len([]rune(value))/4)
		case "image_url", "input_image":
			*hasVision = true
			tokens += 85
		default:
			tokens += len(part["text"]) / 4
		}
	}
	return tokens
}

func riskValue(risk domain.RiskClass) float64 {
	switch risk {
	case domain.RiskLow:
		return 0
	case domain.RiskHigh:
		return 0.75
	case domain.RiskRegulated:
		return 1
	default:
		return 0.35
	}
}

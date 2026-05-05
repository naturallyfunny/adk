// Package openai implements the adkmodel.LLM interface for OpenAI-compatible models.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strings"

	oai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"google.golang.org/genai"

	adkmodel "google.golang.org/adk/model"
)

// Config holds the configuration for the OpenAI model wrapper.
type Config struct {
	// APIKey is the OpenAI API key. If empty, the OPENAI_API_KEY environment
	// variable is used automatically by the underlying client.
	APIKey string
	// BaseURL overrides the default OpenAI API base URL. Useful for
	// OpenAI-compatible third-party APIs (e.g. Azure OpenAI, local LLMs).
	BaseURL string
}

type model struct {
	client oai.Client
	name   string
}

// NewModel returns an [adkmodel.LLM] backed by the OpenAI Chat Completions API.
//
// modelName specifies the model to use (e.g. "gpt-4o", "gpt-4o-mini").
// The Config.APIKey is passed to the underlying client; if empty, the client
// falls back to the OPENAI_API_KEY environment variable.
func NewModel(modelName string, cfg Config) adkmodel.LLM {
	opts := make([]option.RequestOption, 0, 2)
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &model{
		client: oai.NewClient(opts...),
		name:   modelName,
	}
}

// Name returns the model name supplied at construction time.
func (m *model) Name() string {
	return m.name
}

// GenerateContent implements [adkmodel.LLM] by calling the OpenAI Chat
// Completions API.  When stream is true it streams individual text deltas
// followed by one final, fully assembled response.
func (m *model) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	modelName := req.Model
	if modelName == "" {
		modelName = m.name
	}
	params, err := m.buildParams(modelName, req)
	if err != nil {
		return func(yield func(*adkmodel.LLMResponse, error) bool) {
			yield(nil, err)
		}
	}
	if stream {
		return m.generateStream(ctx, params)
	}
	return m.generate(ctx, params)
}

// buildParams assembles the OpenAI request parameters from the ADK request.
func (m *model) buildParams(modelName string, req *adkmodel.LLMRequest) (oai.ChatCompletionNewParams, error) {
	var msgs []oai.ChatCompletionMessageParamUnion

	// System instruction is prepended as a system-role message.
	if req.Config != nil && req.Config.SystemInstruction != nil {
		if text := extractText(req.Config.SystemInstruction); text != "" {
			msgs = append(msgs, oai.SystemMessage(text))
		}
	}

	contentMsgs, err := contentsToMessages(req.Contents)
	if err != nil {
		return oai.ChatCompletionNewParams{}, err
	}
	msgs = append(msgs, contentMsgs...)

	params := oai.ChatCompletionNewParams{
		Model:    shared.ChatModel(modelName),
		Messages: msgs,
	}

	if req.Config == nil {
		return params, nil
	}

	if req.Config.Temperature != nil {
		params.Temperature = oai.Float(float64(*req.Config.Temperature))
	}
	if req.Config.MaxOutputTokens != 0 {
		params.MaxCompletionTokens = oai.Int(int64(req.Config.MaxOutputTokens))
	}
	if req.Config.TopP != nil {
		params.TopP = oai.Float(float64(*req.Config.TopP))
	}
	if req.Config.FrequencyPenalty != nil {
		params.FrequencyPenalty = oai.Float(float64(*req.Config.FrequencyPenalty))
	}
	if req.Config.PresencePenalty != nil {
		params.PresencePenalty = oai.Float(float64(*req.Config.PresencePenalty))
	}
	if req.Config.Seed != nil {
		params.Seed = oai.Int(int64(*req.Config.Seed))
	}
	if len(req.Config.StopSequences) > 0 {
		stop := req.Config.StopSequences
		if len(stop) > 4 {
			stop = slices.Clone(stop[:4]) // OpenAI accepts at most 4 stop sequences.
		}
		params.Stop.OfStringArray = stop
	}
	tools, err := declarationsToTools(req.Config.Tools)
	if err != nil {
		return oai.ChatCompletionNewParams{}, err
	}
	if len(tools) > 0 {
		params.Tools = tools
	}

	return params, nil
}

// generate performs a non-streaming Chat Completions call.
func (m *model) generate(ctx context.Context, params oai.ChatCompletionNewParams) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		resp, err := m.client.Chat.Completions.New(ctx, params)
		if err != nil {
			yield(nil, fmt.Errorf("openai: generate content: %w", err))
			return
		}
		if len(resp.Choices) == 0 {
			yield(nil, fmt.Errorf("openai: empty response"))
			return
		}
		llmResp, err := completionToLLMResponse(resp)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(llmResp, nil)
	}
}

// generateStream performs a streaming Chat Completions call.
//
// Each text delta is yielded as a Partial response so the caller can display
// incremental output.  After the stream ends a single final, non-Partial
// response carrying the fully assembled content is yielded.
func (m *model) generateStream(ctx context.Context, params oai.ChatCompletionNewParams) iter.Seq2[*adkmodel.LLMResponse, error] {
	params.StreamOptions = oai.ChatCompletionStreamOptionsParam{
		IncludeUsage: oai.Bool(true),
	}
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		stream := m.client.Chat.Completions.NewStreaming(ctx, params)
		defer stream.Close()

		var (
			textBuf      strings.Builder
			toolAccums   = make(map[int64]*toolCallAccum)
			finishReason string
			modelVersion string
			usage        oai.CompletionUsage
		)

		for stream.Next() {
			chunk := stream.Current()
			if modelVersion == "" {
				modelVersion = chunk.Model
			}
			if chunk.Usage.TotalTokens > 0 {
				usage = chunk.Usage
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			choice := chunk.Choices[0]
			delta := choice.Delta

			if delta.Content != "" {
				textBuf.WriteString(delta.Content)
				if !yield(&adkmodel.LLMResponse{
					Content: &genai.Content{
						Role:  genai.RoleModel,
						Parts: []*genai.Part{{Text: delta.Content}},
					},
					Partial: true,
				}, nil) {
					return
				}
			}

			for _, tc := range delta.ToolCalls {
				acc := toolAccums[tc.Index]
				if acc == nil {
					acc = &toolCallAccum{}
					toolAccums[tc.Index] = acc
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.argsJSON += tc.Function.Arguments
			}

			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}

		if err := stream.Err(); err != nil {
			yield(nil, fmt.Errorf("openai: stream: %w", err))
			return
		}

		final, err := buildFinalStreamResponse(textBuf.String(), toolAccums, finishReason, modelVersion, usage)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(final, nil)
	}
}

// toolCallAccum accumulates streaming tool-call fragments keyed by index.
type toolCallAccum struct {
	id       string
	name     string
	argsJSON string
}

// buildFinalStreamResponse assembles the complete, non-Partial LLMResponse
// after all streaming chunks have been processed.
//
// ADK requires that text and function calls never coexist in the same Content.
// When tool calls are present the buffered text is dropped; text is only
// included when there are no tool calls.
func buildFinalStreamResponse(text string, toolAccums map[int64]*toolCallAccum, finishReason, modelVersion string, usage oai.CompletionUsage) (*adkmodel.LLMResponse, error) {
	indices := make([]int64, 0, len(toolAccums))
	for idx := range toolAccums {
		indices = append(indices, idx)
	}
	slices.Sort(indices)

	var parts []*genai.Part
	for _, idx := range indices {
		acc := toolAccums[idx]
		args, err := unmarshalArgs(acc.argsJSON)
		if err != nil {
			return nil, err
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   acc.id,
				Name: acc.name,
				Args: args,
			},
		})
	}

	if len(parts) == 0 && text != "" {
		parts = append(parts, &genai.Part{Text: text})
	}

	var content *genai.Content
	if len(parts) > 0 {
		content = &genai.Content{Role: genai.RoleModel, Parts: parts}
	}

	resp := &adkmodel.LLMResponse{
		Content:      content,
		FinishReason: mapFinishReason(finishReason),
		ModelVersion: modelVersion,
		TurnComplete: true,
	}
	if usage.TotalTokens > 0 {
		resp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(usage.PromptTokens),
			CandidatesTokenCount: int32(usage.CompletionTokens),
			TotalTokenCount:      int32(usage.TotalTokens),
		}
	}
	return resp, nil
}

// contentsToMessages converts the ADK conversation history to OpenAI messages.
//
// A single genai user Content may expand into multiple OpenAI messages because
// OpenAI requires each function response to be its own tool-role message.
func contentsToMessages(contents []*genai.Content) ([]oai.ChatCompletionMessageParamUnion, error) {
	var msgs []oai.ChatCompletionMessageParamUnion
	for _, c := range contents {
		if c == nil {
			continue
		}
		switch c.Role {
		case genai.RoleUser:
			userMsgs, err := userContentToMessages(c)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, userMsgs...)
		case genai.RoleModel:
			msgs = append(msgs, modelContentToMessage(c))
		default:
			// Unknown roles are silently skipped. Future roles added to genai
			// will not cause a hard failure but will not be mapped either.
		}
	}
	return msgs, nil
}

// userContentToMessages converts a user-role genai.Content into one or more
// OpenAI messages.  Text parts are coalesced into a single user message.
// FunctionResponse parts each become a separate tool-role message, with any
// preceding text flushed first.
func userContentToMessages(c *genai.Content) ([]oai.ChatCompletionMessageParamUnion, error) {
	var msgs []oai.ChatCompletionMessageParamUnion
	var textBuf strings.Builder

	flushText := func() {
		if textBuf.Len() > 0 {
			msgs = append(msgs, oai.UserMessage(textBuf.String()))
			textBuf.Reset()
		}
	}

	for _, part := range c.Parts {
		if part == nil {
			continue
		}
		switch {
		case part.FunctionResponse != nil:
			flushText()
			msgs = append(msgs, functionResponseToToolMessage(part.FunctionResponse))
		case part.Text != "":
			textBuf.WriteString(part.Text)
		default:
			return nil, fmt.Errorf("openai: unsupported part type in user content")
		}
	}
	flushText()
	return msgs, nil
}

// functionResponseToToolMessage converts a genai FunctionResponse to an
// OpenAI tool-role message.
func functionResponseToToolMessage(fr *genai.FunctionResponse) oai.ChatCompletionMessageParamUnion {
	toolCallID := fr.ID
	if toolCallID == "" {
		// Fall back to the function name so the message is still syntactically
		// valid; the upstream ADK flow always populates IDs in practice.
		toolCallID = fr.Name
	}

	var responseContent string
	if fr.Response != nil {
		b, err := json.Marshal(fr.Response)
		if err == nil {
			responseContent = string(b)
		}
	}
	if responseContent == "" {
		responseContent = "{}"
	}

	return oai.ToolMessage(responseContent, toolCallID)
}

// modelContentToMessage converts a model-role genai.Content to an OpenAI
// assistant message.  Text and function calls may coexist in a single message.
func modelContentToMessage(c *genai.Content) oai.ChatCompletionMessageParamUnion {
	var textBuf strings.Builder
	var toolCalls []oai.ChatCompletionMessageToolCallParam

	for _, part := range c.Parts {
		if part == nil {
			continue
		}
		if part.Text != "" {
			textBuf.WriteString(part.Text)
		} else if part.FunctionCall != nil {
			b, _ := json.Marshal(part.FunctionCall.Args)
			toolCalls = append(toolCalls, oai.ChatCompletionMessageToolCallParam{
				ID: part.FunctionCall.ID,
				Function: oai.ChatCompletionMessageToolCallFunctionParam{
					Name:      part.FunctionCall.Name,
					Arguments: string(b),
				},
			})
		}
	}

	asst := oai.ChatCompletionAssistantMessageParam{}
	if text := textBuf.String(); text != "" {
		asst.Content.OfString = oai.String(text)
	}
	if len(toolCalls) > 0 {
		asst.ToolCalls = toolCalls
	}
	return oai.ChatCompletionMessageParamUnion{OfAssistant: &asst}
}

// declarationsToTools converts genai tool definitions to OpenAI tool params.
func declarationsToTools(tools []*genai.Tool) ([]oai.ChatCompletionToolParam, error) {
	var result []oai.ChatCompletionToolParam
	for _, t := range tools {
		if t == nil {
			continue
		}
		for _, fd := range t.FunctionDeclarations {
			if fd == nil {
				continue
			}
			toolParam := oai.ChatCompletionToolParam{
				Function: shared.FunctionDefinitionParam{Name: fd.Name},
			}
			if fd.Description != "" {
				toolParam.Function.Description = oai.String(fd.Description)
			}
			if fd.Parameters != nil {
				fnParams, err := schemaToFunctionParams(fd.Parameters)
				if err != nil {
					return nil, err
				}
				toolParam.Function.Parameters = fnParams
			}
			result = append(result, toolParam)
		}
	}
	return result, nil
}

// schemaToFunctionParams converts a genai Schema to the OpenAI FunctionParameters
// map by round-tripping through JSON.
func schemaToFunctionParams(s *genai.Schema) (shared.FunctionParameters, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return shared.FunctionParameters{}, fmt.Errorf("openai: marshal schema: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return shared.FunctionParameters{}, fmt.Errorf("openai: unmarshal schema: %w", err)
	}
	return shared.FunctionParameters(m), nil
}

// completionToLLMResponse converts an OpenAI ChatCompletion to adkmodel.LLMResponse.
func completionToLLMResponse(resp *oai.ChatCompletion) (*adkmodel.LLMResponse, error) {
	choice := resp.Choices[0]
	content, err := completionMessageToContent(&choice.Message)
	if err != nil {
		return nil, err
	}
	return &adkmodel.LLMResponse{
		Content:      content,
		FinishReason: mapFinishReason(choice.FinishReason),
		ModelVersion: resp.Model,
		TurnComplete: true,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(resp.Usage.PromptTokens),
			CandidatesTokenCount: int32(resp.Usage.CompletionTokens),
			TotalTokenCount:      int32(resp.Usage.TotalTokens),
		},
	}, nil
}

// completionMessageToContent converts an OpenAI assistant message to a
// genai.Content with the model role.
//
// ADK requires that text and function calls never coexist in the same Content.
// When tool calls are present the text is dropped; text is only included when
// there are no tool calls.
func completionMessageToContent(msg *oai.ChatCompletionMessage) (*genai.Content, error) {
	var parts []*genai.Part

	for _, tc := range msg.ToolCalls {
		args, err := unmarshalArgs(tc.Function.Arguments)
		if err != nil {
			return nil, err
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			},
		})
	}

	if len(parts) == 0 && msg.Content != "" {
		parts = append(parts, &genai.Part{Text: msg.Content})
	}

	if len(parts) == 0 {
		return nil, nil
	}
	return &genai.Content{Role: genai.RoleModel, Parts: parts}, nil
}

// unmarshalArgs parses a JSON tool-call arguments string into a map.
// An empty string is treated as having no arguments.
func unmarshalArgs(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("openai: unmarshal tool args: %w", err)
	}
	return args, nil
}

// extractText concatenates the Text field of every part in a genai.Content.
func extractText(c *genai.Content) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range c.Parts {
		if part != nil {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// mapFinishReason maps an OpenAI finish-reason string to a genai.FinishReason.
func mapFinishReason(r string) genai.FinishReason {
	switch r {
	case "stop":
		return genai.FinishReasonStop
	case "length":
		return genai.FinishReasonMaxTokens
	case "content_filter":
		return genai.FinishReasonSafety
	case "tool_calls":
		// OpenAI uses "tool_calls" when the model wants to invoke a function.
		// We map this to Stop because the ADK caller inspects Content.Parts
		// for FunctionCall parts to determine if tool execution is needed.
		// There is no dedicated FinishReasonToolCall in genai.FinishReason.
		return genai.FinishReasonStop
	default:
		return genai.FinishReasonUnspecified
	}
}

var _ adkmodel.LLM = (*model)(nil)

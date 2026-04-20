package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"time"

	adapters "github.com/colony-2/colony2/server/llm/adapters"
)

func main() {
	fmt.Println("=== Tool Calling Example ===")
	if err := toolCallingExample(); err != nil {
		log.Fatal(err)
	}
}

func toolCallingExample() error {
	// Create adapter (OpenAI has the best tool support)
	adapter, err := adapters.NewOpenAIAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Define available tools
	tools := []adapters.Tool{
		{
			Name:        "get_weather",
			Description: "Get the current weather for a specific location",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"location": {
						"type": "string",
						"description": "The city and state, e.g. San Francisco, CA"
					},
					"unit": {
						"type": "string",
						"enum": ["celsius", "fahrenheit"],
						"default": "celsius"
					}
				},
				"required": ["location"]
			}`),
		},
		{
			Name:        "calculate",
			Description: "Perform mathematical calculations",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"expression": {
						"type": "string",
						"description": "The mathematical expression to evaluate"
					}
				},
				"required": ["expression"]
			}`),
		},
		{
			Name:        "search_web",
			Description: "Search the web for information",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "The search query"
					},
					"num_results": {
						"type": "integer",
						"description": "Number of results to return",
						"default": 5
					}
				},
				"required": ["query"]
			}`),
		},
	}

	// Configure generation
	config := adapters.Config{
		Model:        "gpt-4",
		Temperature:  0.7,
		MaxTokens:    500,
		SystemPrompt: "You are a helpful assistant with access to various tools. Use them when appropriate to answer user questions.",
	}

	// Test prompts that should trigger tool use
	prompts := []string{
		"What's the weather like in Tokyo, Japan?",
		"Calculate the result of 15 * 37 + 289",
		"Search for information about the James Webb Space Telescope",
		"What's the weather in London and what's 25 celsius in fahrenheit?",
	}

	ctx := context.Background()

	for _, prompt := range prompts {
		fmt.Printf("\n--- Prompt: %s ---\n", prompt)

		// Generate with tools
		response, err := adapter.GenerateWithTools(ctx, prompt, tools, config)
		if err != nil {
			return fmt.Errorf("generation failed: %w", err)
		}

		// Print assistant's message
		if response.Content != "" {
			fmt.Printf("Assistant: %s\n", response.Content)
		}

		// Process tool calls
		for _, toolCall := range response.ToolCalls {
			fmt.Printf("\nTool Call: %s (ID: %s)\n", toolCall.Name, toolCall.ID)
			fmt.Printf("Arguments: %s\n", string(toolCall.Arguments))

			// Execute the tool (simulated)
			result, err := executeToolCall(toolCall)
			if err != nil {
				fmt.Printf("Tool Error: %v\n", err)
				continue
			}

			fmt.Printf("Tool Result: %s\n", result)

			// You would typically send the tool result back to the LLM
			// for it to formulate a final response
		}
	}

	return nil
}

// executeToolCall simulates executing a tool call
func executeToolCall(toolCall adapters.ToolCall) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal(toolCall.Arguments, &args); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	switch toolCall.Name {
	case "get_weather":
		location, _ := args["location"].(string)
		unit, _ := args["unit"].(string)
		if unit == "" {
			unit = "celsius"
		}

		// Simulate weather data
		temp := rand.Intn(35) + 5
		conditions := []string{"sunny", "cloudy", "rainy", "partly cloudy"}
		condition := conditions[rand.Intn(len(conditions))]

		return fmt.Sprintf(`{"temperature": %d, "unit": "%s", "condition": "%s", "location": "%s"}`,
			temp, unit, condition, location), nil

	case "calculate":
		expression, _ := args["expression"].(string)
		// In real implementation, use a proper expression evaluator
		return fmt.Sprintf(`{"expression": "%s", "result": "542", "note": "simulated calculation"}`, expression), nil

	case "search_web":
		query, _ := args["query"].(string)
		numResults := 5
		if n, ok := args["num_results"].(float64); ok {
			numResults = int(n)
		}

		// Simulate search results
		results := []map[string]string{
			{
				"title":   "James Webb Space Telescope - NASA",
				"url":     "https://www.nasa.gov/webb",
				"snippet": "The James Webb Space Telescope is NASA's largest and most powerful space telescope...",
			},
			{
				"title":   "Webb Telescope Images - Latest Discoveries",
				"url":     "https://webbtelescope.org/images",
				"snippet": "Stunning images from the James Webb Space Telescope revealing the early universe...",
			},
		}

		data, _ := json.Marshal(map[string]interface{}{
			"query":   query,
			"results": results[:min(numResults, len(results))],
		})
		return string(data), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", toolCall.Name)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

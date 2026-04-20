package main

import (
	"context"
	"fmt"
	"log"
	"os"

	adapters "github.com/colony-2/colony2/server/llm/adapters"
)

func main() {
	// Example 1: Basic OpenAI usage
	fmt.Println("=== OpenAI Example ===")
	if err := openAIExample(); err != nil {
		log.Printf("OpenAI error: %v", err)
	}

	// Example 2: Anthropic usage
	fmt.Println("\n=== Anthropic Example ===")
	if err := anthropicExample(); err != nil {
		log.Printf("Anthropic error: %v", err)
	}

	// Example 3: Gemini usage
	fmt.Println("\n=== Gemini Example ===")
	if err := geminiExample(); err != nil {
		log.Printf("Gemini error: %v", err)
	}

	// Example 4: Registry usage
	fmt.Println("\n=== Registry Example ===")
	if err := registryExample(); err != nil {
		log.Printf("Registry error: %v", err)
	}
}

func openAIExample() error {
	// Create adapter (API key from environment)
	adapter, err := adapters.NewOpenAIAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Configure generation
	config := adapters.Config{
		Model:        "gpt-3.5-turbo",
		Temperature:  0.7,
		MaxTokens:    150,
		SystemPrompt: "You are a helpful assistant. Keep responses concise.",
	}

	// Generate response
	ctx := context.Background()
	response, err := adapter.Generate(
		ctx,
		"What are the three main benefits of using Go for backend development?",
		config,
	)
	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	fmt.Printf("Response: %s\n", response.Content)
	fmt.Printf("Tokens used: %d\n", response.Usage.TotalTokens)

	return nil
}

func anthropicExample() error {
	// Create adapter
	adapter, err := adapters.NewAnthropicAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Configure for Claude
	config := adapters.Config{
		Model:       "claude-3-haiku",
		Temperature: 0.5,
		MaxTokens:   200,
	}

	// Generate haiku
	ctx := context.Background()
	response, err := adapter.Generate(
		ctx,
		"Write a haiku about programming in Go",
		config,
	)
	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	fmt.Printf("Haiku:\n%s\n", response.Content)

	return nil
}

func geminiExample() error {
	// Create adapter
	adapter, err := adapters.NewGeminiAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Configure for Gemini
	config := adapters.Config{
		Model:       "gemini-1.5-flash",
		Temperature: 0.7,
		MaxTokens:   200,
	}

	// Generate response
	ctx := context.Background()
	response, err := adapter.Generate(
		ctx,
		"Explain the concept of goroutines in Go in simple terms",
		config,
	)
	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	fmt.Printf("Response: %s\n", response.Content)

	return nil
}

func registryExample() error {
	// Create registry
	registry := adapters.NewRegistry()

	// Register available adapters
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		if adapter, err := adapters.NewOpenAIAdapter(apiKey); err == nil {
			registry.Register("openai", adapter)
		}
	}

	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		if adapter, err := adapters.NewAnthropicAdapter(apiKey); err == nil {
			registry.Register("anthropic", adapter)
		}
	}

	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		if adapter, err := adapters.NewGeminiAdapter(apiKey); err == nil {
			registry.Register("gemini", adapter)
		}
	}

	// List registered adapters
	fmt.Printf("Registered adapters: %v\n", registry.List())

	// Use adapter from registry
	if adapter, err := registry.Get("openai"); err == nil {
		config := adapters.Config{
			Model:     "gpt-3.5-turbo",
			MaxTokens: 50,
		}

		ctx := context.Background()
		response, err := adapter.Generate(ctx, "Say hello!", config)
		if err != nil {
			return err
		}

		fmt.Printf("Registry response: %s\n", response.Content)
	}

	return nil
}

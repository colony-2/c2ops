package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	adapters "github.com/colony-2/colony2/server/llm/adapters"
)

func main() {
	fmt.Println("=== Error Handling Examples ===")

	// Create adapter
	adapter, err := adapters.NewOpenAIAdapter("")
	if err != nil {
		// Handle missing API key
		if errors.Is(err, adapters.ErrAPIKeyMissing) {
			fmt.Println("Please set OPENAI_API_KEY environment variable")
			return
		}
		log.Fatal(err)
	}

	ctx := context.Background()

	// Example 1: Handle rate limiting
	fmt.Println("\n1. Rate Limiting Example")
	handleRateLimiting(ctx, adapter)

	// Example 2: Handle context length
	fmt.Println("\n2. Context Length Example")
	handleContextLength(ctx, adapter)

	// Example 3: Handle invalid configuration
	fmt.Println("\n3. Invalid Configuration Example")
	handleInvalidConfig(ctx, adapter)

	// Example 4: Handle model errors
	fmt.Println("\n4. Model Error Example")
	handleModelError(ctx, adapter)

	// Example 5: Retry with backoff
	fmt.Println("\n5. Retry with Backoff Example")
	retryWithBackoff(ctx, adapter)
}

func handleRateLimiting(ctx context.Context, adapter adapters.Adapter) {
	config := adapters.Config{
		Model:     "gpt-3.5-turbo",
		MaxTokens: 50,
	}

	// Simulate multiple rapid requests
	for i := 0; i < 3; i++ {
		response, err := adapter.Generate(ctx, fmt.Sprintf("Count to %d", i+1), config)
		if err != nil {
			if errors.Is(err, adapters.ErrRateLimitExceeded) {
				fmt.Printf("Rate limit hit on request %d. Waiting...\n", i+1)
				time.Sleep(5 * time.Second)
				// Retry
				response, err = adapter.Generate(ctx, fmt.Sprintf("Count to %d", i+1), config)
			}

			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}
		}

		fmt.Printf("Response %d: %s\n", i+1, truncate(response.Content, 50))
	}
}

func handleContextLength(ctx context.Context, adapter adapters.Adapter) {
	// Create a very long prompt
	longPrompt := strings.Repeat("This is a very long text. ", 10000)

	config := adapters.Config{
		Model:     "gpt-3.5-turbo",
		MaxTokens: 100,
	}

	_, err := adapter.Generate(ctx, longPrompt, config)
	if err != nil {
		if errors.Is(err, adapters.ErrContextLengthExceeded) {
			fmt.Println("Context too long! Reducing prompt size...")

			// Retry with shorter prompt
			shortPrompt := "Summarize: " + longPrompt[:1000] + "..."
			response, err := adapter.Generate(ctx, shortPrompt, config)
			if err != nil {
				fmt.Printf("Still failed: %v\n", err)
			} else {
				fmt.Printf("Success with shorter prompt: %s\n", truncate(response.Content, 50))
			}
		} else {
			fmt.Printf("Unexpected error: %v\n", err)
		}
	}
}

func handleInvalidConfig(ctx context.Context, adapter adapters.Adapter) {
	// Invalid temperature
	config := adapters.Config{
		Model:       "gpt-3.5-turbo",
		Temperature: 3.0, // Too high
		MaxTokens:   50,
	}

	_, err := adapter.Generate(ctx, "Hello", config)
	if err != nil {
		if errors.Is(err, adapters.ErrInvalidConfig) {
			fmt.Println("Invalid config detected! Fixing...")

			// Fix configuration
			config.Temperature = 0.7
			response, err := adapter.Generate(ctx, "Hello", config)
			if err != nil {
				fmt.Printf("Still failed: %v\n", err)
			} else {
				fmt.Printf("Success with fixed config: %s\n", response.Content)
			}
		}
	}
}

func handleModelError(ctx context.Context, adapter adapters.Adapter) {
	// Try to use a non-existent model
	config := adapters.Config{
		Model:     "gpt-5-ultra", // Doesn't exist
		MaxTokens: 50,
	}

	_, err := adapter.Generate(ctx, "Hello", config)
	if err != nil {
		if errors.Is(err, adapters.ErrModelNotSupported) {
			fmt.Println("Model not supported! Falling back...")

			// Fallback to a known model
			config.Model = "gpt-3.5-turbo"
			response, err := adapter.Generate(ctx, "Hello", config)
			if err != nil {
				fmt.Printf("Fallback failed: %v\n", err)
			} else {
				fmt.Printf("Success with fallback model: %s\n", response.Content)
			}
		} else {
			fmt.Printf("Error: %v\n", err)
		}
	}
}

func retryWithBackoff(ctx context.Context, adapter adapters.Adapter) {
	config := adapters.Config{
		Model:     "gpt-3.5-turbo",
		MaxTokens: 100,
	}

	prompt := "Explain exponential backoff"
	maxRetries := 3
	baseDelay := time.Second

	var response adapters.Response
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		response, err = adapter.Generate(ctx, prompt, config)

		if err == nil {
			fmt.Printf("Success on attempt %d\n", attempt+1)
			fmt.Printf("Response: %s\n", truncate(response.Content, 100))
			break
		}

		// Check if error is retryable
		if isRetryable(err) && attempt < maxRetries {
			delay := baseDelay * time.Duration(1<<uint(attempt)) // Exponential backoff
			fmt.Printf("Attempt %d failed: %v. Retrying in %v...\n", attempt+1, err, delay)
			time.Sleep(delay)
			continue
		}

		// Non-retryable error or max retries reached
		fmt.Printf("Failed after %d attempts: %v\n", attempt+1, err)
		break
	}
}

func isRetryable(err error) bool {
	// Define which errors are retryable
	retryableErrors := []error{
		adapters.ErrRateLimitExceeded,
		context.DeadlineExceeded,
	}

	for _, retryable := range retryableErrors {
		if errors.Is(err, retryable) {
			return true
		}
	}

	// Also retry on network errors (connection refused, timeout, etc.)
	errStr := err.Error()
	networkErrors := []string{
		"connection refused",
		"timeout",
		"temporary failure",
		"no such host",
	}

	for _, netErr := range networkErrors {
		if strings.Contains(strings.ToLower(errStr), netErr) {
			return true
		}
	}

	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	adapters "github.com/colony-2/colony2/server/llm/adapters"
)

func main() {
	// Set up signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
	}()

	// Example: Streaming with OpenAI
	fmt.Println("=== Streaming Example ===")
	if err := streamingExample(ctx); err != nil {
		log.Fatal(err)
	}
}

func streamingExample(ctx context.Context) error {
	// Create adapter
	adapter, err := adapters.NewOpenAIAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Configure for streaming
	config := adapters.Config{
		Model:        "gpt-3.5-turbo",
		Temperature:  0.8,
		MaxTokens:    500,
		SystemPrompt: "You are a creative storyteller.",
	}

	// Start streaming
	prompt := "Tell me a short story about a robot learning to paint"
	fmt.Printf("Prompt: %s\n\n", prompt)
	fmt.Println("Response:")

	tokenChan, err := adapter.StreamGenerate(ctx, prompt, config)
	if err != nil {
		return fmt.Errorf("failed to start stream: %w", err)
	}

	// Process tokens as they arrive
	totalTokens := 0
	for token := range tokenChan {
		// Check for errors
		if token.Error != nil {
			return fmt.Errorf("stream error: %w", token.Error)
		}

		// Print token without newline
		fmt.Print(token.Content)
		totalTokens++

		// Check if context was cancelled
		select {
		case <-ctx.Done():
			fmt.Println("\n\nStream cancelled")
			return ctx.Err()
		default:
		}
	}

	fmt.Printf("\n\nStreaming complete. Tokens received: %d\n", totalTokens)

	return nil
}

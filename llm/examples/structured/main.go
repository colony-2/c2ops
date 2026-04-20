package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	adapters "github.com/colony-2/colony2/server/llm/adapters"
)

// Define a struct for our expected response
type PersonInfo struct {
	Name       string   `json:"name"`
	Age        int      `json:"age"`
	Email      string   `json:"email,omitempty"`
	Occupation string   `json:"occupation"`
	Hobbies    []string `json:"hobbies"`
}

func main() {
	// Example 1: Gemini with structured output
	fmt.Println("=== Gemini Structured Output Example ===")
	if err := geminiStructuredExample(); err != nil {
		log.Printf("Gemini error: %v", err)
	}

	// Example 2: OpenAI with structured output (for comparison)
	fmt.Println("\n=== OpenAI Structured Output Example ===")
	if err := openAIStructuredExample(); err != nil {
		log.Printf("OpenAI error: %v", err)
	}
}

func geminiStructuredExample() error {
	// Create adapter
	adapter, err := adapters.NewGeminiAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// Define the JSON schema for structured output
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "The person's full name"
			},
			"age": {
				"type": "integer",
				"description": "The person's age in years"
			},
			"email": {
				"type": "string",
				"format": "email",
				"description": "The person's email address"
			},
			"occupation": {
				"type": "string",
				"description": "The person's job or profession"
			},
			"hobbies": {
				"type": "array",
				"items": {
					"type": "string"
				},
				"description": "List of the person's hobbies"
			}
		},
		"required": ["name", "age", "occupation", "hobbies"]
	}`)

	// Configure for structured output
	config := adapters.Config{
		Model:          "gemini-1.5-flash",
		Temperature:    0.3, // Lower temperature for more consistent structured output
		MaxTokens:      200,
		ResponseFormat: "json",
		ResponseSchema: schema,
		SystemPrompt:   "You are a helpful assistant that generates person profiles in JSON format.",
	}

	// Generate structured response
	ctx := context.Background()
	response, err := adapter.Generate(
		ctx,
		"Generate a profile for a software engineer who enjoys outdoor activities. Make it realistic.",
		config,
	)
	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	// Parse the structured response
	var person PersonInfo
	if err := json.Unmarshal([]byte(response.Content), &person); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	// Display the result
	fmt.Printf("Generated Profile:\n")
	fmt.Printf("  Name: %s\n", person.Name)
	fmt.Printf("  Age: %d\n", person.Age)
	fmt.Printf("  Email: %s\n", person.Email)
	fmt.Printf("  Occupation: %s\n", person.Occupation)
	fmt.Printf("  Hobbies: %v\n", person.Hobbies)
	fmt.Printf("\nRaw JSON:\n%s\n", response.Content)

	return nil
}

func openAIStructuredExample() error {
	// Create adapter
	adapter, err := adapters.NewOpenAIAdapter("")
	if err != nil {
		return fmt.Errorf("failed to create adapter: %w", err)
	}

	// OpenAI doesn't support response_schema in the same way,
	// but we can use response_format: "json" and prompt engineering
	config := adapters.Config{
		Model:          "gpt-3.5-turbo",
		Temperature:    0.3,
		MaxTokens:      200,
		ResponseFormat: "json",
		SystemPrompt: `You are a helpful assistant that generates person profiles in JSON format.
Always respond with a JSON object containing these fields:
- name (string): The person's full name
- age (integer): The person's age in years
- email (string): The person's email address
- occupation (string): The person's job or profession
- hobbies (array of strings): List of the person's hobbies`,
	}

	// Generate response
	ctx := context.Background()
	response, err := adapter.Generate(
		ctx,
		"Generate a profile for a software engineer who enjoys outdoor activities. Make it realistic.",
		config,
	)
	if err != nil {
		return fmt.Errorf("generation failed: %w", err)
	}

	// Parse the response
	var person PersonInfo
	if err := json.Unmarshal([]byte(response.Content), &person); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	// Display the result
	fmt.Printf("Generated Profile:\n")
	fmt.Printf("  Name: %s\n", person.Name)
	fmt.Printf("  Age: %d\n", person.Age)
	fmt.Printf("  Email: %s\n", person.Email)
	fmt.Printf("  Occupation: %s\n", person.Occupation)
	fmt.Printf("  Hobbies: %v\n", person.Hobbies)
	fmt.Printf("\nRaw JSON:\n%s\n", response.Content)

	return nil
}

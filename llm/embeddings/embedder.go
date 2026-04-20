package embeddings

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"math"
)

// MockEmbedder is a mock implementation of the Embedder interface for testing
type MockEmbedder struct {
	dimension int
}

// NewMockEmbedder creates a new mock embedder with the specified dimension
func NewMockEmbedder(dimension int) *MockEmbedder {
	return &MockEmbedder{
		dimension: dimension,
	}
}

// Embed generates a mock embedding for a single text
func (m *MockEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	// Generate a deterministic embedding based on the text
	hash := md5.Sum([]byte(text))
	
	embedding := make([]float64, m.dimension)
	for i := 0; i < m.dimension; i++ {
		// Use hash bytes to generate pseudo-random values
		byteIndex := i % len(hash)
		value := float64(hash[byteIndex]) / 255.0
		
		// Normalize to [-1, 1] range
		embedding[i] = (value * 2) - 1
		
		// Add some variation based on position
		embedding[i] += math.Sin(float64(i)) * 0.1
		
		// Ensure values are in [-1, 1] range
		if embedding[i] > 1 {
			embedding[i] = 1
		} else if embedding[i] < -1 {
			embedding[i] = -1
		}
	}
	
	// Normalize the vector
	norm := 0.0
	for _, v := range embedding {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	
	if norm > 0 {
		for i := range embedding {
			embedding[i] /= norm
		}
	}
	
	return embedding, nil
}

// EmbedBatch generates mock embeddings for multiple texts
func (m *MockEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	embeddings := make([][]float64, len(texts))
	
	for i, text := range texts {
		embedding, err := m.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		embeddings[i] = embedding
	}
	
	return embeddings, nil
}

// OpenAIEmbedder is a placeholder for OpenAI embeddings
type OpenAIEmbedder struct {
	apiKey string
	model  string
}

// NewOpenAIEmbedder creates a new OpenAI embedder (mock implementation)
func NewOpenAIEmbedder(apiKey, model string) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		apiKey: apiKey,
		model:  model,
	}
}

// Embed generates an embedding using OpenAI API (mock implementation)
func (o *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	// In a real implementation, this would call the OpenAI API
	// For now, we'll use the mock embedder
	mock := NewMockEmbedder(1536) // OpenAI ada-002 dimension
	return mock.Embed(ctx, text)
}

// EmbedBatch generates embeddings for multiple texts using OpenAI API (mock implementation)
func (o *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	// In a real implementation, this would call the OpenAI API
	// For now, we'll use the mock embedder
	mock := NewMockEmbedder(1536) // OpenAI ada-002 dimension
	return mock.EmbedBatch(ctx, texts)
}

// cosineSimilarity calculates the cosine similarity between two vectors
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	
	dotProduct := 0.0
	normA := 0.0
	normB := 0.0
	
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	
	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)
	
	if normA == 0 || normB == 0 {
		return 0
	}
	
	return dotProduct / (normA * normB)
}

// uint64ToBytes converts a uint64 to a byte slice
func uint64ToBytes(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}
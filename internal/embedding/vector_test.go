package embedding

import (
	"math"
	"testing"
)

func TestEncodeDecodeVectorRoundTrip(t *testing.T) {
	blob, err := EncodeVector([]float32{1, 2, 3}, 3)
	if err != nil {
		t.Fatalf("EncodeVector() error = %v", err)
	}
	vector, err := DecodeVector(blob, 3)
	if err != nil {
		t.Fatalf("DecodeVector() error = %v", err)
	}
	for i, want := range []float32{1, 2, 3} {
		if vector[i] != want {
			t.Fatalf("vector[%d] = %v, want %v", i, vector[i], want)
		}
	}
}

func TestEncodeVectorRejectsBadVectors(t *testing.T) {
	for _, vector := range [][]float32{{1, 2}, {float32(math.NaN())}, {float32(math.Inf(1))}, {0}} {
		if _, err := EncodeVector(vector, 1); err == nil {
			t.Fatalf("EncodeVector(%v) error = nil, want error", vector)
		}
	}
}

func TestDecodeVectorRejectsInvalidBlob(t *testing.T) {
	for _, blob := range [][]byte{nil, {1, 2, 3}, make([]byte, 8)} {
		if _, err := DecodeVector(blob, 3); err == nil {
			t.Fatalf("DecodeVector(%v) error = nil, want error", blob)
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	similarity, err := CosineSimilarity([]float32{1, 0}, []float32{1, 0})
	if err != nil {
		t.Fatalf("CosineSimilarity() error = %v", err)
	}
	if similarity != 1 {
		t.Fatalf("similarity = %v, want 1", similarity)
	}
	orthogonal, err := CosineSimilarity([]float32{1, 0}, []float32{0, 1})
	if err != nil {
		t.Fatalf("CosineSimilarity(orthogonal) error = %v", err)
	}
	if orthogonal != 0 {
		t.Fatalf("orthogonal similarity = %v, want 0", orthogonal)
	}
}

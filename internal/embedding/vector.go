package embedding

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

func EncodeVector(vector []float32, dimensions int) ([]byte, error) {
	if err := validateVector(vector, dimensions); err != nil {
		return nil, err
	}
	buf := bytes.NewBuffer(make([]byte, 0, len(vector)*4))
	for _, value := range vector {
		if err := binary.Write(buf, binary.LittleEndian, value); err != nil {
			return nil, fmt.Errorf("encode embedding vector: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func DecodeVector(blob []byte, dimensions int) ([]float32, error) {
	if dimensions <= 0 {
		return nil, fmt.Errorf("embedding dimensions must be > 0")
	}
	if len(blob) == 0 {
		return nil, fmt.Errorf("embedding blob is empty")
	}
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("embedding blob length is invalid")
	}
	if len(blob)/4 != dimensions {
		return nil, fmt.Errorf("embedding blob dimensions mismatch")
	}
	vector := make([]float32, dimensions)
	if err := binary.Read(bytes.NewReader(blob), binary.LittleEndian, &vector); err != nil {
		return nil, fmt.Errorf("decode embedding vector: %w", err)
	}
	if err := validateVector(vector, dimensions); err != nil {
		return nil, err
	}
	return vector, nil
}

func CosineSimilarity(a []float32, b []float32) (float64, error) {
	if len(a) == 0 || len(b) == 0 {
		return 0, fmt.Errorf("cosine similarity requires non-empty vectors")
	}
	if len(a) != len(b) {
		return 0, fmt.Errorf("cosine similarity dimensions mismatch")
	}
	var dot float64
	var normA float64
	var normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		if math.IsNaN(av) || math.IsInf(av, 0) || math.IsNaN(bv) || math.IsInf(bv, 0) {
			return 0, fmt.Errorf("cosine similarity vector contains non-finite value")
		}
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("cosine similarity requires non-zero vectors")
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), nil
}

func validateVector(vector []float32, dimensions int) error {
	if dimensions <= 0 {
		return fmt.Errorf("embedding dimensions must be > 0")
	}
	if len(vector) != dimensions {
		return fmt.Errorf("embedding vector dimensions mismatch")
	}
	var norm float64
	for _, value := range vector {
		v := float64(value)
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("embedding vector contains non-finite value")
		}
		norm += v * v
	}
	if norm == 0 {
		return fmt.Errorf("embedding vector must be non-zero")
	}
	return nil
}

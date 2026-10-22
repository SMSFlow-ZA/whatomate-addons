package knowledge

import (
	"context"
	"database/sql"
	"math"
	"testing"
)

func TestVectorProfileAndEncodingDefaultDeny(t *testing.T) {
	for _, pin := range []VectorPin{{}, {Model: "m", Dimensions: 0}, {Model: "m", Dimensions: 2001}, {Model: " ", Dimensions: 2}} {
		if _, err := NewVectorRepository(new(sql.DB), "tenant", pin); err == nil {
			t.Fatal("invalid vector profile accepted")
		}
	}
	if _, err := NewVectorRepository(nil, "tenant", VectorPin{Model: "m", Dimensions: 2}); err == nil {
		t.Fatal("nil database accepted")
	}
	for _, vector := range [][]float64{nil, {0, 0}, {math.NaN(), 1}, {math.Inf(1), 1}, {math.MaxFloat64, 1}, {1}} {
		if _, err := encodeVector(vector, 2); err == nil {
			t.Fatal("invalid embedding accepted")
		}
	}
	got, err := encodeVector([]float64{1, -0.5}, 2)
	if err != nil || got != "[1,-0.5]" {
		t.Fatalf("valid vector encoding failed: %v", err)
	}
}

func TestVectorWorkerAndReadUninitializedDeny(t *testing.T) {
	var repository VectorRepository
	if _, err := repository.IndexNext(context.Background(), "worker", nil); err == nil {
		t.Fatal("uninitialized worker accepted")
	}
	if _, err := repository.RetrieveVector(context.Background(), Query{}, nil, nil); err == nil {
		t.Fatal("uninitialized read accepted")
	}
}

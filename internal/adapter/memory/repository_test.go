package memory_test

import (
	"testing"

	"github.com/arielcr/medication-api/internal/adapter/memory"
	"github.com/arielcr/medication-api/internal/medication"
	"github.com/arielcr/medication-api/internal/medication/medicationtest"
)

func TestRepositoryContract(t *testing.T) {
	t.Parallel()

	medicationtest.RunRepositoryContract(t, func(*testing.T) medication.Repository {
		return memory.NewRepository()
	})
}

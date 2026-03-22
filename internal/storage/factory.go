package storage

import (
	"fmt"
)

// NewStorage creates a [Storage] backend based on cfg.Type.
func NewStorage(cfg Config) (Storage, error) {
	switch cfg.Type {
	case "sqlite", "":
		return NewSQLiteStorage(cfg)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.Type)
	}
}

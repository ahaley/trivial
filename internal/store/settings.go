package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const settingAIEnabled = "ai_enabled"

// AIEnabled reports whether open-ended grading may call the LLM. An absent
// row means the default: enabled.
func (s *Store) AIEnabled() (bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingAIEnabled).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("read setting %s: %w", settingAIEnabled, err)
	}
	return value == "true", nil
}

// SetAIEnabled persists the AI grading switch.
func (s *Store) SetAIEnabled(on bool) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		settingAIEnabled, strconv.FormatBool(on))
	if err != nil {
		return fmt.Errorf("write setting %s: %w", settingAIEnabled, err)
	}
	return nil
}

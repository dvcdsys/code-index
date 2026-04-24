package config

import "os"

// osUnsetenv is a tiny shim so tests can wipe env vars without importing os
// directly in config_test.go (keeps the imports there minimal and focused).
func osUnsetenv(key string) {
	_ = os.Unsetenv(key)
}

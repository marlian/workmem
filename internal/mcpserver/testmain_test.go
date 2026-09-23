package mcpserver

import (
	"os"
	"testing"
)

// TestMain isolates the suite from the developer's shell: tests spawn
// `go run` children that inherit the environment, and an exported
// MEMORY_PROJECT_MODE, MEMORY_PROJECTS_ROOT or MEMORY_DB_PATH would switch
// storage mode or point tests at a real memory database.
func TestMain(m *testing.M) {
	for _, key := range []string{"MEMORY_PROJECT_MODE", "MEMORY_PROJECTS_ROOT", "MEMORY_DB_PATH", "MEMORY_TELEMETRY_PATH"} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

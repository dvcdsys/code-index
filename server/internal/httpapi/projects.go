package httpapi

// JSON request / response types kept for tests that unmarshal handler
// responses into them. Wire-compatible with the generated openapi types in
// internal/httpapi/openapi — every JSON tag matches doc/openapi.yaml. The
// Server struct in server.go now drives the actual HTTP responses; these
// types remain only as stable test fixtures.

type projectSettingsJSON struct {
	ExcludePatterns []string `json:"exclude_patterns"`
	MaxFileSize     int      `json:"max_file_size"`
}

type projectStatsJSON struct {
	TotalFiles   int `json:"total_files"`
	IndexedFiles int `json:"indexed_files"`
	TotalChunks  int `json:"total_chunks"`
	TotalSymbols int `json:"total_symbols"`
}

type projectResponse struct {
	HostPath      string              `json:"host_path"`
	ContainerPath string              `json:"container_path"`
	Languages     []string            `json:"languages"`
	Settings      projectSettingsJSON `json:"settings"`
	Stats         projectStatsJSON    `json:"stats"`
	Status        string              `json:"status"`
	CreatedAt     string              `json:"created_at"`
	UpdatedAt     string              `json:"updated_at"`
	LastIndexedAt *string             `json:"last_indexed_at"`
}

type projectListResponse struct {
	Projects []projectResponse `json:"projects"`
	Total    int               `json:"total"`
}

type createProjectRequest struct {
	HostPath string `json:"host_path"`
}

type updateProjectRequest struct {
	Settings *projectSettingsJSON `json:"settings"`
}

// ensure the request shapes are referenced even when only read by tests.
var (
	_ = createProjectRequest{}
	_ = updateProjectRequest{}
)

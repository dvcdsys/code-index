class ProjectNotFoundError(Exception):
    def __init__(self, project_id: str):
        self.project_id = project_id
        super().__init__(f"Project not found: {project_id}")


class IndexingError(Exception):
    def __init__(self, message: str, project_id: str | None = None):
        self.project_id = project_id
        super().__init__(message)


class AuthError(Exception):
    def __init__(self, message: str = "Invalid or missing API key"):
        super().__init__(message)

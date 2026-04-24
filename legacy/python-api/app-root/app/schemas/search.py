from pydantic import BaseModel, Field


class SearchRequest(BaseModel):
    query: str
    limit: int = 10
    languages: list[str] = Field(default_factory=list)
    paths: list[str] = Field(default_factory=list)
    min_score: float = 0.1


class SymbolSearchRequest(BaseModel):
    query: str
    kinds: list[str] = Field(default_factory=list)
    limit: int = 20


class FileSearchRequest(BaseModel):
    query: str
    limit: int = 20


class SearchResultItem(BaseModel):
    file_path: str
    start_line: int
    end_line: int
    content: str
    score: float
    chunk_type: str
    symbol_name: str
    language: str


class SearchResponse(BaseModel):
    results: list[SearchResultItem]
    total: int
    query_time_ms: float


class SymbolResultItem(BaseModel):
    name: str
    kind: str
    file_path: str
    line: int
    end_line: int
    language: str
    signature: str | None = None
    parent_name: str | None = None


class SymbolSearchResponse(BaseModel):
    results: list[SymbolResultItem]
    total: int


class FileResultItem(BaseModel):
    file_path: str
    language: str | None


class FileSearchResponse(BaseModel):
    results: list[FileResultItem]
    total: int


class DefinitionRequest(BaseModel):
    symbol: str
    kind: str | None = None  # function|class|method|type
    file_path: str | None = None  # narrow to a specific file
    limit: int = 10


class DefinitionItem(BaseModel):
    name: str
    kind: str
    file_path: str
    line: int
    end_line: int
    language: str
    signature: str | None = None
    parent_name: str | None = None


class DefinitionResponse(BaseModel):
    results: list[DefinitionItem]
    total: int


class ReferenceRequest(BaseModel):
    symbol: str
    limit: int = 50
    file_path: str | None = None  # narrow to a specific file


class ReferenceItem(BaseModel):
    file_path: str
    start_line: int
    end_line: int
    content: str
    chunk_type: str
    symbol_name: str
    language: str


class ReferenceResponse(BaseModel):
    results: list[ReferenceItem]
    total: int


class ProjectSummary(BaseModel):
    host_path: str
    status: str
    languages: list[str]
    total_files: int
    total_chunks: int
    total_symbols: int
    top_directories: list[dict]
    recent_symbols: list[dict]

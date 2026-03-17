import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from .config import settings
from .core.exceptions import ProjectNotFoundError, IndexingError, AuthError
from .database import init_db, close_db
from .routers import health, projects, indexing, search

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("Starting up — initializing database...")
    await init_db()
    logger.info("Database initialized")

    logger.info("Loading embedding model: %s", settings.embedding_model)
    from .services.embeddings import embedding_service
    await embedding_service.load_model()
    logger.info("Embedding model loaded")

    yield

    logger.info("Shutting down...")
    await close_db()


app = FastAPI(
    title="Claude Code Index API",
    version="0.1.0",
    lifespan=lifespan,
)

app.include_router(health.router)
app.include_router(projects.router)
app.include_router(indexing.router)
app.include_router(search.router)


@app.exception_handler(ProjectNotFoundError)
async def project_not_found_handler(request: Request, exc: ProjectNotFoundError):
    return JSONResponse(status_code=404, content={"detail": str(exc)})


@app.exception_handler(IndexingError)
async def indexing_error_handler(request: Request, exc: IndexingError):
    return JSONResponse(status_code=500, content={"detail": str(exc)})


@app.exception_handler(AuthError)
async def auth_error_handler(request: Request, exc: AuthError):
    return JSONResponse(status_code=401, content={"detail": str(exc)})

"""Search integration tests — require running Docker container with indexed project."""
import os

import httpx
import pytest

BASE_URL = os.environ.get("CODE_INDEX_API_URL", "http://localhost:21847")
API_KEY = os.environ.get("CODE_INDEX_API_KEY", "")


@pytest.fixture
def client():
    return httpx.Client(
        base_url=BASE_URL,
        headers={"Authorization": f"Bearer {API_KEY}"},
        timeout=60.0,
    )


@pytest.fixture
def project_with_index(client):
    """Create a project, wait for indexing, return project_id. Cleanup after."""
    if not API_KEY:
        pytest.skip("API_KEY not set")

    r = client.post(
        "/api/v1/projects",
        json={"name": "test-search", "host_path": "/tmp/test-search"},
    )
    if r.status_code == 409:
        # Already exists, find it
        r = client.get("/api/v1/projects")
        for p in r.json()["projects"]:
            if p["name"] == "test-search":
                yield p["id"]
                client.delete(f"/api/v1/projects/{p['id']}")
                return

    project_id = r.json()["id"]
    yield project_id
    client.delete(f"/api/v1/projects/{project_id}")


def test_semantic_search(client, project_with_index):
    r = client.post(
        f"/api/v1/projects/{project_with_index}/search",
        json={"query": "test function", "limit": 5},
    )
    assert r.status_code == 200
    data = r.json()
    assert "results" in data
    assert "total" in data
    assert "query_time_ms" in data


def test_symbol_search(client, project_with_index):
    r = client.post(
        f"/api/v1/projects/{project_with_index}/search/symbols",
        json={"query": "main", "limit": 5},
    )
    assert r.status_code == 200
    data = r.json()
    assert "results" in data
    assert "total" in data


def test_file_search(client, project_with_index):
    r = client.post(
        f"/api/v1/projects/{project_with_index}/search/files",
        json={"query": "test", "limit": 5},
    )
    assert r.status_code == 200
    data = r.json()
    assert "files" in data
    assert "total" in data


def test_project_summary(client, project_with_index):
    r = client.get(f"/api/v1/projects/{project_with_index}/summary")
    assert r.status_code == 200
    data = r.json()
    assert "name" in data
    assert "languages" in data
    assert "total_files" in data


def test_search_with_filters(client, project_with_index):
    r = client.post(
        f"/api/v1/projects/{project_with_index}/search",
        json={
            "query": "function",
            "limit": 5,
            "languages": ["python"],
            "min_score": 0.1,
        },
    )
    assert r.status_code == 200
    data = r.json()
    assert "results" in data
    assert "total" in data
    assert "query_time_ms" in data


def test_search_nonexistent_project(client):
    if not API_KEY:
        pytest.skip("API_KEY not set")
    r = client.post(
        "/api/v1/projects/nonexistent-id/search",
        json={"query": "test"},
    )
    assert r.status_code == 404

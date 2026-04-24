"""API integration tests — require running Docker container."""
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
        timeout=30.0,
    )


def test_health_no_auth():
    r = httpx.get(f"{BASE_URL}/health", timeout=10.0)
    assert r.status_code == 200
    assert r.json()["status"] == "ok"


def test_status_requires_auth():
    r = httpx.get(f"{BASE_URL}/api/v1/status", timeout=10.0)
    assert r.status_code in (401, 403)


def test_status_with_auth(client):
    if not API_KEY:
        pytest.skip("API_KEY not set")
    r = client.get("/api/v1/status")
    assert r.status_code == 200
    data = r.json()
    assert "model_loaded" in data
    assert "server_version" in data
    assert "api_version" in data
    assert data["api_version"] == "v1"


def test_project_crud(client):
    if not API_KEY:
        pytest.skip("API_KEY not set")

    # Create
    r = client.post(
        "/api/v1/projects",
        json={"name": "test-project", "host_path": "/tmp/test-project"},
    )
    assert r.status_code == 201
    project = r.json()
    project_id = project["id"]
    assert project["name"] == "test-project"

    # List
    r = client.get("/api/v1/projects")
    assert r.status_code == 200
    assert any(p["id"] == project_id for p in r.json()["projects"])

    # Get
    r = client.get(f"/api/v1/projects/{project_id}")
    assert r.status_code == 200
    assert r.json()["name"] == "test-project"

    # Update
    r = client.patch(
        f"/api/v1/projects/{project_id}",
        json={"name": "test-project-updated"},
    )
    assert r.status_code == 200
    assert r.json()["name"] == "test-project-updated"

    # Delete
    r = client.delete(f"/api/v1/projects/{project_id}")
    assert r.status_code == 204

    # Verify deleted
    r = client.get(f"/api/v1/projects/{project_id}")
    assert r.status_code == 404


def test_index_trigger(client):
    if not API_KEY:
        pytest.skip("API_KEY not set")

    # Create project first
    r = client.post(
        "/api/v1/projects",
        json={"name": "test-index", "host_path": "/tmp/test-index"},
    )
    project_id = r.json()["id"]

    # Trigger index
    r = client.post(f"/api/v1/projects/{project_id}/index")
    assert r.status_code == 202
    assert "run_id" in r.json()

    # Check status
    r = client.get(f"/api/v1/projects/{project_id}/index/status")
    assert r.status_code == 200
    assert "status" in r.json()

    # Cleanup
    client.delete(f"/api/v1/projects/{project_id}")

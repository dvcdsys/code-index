import os

import httpx

BASE_URL = os.environ.get("CODE_INDEX_API_URL", "http://localhost:21847")
API_KEY = os.environ.get("CODE_INDEX_API_KEY", "")

_NOT_RUNNING_MSG = (
    "Code index service not running. Start with:\n"
    "  cd ~/Cursor/code-index && docker compose up -d"
)


class APIClient:
    def __init__(self):
        self._client: httpx.AsyncClient | None = None

    def _get_client(self) -> httpx.AsyncClient:
        if self._client is None or self._client.is_closed:
            self._client = httpx.AsyncClient(
                base_url=BASE_URL,
                headers={"Authorization": f"Bearer {API_KEY}"},
                timeout=httpx.Timeout(300.0, connect=10.0),
            )
        return self._client

    async def request(self, method: str, path: str, **kwargs) -> dict | list | None:
        try:
            client = self._get_client()
            response = await client.request(method, path, **kwargs)
            response.raise_for_status()
            if response.status_code == 204:
                return None
            return response.json()
        except httpx.ConnectError:
            raise ConnectionError(_NOT_RUNNING_MSG)
        except httpx.HTTPStatusError as e:
            detail = ""
            try:
                detail = e.response.json().get("detail", "")
            except Exception:
                detail = e.response.text
            raise RuntimeError(f"API error ({e.response.status_code}): {detail}")

    async def get(self, path: str, **kwargs):
        return await self.request("GET", path, **kwargs)

    async def post(self, path: str, **kwargs):
        return await self.request("POST", path, **kwargs)

    async def patch(self, path: str, **kwargs):
        return await self.request("PATCH", path, **kwargs)

    async def delete(self, path: str, **kwargs):
        return await self.request("DELETE", path, **kwargs)

    async def close(self):
        if self._client and not self._client.is_closed:
            await self._client.aclose()


api_client = APIClient()

from fastapi import Depends, HTTPException, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from .config import settings

_scheme = HTTPBearer()


async def verify_api_key(
    credentials: HTTPAuthorizationCredentials = Depends(_scheme),
) -> str:
    token = credentials.credentials
    if not settings.api_key or token != settings.api_key:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Invalid or missing API key",
        )
    return token

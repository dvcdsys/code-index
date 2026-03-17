from pydantic import BaseModel


class ErrorResponse(BaseModel):
    detail: str


class MessageResponse(BaseModel):
    message: str

"""Internal HTTP API. Reachable only on the Docker network and only with the shared service token
(specs/architecture.md, Rules)."""

from __future__ import annotations

import hmac
from typing import Annotated
from uuid import UUID

from fastapi import Depends, FastAPI, File, Form, Header, Request, UploadFile
from fastapi.concurrency import run_in_threadpool
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from . import db
from .config import Settings
from .imports import ImportError_, Upload, import_file
from .parsers import default_parsers
from .parsers.base import Parser


def error(status: int, code: str, message: str, **extra: object) -> JSONResponse:
    return JSONResponse({"error": code, "message": message, **extra}, status_code=status)


def create_app(settings: Settings | None = None, parsers: dict[str, Parser] | None = None) -> FastAPI:
    settings = settings or Settings.from_env()
    parsers = default_parsers() if parsers is None else parsers
    app = FastAPI(title="importer", docs_url=None, redoc_url=None, openapi_url=None)

    @app.exception_handler(ImportError_)
    async def _import_error(_: Request, e: ImportError_) -> JSONResponse:
        return error(e.status, e.code, e.message, **e.extra)

    @app.exception_handler(RequestValidationError)
    async def _bad_request(_: Request, e: RequestValidationError) -> JSONResponse:
        fields = sorted({".".join(str(p) for p in err["loc"][1:]) for err in e.errors()})
        return error(400, "bad_request", "invalid or missing: " + ", ".join(fields))

    def require_service(x_service_token: Annotated[str, Header()] = "") -> None:
        if not hmac.compare_digest(x_service_token.encode(), settings.service_token.encode()):
            raise ImportError_(401, "unauthenticated", "missing or wrong service token")

    @app.get("/internal/health")
    def health() -> JSONResponse:
        try:
            db.connect(settings).close()
        except Exception:  # noqa: BLE001 - any failure means not ready; details stay in the logs
            return JSONResponse({"status": "unavailable", "database": "error"}, status_code=503)
        return JSONResponse({"status": "ok", "database": "ok"})

    @app.post("/internal/imports", status_code=201, dependencies=[Depends(require_service)])
    async def create_import(
        file: Annotated[UploadFile, File()],
        broker: Annotated[str, Form()],
        x_user_id: Annotated[UUID, Header()],
        account_id: Annotated[UUID | None, Form()] = None,
    ) -> dict:
        parser = parsers.get(broker)
        if parser is None:
            raise ImportError_(422, "unsupported_broker", f"no parser for broker {broker!r}")
        data = await _read_limited(file, settings.max_upload_bytes)
        if not data:
            raise ImportError_(422, "empty_file", "the uploaded file is empty")
        up = Upload(
            user_id=x_user_id, broker=broker, account_id=account_id,
            filename=file.filename or "upload", media_type=file.content_type or "application/octet-stream", data=data,
        )
        return await run_in_threadpool(import_file, settings, parser, up)

    return app


async def _read_limited(file: UploadFile, limit: int) -> bytes:
    chunks, size = [], 0
    while chunk := await file.read(1 << 20):
        size += len(chunk)
        if size > limit:
            shown = f"{limit // (1024 * 1024)} MiB" if limit >= 1024 * 1024 else f"{limit} bytes"
            raise ImportError_(413, "file_too_large", f"files are limited to {shown}")
        chunks.append(chunk)
    return b"".join(chunks)

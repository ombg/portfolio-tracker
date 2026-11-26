"""ASGI entry point: `uvicorn importer.main:app`."""

from .app import create_app

app = create_app()

"""Internal HTTP API and scheduler (D-3, D-39). Reachable only on the Docker network, with the shared service token
for everything but the health check."""

from __future__ import annotations

import hmac
import logging
import threading
from contextlib import asynccontextmanager
from datetime import datetime
from typing import Annotated, Any

import httpx
from fastapi import FastAPI, Header, HTTPException
from fastapi.responses import JSONResponse

from . import calendar, schedule
from .calendar import TZ
from .config import Settings
from .providers.boc import BankOfCanada
from .providers.http import Fetcher, RecordedTransport
from .providers.yahoo import Yahoo
from .refresh import Refresher
from .store import Store

log = logging.getLogger("marketdata")


class Scheduler:
    """Runs the slots of schedule.py. On start it catches up: missing history first, then the slot that is due if
    no run of its kind started since. With a frozen clock (MARKET_DATA_NOW) it only catches up once."""

    def __init__(self, refresher: Refresher, settings: Settings):
        self.refresher, self.settings = refresher, settings
        self.stop = threading.Event()
        self.thread = threading.Thread(target=self._loop, name="scheduler", daemon=True)

    def start(self) -> None:
        self.thread.start()

    def catch_up(self) -> None:
        self.refresher.run("backfill")
        now = self.settings.now()
        due = schedule.due_slot(now)
        if due is None:
            return
        last = self.refresher.store.last_started(due.kind)
        if last is None or last < due.at:
            self.refresher.run(due.kind)

    def _loop(self) -> None:
        try:
            self.catch_up()
        except Exception:  # noqa: BLE001 - keep the loop alive; the run itself records the failure
            log.exception("catch-up failed")
        if self.settings.frozen_now is not None:
            return
        while not self.stop.is_set():
            slot = schedule.next_slot(self.settings.now())
            wait = (slot.at - self.settings.now()).total_seconds()
            if self.stop.wait(max(wait, 0)):
                return
            try:
                self.refresher.run(slot.kind)
                if slot.kind == "close":
                    self.refresher.run("backfill")
            except Exception:  # noqa: BLE001
                log.exception("%s run failed", slot.kind)


def build(settings: Settings) -> tuple[Store, Refresher | None]:
    store = Store(settings.db)
    if settings.provider == "off":
        return store, None
    transport = RecordedTransport(settings.fixture_dir) if settings.provider == "fixture" else None
    client = httpx.Client(transport=transport, timeout=20) if transport else httpx.Client(timeout=20)
    fetcher = Fetcher(client)
    return store, Refresher(store, Yahoo(fetcher, now=settings.now), BankOfCanada(fetcher), settings.now,
                            settings.intraday_days_kept)


def status(store: Store, settings: Settings, refresher: Refresher | None) -> dict[str, Any]:
    now = settings.now()
    runs = store.runs(10)
    scheduled = [r for r in runs if r["kind"] in ("intraday", "close")]
    last_success = next((r for r in scheduled if r["status"] == "success"), None)
    iso = lambda t: t.isoformat() if isinstance(t, datetime) else None  # noqa: E731
    return {
        "provider": settings.provider,
        "clock": "frozen" if settings.frozen_now else "live",
        "now": now.isoformat(),
        "market_open": schedule.market_open(now),
        "last_close_date": calendar.latest_close(now, "TSX").astimezone(TZ).date().isoformat(),
        "last_success_at": iso(last_success["finished_at"]) if last_success else None,
        "last_run_status": scheduled[0]["status"] if scheduled else None,
        "next_run_at": iso(schedule.next_slot(now).at) if refresher and not settings.frozen_now else None,
        "runs": [{"id": str(r["id"]), "kind": r["kind"], "started_at": iso(r["started_at"]),
                  "finished_at": iso(r["finished_at"]), "status": r["status"], "symbols_ok": r["symbols_ok"],
                  "symbols_failed": r["symbols_failed"], "errors": r["errors"]} for r in runs],
    }


def create_app(settings: Settings | None = None, start_scheduler: bool = True) -> FastAPI:
    settings = settings or Settings.from_env()
    store, refresher = build(settings)
    scheduler = Scheduler(refresher, settings) if refresher else None

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        store.check_role()
        if scheduler and start_scheduler:
            scheduler.start()
        yield
        if scheduler:
            scheduler.stop.set()

    app = FastAPI(title="market-data", docs_url=None, redoc_url=None, openapi_url=None, lifespan=lifespan)
    app.state.scheduler, app.state.refresher, app.state.store = scheduler, refresher, store

    def require_service(token: str) -> None:
        if not hmac.compare_digest(token.encode(), settings.service_token.encode()):
            raise HTTPException(401, "unauthenticated")

    @app.exception_handler(HTTPException)
    async def _http_error(_, e: HTTPException) -> JSONResponse:
        return JSONResponse({"error": str(e.detail), "message": str(e.detail)}, status_code=e.status_code)

    @app.get("/internal/health")
    def health() -> dict[str, str]:
        return {"status": "ok", "provider": settings.provider}

    @app.get("/internal/status")
    def get_status(x_service_token: Annotated[str, Header()] = "") -> dict[str, Any]:
        require_service(x_service_token)
        return status(store, settings, refresher)

    def background(kind: str) -> dict[str, str]:
        if refresher is None:
            raise HTTPException(409, "provider_off")
        threading.Thread(target=refresher.run, args=(kind,), daemon=True).start()
        return {"status": "started", "kind": kind}

    @app.post("/internal/refresh", status_code=202)
    def refresh(kind: str = "intraday", x_service_token: Annotated[str, Header()] = "") -> dict[str, str]:
        require_service(x_service_token)
        if kind not in ("intraday", "close"):
            raise HTTPException(400, "kind must be intraday or close")
        return background(kind)

    @app.post("/internal/backfill", status_code=202)
    def backfill(x_service_token: Annotated[str, Header()] = "") -> dict[str, str]:
        require_service(x_service_token)
        return background("backfill")

    return app

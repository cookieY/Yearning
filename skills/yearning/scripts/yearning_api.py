#!/usr/bin/env python3
"""Zero-dependency Yearning HTTP API client for the `yearning` Agent Skill.

Wraps the governed `/api/v4/*` REST API using only the Python 3 standard library,
automatically handling session authentication, double-submit CSRF headers,
`Idempotency-Key` generation, optimistic concurrency (`If-Match`), explicit
audited SQL reveal (`/sql-reveals`), and ephemeral SQL review precheck polling.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import http.cookiejar
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from typing import Any, Callable

DEFAULT_BASE_URL = "http://127.0.0.1:8080"
MUTATING_METHODS = ("POST", "PUT", "PATCH", "DELETE")
TERMINAL_REVIEW_RUN_STATES = (
    "ready",
    "blocked",
    "partial",
    "failed",
    "outdated",
    "completed",
    "cancelled",
    "succeeded",
)
DIAGNOSTICS_SORT_CHOICES = (
    "total_exec_time_ms",
    "avg_exec_time_ms",
    "calls",
    "rows_examined",
)


class YearningAPIError(RuntimeError):
    """Represents an HTTP or business error returned by the Yearning API."""

    def __init__(self, message: str, status_code: int = 0, err_code: int = 0, payload: Any = None) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.err_code = err_code
        self.payload = payload


@dataclass(frozen=True)
class QuerySessionTarget:
    """Bundles target identifiers needed to resolve or open a read-only query session."""

    datasource_id: str
    session_id: str | None = None
    grant_id: str | None = None
    flow_id: str | None = None

    @classmethod
    def from_args(cls, args: argparse.Namespace) -> QuerySessionTarget:
        return cls(
            datasource_id=args.datasource_id,
            session_id=getattr(args, "session_id", None),
            grant_id=getattr(args, "grant_id", None),
            flow_id=getattr(args, "flow_id", None),
        )


def _extract_envelope_data(parsed: Any, status_code: int) -> Any:
    """Validates a Yearning JSON response envelope or raises YearningAPIError."""
    if isinstance(parsed, dict) and "err_code" in parsed:
        err_code = int(parsed.get("err_code") or 0)
        if err_code != 0:
            msg = parsed.get("message") or parsed.get("err_msg") or f"HTTP {status_code}"
            detail = parsed.get("data") if parsed.get("data") is not None else parsed.get("meta")
            raise YearningAPIError(
                f"Yearning business error {err_code} (HTTP {status_code}): {msg}",
                status_code=status_code,
                err_code=err_code,
                payload=detail,
            )
        return parsed.get("data")
    return parsed


class YearningClient:
    """Zero-dependency HTTP client for Yearning `/api/v4/*`."""

    def __init__(
        self,
        base_url: str,
        username: str = "",
        password: str = "",
        auth_provider: str = "local",
        session_cookie: str = "",
        csrf_token: str = "",
        timeout: float = 60.0,
    ) -> None:
        self.base_url = (base_url or DEFAULT_BASE_URL).rstrip("/")
        self.username = username.strip()
        self.password = password
        self.auth_provider = (auth_provider or "local").strip().lower()
        self.timeout = timeout
        self.cookie_jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookie_jar))
        self._manual_session = session_cookie.strip()
        self._manual_csrf = csrf_token.strip()

    def _current_cookies(self) -> tuple[str, str]:
        sess = self._manual_session
        csrf = self._manual_csrf
        for ck in self.cookie_jar:
            if ck.name == "yearning_session" and ck.value:
                sess = ck.value
            elif ck.name == "yearning_csrf" and ck.value:
                csrf = ck.value
        return sess, csrf

    def login(self) -> dict[str, Any]:
        if not self.username or not self.password:
            raise YearningAPIError(
                "Missing credentials: set YEARNING_USERNAME and YEARNING_PASSWORD (or YEARNING_PASSWORD_FILE)."
            )
        endpoint = "/api/v4/auth/ldap/login" if self.auth_provider == "ldap" else "/api/v4/auth/login"
        return self.request(
            "POST",
            endpoint,
            body={"username": self.username, "password": self.password},
            skip_auto_login=True,
        )

    def ensure_authenticated(self) -> None:
        sess, _ = self._current_cookies()
        if not sess and self.username and self.password:
            self.login()

    def request(
        self,
        method: str,
        path: str,
        *,
        query: dict[str, Any] | None = None,
        body: Any = None,
        if_match_version: int | str | None = None,
        idempotency_key: str | None = None,
        skip_auto_login: bool = False,
    ) -> Any:
        if not skip_auto_login:
            self.ensure_authenticated()

        method_up = method.upper().strip()
        bound_idem_key = idempotency_key
        if method_up in MUTATING_METHODS and not bound_idem_key:
            bound_idem_key = str(uuid.uuid4())

        try:
            return self._do_once(
                method_up,
                path,
                query=query,
                body=body,
                if_match_version=if_match_version,
                idempotency_key=bound_idem_key,
            )
        except YearningAPIError as exc:
            if exc.status_code == 401 and not skip_auto_login and self.username and self.password:
                self.login()
                return self._do_once(
                    method_up,
                    path,
                    query=query,
                    body=body,
                    if_match_version=if_match_version,
                    idempotency_key=bound_idem_key,
                )
            raise

    def _do_once(
        self,
        method_up: str,
        path: str,
        *,
        query: dict[str, Any] | None = None,
        body: Any = None,
        if_match_version: int | str | None = None,
        idempotency_key: str | None = None,
    ) -> Any:
        rel_path = path if path.startswith("/") else f"/{path}"
        url = f"{self.base_url}{rel_path}"
        if query:
            filtered = {k: v for k, v in query.items() if v is not None and v != ""}
            if filtered:
                url = f"{url}?{urllib.parse.urlencode(filtered, doseq=True)}"

        data_bytes: bytes | None = None
        headers: dict[str, str] = {"Accept": "application/json"}
        if body is not None:
            data_bytes = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"

        sess, csrf = self._current_cookies()
        if self._manual_session and not any(ck.name == "yearning_session" for ck in self.cookie_jar):
            cookie_parts = [f"yearning_session={sess}"]
            if csrf:
                cookie_parts.append(f"yearning_csrf={csrf}")
            headers["Cookie"] = "; ".join(cookie_parts)

        if method_up in MUTATING_METHODS:
            if csrf:
                headers["X-CSRF-Token"] = csrf
            if idempotency_key:
                headers["Idempotency-Key"] = idempotency_key

        if if_match_version is not None:
            ver_str = str(if_match_version).strip().strip('"')
            headers["If-Match"] = f'"{ver_str}"'

        req = urllib.request.Request(url, data=data_bytes, headers=headers, method=method_up)
        try:
            with self.opener.open(req, timeout=self.timeout) as resp:
                raw_bytes = resp.read()
                status_code = resp.getcode()
        except urllib.error.HTTPError as http_err:
            raw_bytes = http_err.read()
            status_code = http_err.code
            self._raise_http_error(status_code, raw_bytes)

        if not raw_bytes or not raw_bytes.strip():
            return None

        parsed = json.loads(raw_bytes.decode("utf-8"))
        return _extract_envelope_data(parsed, status_code)

    def _raise_http_error(self, status_code: int, raw_bytes: bytes) -> None:
        try:
            parsed = json.loads(raw_bytes.decode("utf-8"))
        except Exception:
            parsed = {"raw": raw_bytes.decode("utf-8", errors="replace")}

        _extract_envelope_data(parsed, status_code)
        if isinstance(parsed, dict):
            title = parsed.get("title") or parsed.get("message") or f"HTTP {status_code}"
            detail = parsed.get("detail") or ""
            msg = f"Yearning HTTP {status_code} ({title}): {detail}" if detail else f"Yearning HTTP {status_code}: {title}"
            raise YearningAPIError(msg, status_code=status_code, payload=parsed)
        raise YearningAPIError(f"Yearning HTTP {status_code}", status_code=status_code, payload=parsed)

    def resolve_version(self, resource_path: str, explicit_version: int | None = None) -> int:
        if explicit_version is not None and explicit_version > 0:
            return explicit_version
        data = self.request("GET", resource_path)
        if isinstance(data, dict):
            if "version" in data and isinstance(data["version"], int):
                return data["version"]
            if isinstance(data.get("order"), dict) and isinstance(data["order"].get("version"), int):
                return data["order"]["version"]
        raise YearningAPIError(f"Unable to resolve optimistic concurrency version from {resource_path}")


def _resolve_credentials(args: argparse.Namespace) -> tuple[str, str]:
    username = (args.username or os.environ.get("YEARNING_USERNAME") or "").strip()
    password = args.password or os.environ.get("YEARNING_PASSWORD") or ""
    pw_file = args.password_file or os.environ.get("YEARNING_PASSWORD_FILE") or ""
    if not password and pw_file:
        if not os.path.isfile(pw_file):
            raise YearningAPIError(f"Password file not found: {pw_file}")
        with open(pw_file, "r", encoding="utf-8") as fh:
            password = fh.read().strip()
    return username, password


def build_client(args: argparse.Namespace) -> YearningClient:
    base_url = (args.base_url or os.environ.get("YEARNING_BASE_URL") or DEFAULT_BASE_URL).strip()
    username, password = _resolve_credentials(args)
    return YearningClient(
        base_url=base_url,
        username=username,
        password=password,
        auth_provider=args.auth_provider or os.environ.get("YEARNING_AUTH_PROVIDER", "local"),
        session_cookie=os.environ.get("YEARNING_SESSION_COOKIE", ""),
        csrf_token=os.environ.get("YEARNING_CSRF_TOKEN", ""),
    )


def _fetch_metadata_tree(
    client: YearningClient,
    base_path: str,
    schema_name: str | None,
    table_name: str | None,
    extra_query: dict[str, Any] | None = None,
) -> Any:
    query = dict(extra_query or {})
    if table_name:
        if not schema_name:
            raise YearningAPIError("--schema-name is required when --table-name is provided")
        query["schema_name"] = schema_name
        query["table_name"] = table_name
        return client.request("GET", f"{base_path}/columns", query=query)
    if schema_name:
        query["schema_name"] = schema_name
        return client.request("GET", f"{base_path}/tables", query=query)
    return client.request("GET", f"{base_path}/schemas", query=query)


def cmd_me(client: YearningClient, _args: argparse.Namespace) -> Any:
    user = client.request("GET", "/api/v4/users/me")
    dashboard = client.request("GET", "/api/v4/dashboard/me")
    return {"user": user, "dashboard": dashboard}


def cmd_flows(client: YearningClient, args: argparse.Namespace) -> Any:
    if args.flow_type:
        return client.request(
            "GET",
            "/api/v4/users/me/flows",
            query={"flow_type": args.flow_type, "limit": args.limit, "after": args.after},
        )
    change_flows = client.request(
        "GET",
        "/api/v4/users/me/flows",
        query={"flow_type": "change_review", "limit": args.limit},
    )
    query_flows = client.request(
        "GET",
        "/api/v4/users/me/flows",
        query={"flow_type": "query_access", "limit": args.limit},
    )
    return {
        "change_review_flows": change_flows.get("items", []) if isinstance(change_flows, dict) else change_flows,
        "query_access_flows": query_flows.get("items", []) if isinstance(query_flows, dict) else query_flows,
    }


def cmd_metadata(client: YearningClient, args: argparse.Namespace) -> Any:
    base = f"/api/v4/users/me/flows/{urllib.parse.quote(args.flow_id)}/datasources/{urllib.parse.quote(args.datasource_id)}/metadata"
    return _fetch_metadata_tree(client, base, args.schema_name, args.table_name)


def cmd_precheck(client: YearningClient, args: argparse.Namespace) -> Any:
    body: dict[str, Any] = {"flow_id": args.flow_id, "sql": args.sql}
    if args.draft_id:
        body["draft_id"] = args.draft_id
    if args.output_locale:
        body["output_locale"] = args.output_locale

    started = client.request("POST", "/api/v4/review-runs", body=body)
    run_id = started.get("run_id") or started.get("id") if isinstance(started, dict) else None
    if not args.wait or not run_id:
        return {"run": started}

    run = client.request("GET", f"/api/v4/review-runs/{urllib.parse.quote(run_id)}")
    deadline = time.time() + args.wait_timeout
    while time.time() < deadline:
        state = str((run.get("status") or run.get("state") or "") if isinstance(run, dict) else "").lower()
        if state in TERMINAL_REVIEW_RUN_STATES:
            break
        time.sleep(0.5)
        run = client.request("GET", f"/api/v4/review-runs/{urllib.parse.quote(run_id)}")

    findings = client.request("GET", f"/api/v4/review-runs/{urllib.parse.quote(run_id)}/findings")
    return {"run_id": run_id, "run": run, "findings": findings}


def cmd_drafts(client: YearningClient, args: argparse.Namespace) -> Any:
    return client.request("GET", "/api/v4/change-drafts", query={"limit": args.limit, "after": args.after})


def cmd_create_draft(client: YearningClient, args: argparse.Namespace) -> Any:
    body: dict[str, Any] = {
        "flow_id": args.flow_id,
        "title": args.title,
        "rollback_enabled": not args.disable_rollback,
    }
    if args.description:
        body["description"] = args.description
    if args.requested_scheduled_for:
        body["requested_scheduled_for"] = args.requested_scheduled_for
    draft = client.request("POST", "/api/v4/change-drafts", body=body)
    if args.sql and isinstance(draft, dict) and draft.get("id"):
        did = urllib.parse.quote(draft["id"])
        ver = draft.get("version", 1)
        draft = client.request("PUT", f"/api/v4/change-drafts/{did}/sql", body={"sql": args.sql}, if_match_version=ver)
    return draft


def cmd_orders(client: YearningClient, args: argparse.Namespace) -> Any:
    if not (args.pending_my_approval or args.pending_my_execution or args.my_submissions):
        return client.request("GET", "/api/v4/change-orders", query={"limit": args.limit, "after": args.after})

    me = client.request("GET", "/api/v4/users/me")
    my_id = me.get("id") if isinstance(me, dict) else None
    filtered: list[dict[str, Any]] = []
    cursor = args.after
    while True:
        res = client.request("GET", "/api/v4/change-orders", query={"limit": args.limit, "after": cursor})
        if not isinstance(res, dict):
            break
        items = res.get("items") or []
        for item in items:
            if args.my_submissions and item.get("submitter_user_id") == my_id:
                filtered.append(item)
                continue
            if args.pending_my_approval and item.get("state") == "stage_approval_active":
                for stage in item.get("stages") or []:
                    if stage.get("state") != "approval_active":
                        continue
                    for step in stage.get("approval_steps") or []:
                        if step.get("state") == "active" and any(a.get("id") == my_id for a in (step.get("actors") or [])):
                            filtered.append(item)
                            break
            if args.pending_my_execution and item.get("state") in ("stage_execution_pending", "scheduled"):
                for stage in item.get("stages") or []:
                    if stage.get("state") in ("execution_pending", "scheduled") and any(
                        a.get("id") == my_id for a in (stage.get("execution_actors") or [])
                    ):
                        filtered.append(item)
                        break
        page = res.get("page") or {}
        if not page.get("has_more") or not page.get("next_cursor"):
            break
        cursor = page["next_cursor"]
    return {"user_id": my_id, "count": len(filtered), "items": filtered}


def cmd_order(client: YearningClient, args: argparse.Namespace) -> Any:
    oid = urllib.parse.quote(args.order_id)
    order = client.request("GET", f"/api/v4/change-orders/{oid}")
    out: dict[str, Any] = {"order": order}
    if args.reveal_sql:
        reveal = client.request("POST", f"/api/v4/change-orders/{oid}/sql-reveals", body={"purpose": "order-review"})
        out["sql"] = reveal.get("sql") if isinstance(reveal, dict) else reveal
    if args.reveal_rollback_sql:
        rb_reveal = client.request(
            "POST", f"/api/v4/change-orders/{oid}/sql-reveals", body={"purpose": "rollback-order-sql"}
        )
        out["rollback_sql"] = rb_reveal.get("sql") if isinstance(rb_reveal, dict) else rb_reveal
    if args.include_timeline:
        out["timeline"] = client.request("GET", f"/api/v4/change-orders/{oid}/timeline")
    if args.include_findings:
        out["review_findings"] = client.request("GET", f"/api/v4/change-orders/{oid}/review-findings")
    if args.include_comments:
        out["comments"] = client.request("GET", f"/api/v4/change-orders/{oid}/comments")
    return out


def cmd_submit_order(client: YearningClient, args: argparse.Namespace) -> Any:
    metadata: dict[str, Any] = {"title": args.title}
    if args.remark:
        metadata["description"] = args.remark
    if args.requested_scheduled_for:
        metadata["requested_scheduled_for"] = args.requested_scheduled_for
    body: dict[str, Any] = {
        "flow_id": args.flow_id,
        "run_id": args.run_id,
        "sql": args.sql,
        "rollback_enabled": not args.disable_rollback,
        "metadata": metadata,
    }
    if args.draft_id:
        body["draft_id"] = args.draft_id
    return client.request("POST", "/api/v4/change-orders", body=body)


def cmd_decide_order(client: YearningClient, args: argparse.Namespace) -> Any:
    oid = urllib.parse.quote(args.order_id)
    ver = client.resolve_version(f"/api/v4/change-orders/{oid}", args.version)
    body: dict[str, Any] = {"decision": args.decision}
    if args.comment:
        body["comment"] = args.comment
    return client.request(
        "POST",
        f"/api/v4/change-orders/{oid}/approval-decisions",
        body=body,
        if_match_version=ver,
    )


def cmd_comment_order(client: YearningClient, args: argparse.Namespace) -> Any:
    oid = urllib.parse.quote(args.order_id)
    return client.request("POST", f"/api/v4/change-orders/{oid}/comments", body={"content": args.content})


def cmd_withdraw_or_void(client: YearningClient, args: argparse.Namespace) -> Any:
    oid = urllib.parse.quote(args.order_id)
    ver = client.resolve_version(f"/api/v4/change-orders/{oid}", args.version)
    suffix = "voidance" if args.action == "void" else "withdrawal"
    return client.request(
        "POST",
        f"/api/v4/change-orders/{oid}/{suffix}",
        body={"reason": args.reason},
        if_match_version=ver,
    )


def cmd_execute_order(client: YearningClient, args: argparse.Namespace) -> Any:
    oid = urllib.parse.quote(args.order_id)
    ver = client.resolve_version(f"/api/v4/change-orders/{oid}", args.version)
    body: dict[str, Any] = {}
    if args.ddl_execution_mode:
        body["ddl_execution_mode"] = args.ddl_execution_mode
    return client.request(
        "POST",
        f"/api/v4/change-orders/{oid}/execution-attempts",
        body=body,
        if_match_version=ver,
    )


def cmd_execution_attempt(client: YearningClient, args: argparse.Namespace) -> Any:
    aid = urllib.parse.quote(args.attempt_id)
    attempt = client.request("GET", f"/api/v4/execution-attempts/{aid}")
    statements = client.request("GET", f"/api/v4/execution-attempts/{aid}/statements")
    return {"attempt": attempt, "statements": statements}


def cmd_rollback_draft(client: YearningClient, args: argparse.Namespace) -> Any:
    oid = urllib.parse.quote(args.order_id)
    draft = client.request("POST", f"/api/v4/change-orders/{oid}/rollback-drafts", body={})
    out: dict[str, Any] = {"draft": draft}
    if args.reveal_sql and isinstance(draft, dict) and draft.get("id"):
        did = urllib.parse.quote(draft["id"])
        reveal = client.request("POST", f"/api/v4/change-drafts/{did}/sql-reveals", body={"purpose": "draft-edit"})
        out["rollback_sql"] = reveal.get("sql") if isinstance(reveal, dict) else reveal
    return out


def _resolve_or_open_query_session(client: YearningClient, target: QuerySessionTarget) -> str:
    if target.session_id:
        return target.session_id

    sessions_resp = client.request("GET", "/api/v4/query-sessions")
    for sess in (sessions_resp.get("items") or []) if isinstance(sessions_resp, dict) else []:
        if sess.get("state") != "active":
            continue
        for cap in sess.get("capabilities") or []:
            if cap.get("datasource_id") == target.datasource_id and cap.get("can_query"):
                return sess["id"]

    grant_id = target.grant_id
    if not grant_id:
        grants_resp = client.request("GET", "/api/v4/query-grants")
        for g in (grants_resp.get("items") or []) if isinstance(grants_resp, dict) else []:
            if g.get("state") == "active":
                grant_id = g.get("id")
                break

    flow_id = target.flow_id
    if not flow_id:
        flows_resp = client.request("GET", "/api/v4/users/me/flows", query={"flow_type": "query_access"})
        for fl in (flows_resp.get("items") or []) if isinstance(flows_resp, dict) else []:
            for cap in fl.get("query_capabilities") or []:
                if cap.get("datasource_id") == target.datasource_id and cap.get("can_query"):
                    flow_id = fl.get("id")
                    break
            if flow_id:
                break

    body: dict[str, Any] = {"datasource_ids": [target.datasource_id]}
    if flow_id:
        body["flow_id"] = flow_id
    if grant_id:
        body["grant_id"] = grant_id
    session = client.request("POST", "/api/v4/query-sessions", body=body)
    session_id = session.get("id") if isinstance(session, dict) else None
    if not session_id:
        raise YearningAPIError("Failed to open query session; verify active query grant or pass --grant-id / --flow-id.")
    return session_id


def cmd_query_context(client: YearningClient, _args: argparse.Namespace) -> Any:
    grants = client.request("GET", "/api/v4/query-grants")
    sessions = client.request("GET", "/api/v4/query-sessions")
    flows = client.request("GET", "/api/v4/users/me/flows", query={"flow_type": "query_access"})
    return {
        "active_grants": [g for g in (grants.get("items") or []) if g.get("state") == "active"] if isinstance(grants, dict) else [],
        "active_sessions": [s for s in (sessions.get("items") or []) if s.get("state") == "active"] if isinstance(sessions, dict) else [],
        "query_access_flows": flows.get("items", []) if isinstance(flows, dict) else [],
    }


def cmd_query_metadata(client: YearningClient, args: argparse.Namespace) -> Any:
    target = QuerySessionTarget.from_args(args)
    session_id = _resolve_or_open_query_session(client, target)
    base = f"/api/v4/query-sessions/{urllib.parse.quote(session_id)}/metadata"
    return _fetch_metadata_tree(
        client,
        base,
        args.schema_name,
        args.table_name,
        extra_query={"datasource_id": target.datasource_id},
    )


def cmd_query(client: YearningClient, args: argparse.Namespace) -> Any:
    target = QuerySessionTarget.from_args(args)
    session_id = _resolve_or_open_query_session(client, target)
    body: dict[str, Any] = {
        "datasource_id": target.datasource_id,
        "schema_name": args.schema_name,
        "sql": args.sql,
        "page_size": args.page_size,
    }
    if args.timeout_ms:
        body["timeout_ms"] = args.timeout_ms
    execution = client.request(
        "POST",
        f"/api/v4/query-sessions/{urllib.parse.quote(session_id)}/executions",
        body=body,
    )
    return {"session_id": session_id, "execution": execution}


def cmd_query_page(client: YearningClient, args: argparse.Namespace) -> Any:
    eid = urllib.parse.quote(args.execution_id)
    return client.request(
        "GET",
        f"/api/v4/query-executions/{eid}/pages",
        query={"cursor": args.cursor, "page_size": args.page_size, "purpose": args.purpose},
    )


def cmd_query_request(client: YearningClient, args: argparse.Namespace) -> Any:
    return client.request(
        "POST",
        "/api/v4/query-access-requests",
        body={
            "flow_id": args.flow_id,
            "datasource_ids": args.datasource_ids,
            "requested_until": args.requested_until,
            "reason": args.reason,
        },
    )


def _diag_datasources(client: YearningClient, _args: argparse.Namespace) -> Any:
    return client.request("GET", "/api/v4/admin/diagnostics/datasources")


def _diag_overview(client: YearningClient, args: argparse.Namespace) -> Any:
    ds_id = urllib.parse.quote(args.datasource_id)
    return client.request(
        "GET",
        f"/api/v4/admin/diagnostics/datasources/{ds_id}/overview",
        query={
            "from": args.from_time,
            "to": args.to_time,
            "compare_from": args.compare_from,
            "compare_to": args.compare_to,
            "sort_by": args.sort_by,
            "limit": args.limit,
        },
    )


def _diag_snapshot(client: YearningClient, args: argparse.Namespace) -> Any:
    ds_id = urllib.parse.quote(args.datasource_id)
    return client.request("POST", f"/api/v4/admin/diagnostics/datasources/{ds_id}/snapshots", body={})


def _diag_detail(client: YearningClient, args: argparse.Namespace) -> Any:
    ds_id = urllib.parse.quote(args.datasource_id)
    stmt_id = urllib.parse.quote(args.statement_id)
    return client.request(
        "GET",
        f"/api/v4/admin/diagnostics/datasources/{ds_id}/statements/{stmt_id}",
        query={"from": args.from_time, "to": args.to_time},
    )


def _diag_ai_report(client: YearningClient, args: argparse.Namespace) -> Any:
    ds_id = urllib.parse.quote(args.datasource_id)
    stmt_id = urllib.parse.quote(args.statement_id)
    body: dict[str, Any] = {}
    if args.from_time:
        body["from"] = args.from_time
    if args.to_time:
        body["to"] = args.to_time
    if args.output_locale:
        body["output_locale"] = args.output_locale
    return client.request(
        "POST",
        f"/api/v4/admin/diagnostics/datasources/{ds_id}/statements/{stmt_id}/ai-reports",
        body=body,
    )


def _diag_precheck_report(client: YearningClient, args: argparse.Namespace) -> Any:
    ds_id = urllib.parse.quote(args.datasource_id)
    rep_id = urllib.parse.quote(args.report_id)
    body: dict[str, Any] = {"flow_id": args.flow_id}
    if args.candidate_sql:
        body["candidate_sql"] = args.candidate_sql
    if args.output_locale:
        body["output_locale"] = args.output_locale
    return client.request(
        "POST",
        f"/api/v4/admin/diagnostics/datasources/{ds_id}/ai-reports/{rep_id}/prechecks",
        body=body,
    )


def _diag_submit_report(client: YearningClient, args: argparse.Namespace) -> Any:
    ds_id = urllib.parse.quote(args.datasource_id)
    rep_id = urllib.parse.quote(args.report_id)
    body: dict[str, Any] = {
        "flow_id": args.flow_id,
        "ephemeral_run_id": args.ephemeral_run_id,
        "title": args.title,
    }
    if args.remark:
        body["remark"] = args.remark
    return client.request(
        "POST",
        f"/api/v4/admin/diagnostics/datasources/{ds_id}/ai-reports/{rep_id}/order-submissions",
        body=body,
    )


_DIAGNOSTICS_DISPATCH: dict[str, Callable[[YearningClient, argparse.Namespace], Any]] = {
    "datasources": _diag_datasources,
    "overview": _diag_overview,
    "snapshot": _diag_snapshot,
    "detail": _diag_detail,
    "ai-report": _diag_ai_report,
    "precheck-report": _diag_precheck_report,
    "submit-report": _diag_submit_report,
}


def cmd_diagnostics(client: YearningClient, args: argparse.Namespace) -> Any:
    handler = _DIAGNOSTICS_DISPATCH.get(args.diag_cmd)
    if not handler:
        raise YearningAPIError(f"Unknown diagnostics subcommand: {args.diag_cmd}")
    return handler(client, args)


def cmd_raw(client: YearningClient, args: argparse.Namespace) -> Any:
    body = json.loads(args.json_body) if args.json_body else None
    return client.request(
        args.method,
        args.path,
        body=body,
        if_match_version=args.if_match,
    )


def _add_query_session_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--datasource-id", required=True)
    parser.add_argument("--session-id")
    parser.add_argument("--grant-id")
    parser.add_argument("--flow-id")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Yearning HTTP API helper for the `yearning` Skill.")
    parser.add_argument("--base-url", help="Yearning base URL (or YEARNING_BASE_URL)")
    parser.add_argument("--username", help="Username (or YEARNING_USERNAME)")
    parser.add_argument("--password", help="Password (or YEARNING_PASSWORD)")
    parser.add_argument("--password-file", help="Path to password file (or YEARNING_PASSWORD_FILE)")
    parser.add_argument("--auth-provider", choices=["local", "ldap"], help="Authentication provider (default: local)")

    sub = parser.add_subparsers(dest="command", required=True)

    sub.add_parser("me", help="Fetch current user profile and personal dashboard counters")

    p_flows = sub.add_parser("flows", help="List granted change_review and query_access flows")
    p_flows.add_argument("--flow-type", choices=["change_review", "query_access"])
    p_flows.add_argument("--limit", type=int, default=50)
    p_flows.add_argument("--after")

    p_meta = sub.add_parser("metadata", help="Browse schemas, tables, or columns for a change_review flow datasource")
    p_meta.add_argument("--flow-id", required=True)
    p_meta.add_argument("--datasource-id", required=True)
    p_meta.add_argument("--schema-name")
    p_meta.add_argument("--table-name")

    p_pre = sub.add_parser("precheck", help="Run automated + AI SQL review precheck bound to a change_review flow")
    p_pre.add_argument("--flow-id", required=True)
    p_pre.add_argument("--sql", required=True)
    p_pre.add_argument("--draft-id")
    p_pre.add_argument("--output-locale")
    p_pre.add_argument("--no-wait", dest="wait", action="store_false", default=True)
    p_pre.add_argument("--wait-timeout", dest="wait_timeout", type=float, default=60.0)

    p_drafts = sub.add_parser("drafts", help="List saved change drafts")
    p_drafts.add_argument("--limit", type=int, default=50)
    p_drafts.add_argument("--after")

    p_cdraft = sub.add_parser("create-draft", help="Create a change draft and optionally populate its SQL")
    p_cdraft.add_argument("--flow-id", required=True)
    p_cdraft.add_argument("--title", required=True)
    p_cdraft.add_argument("--description")
    p_cdraft.add_argument("--sql")
    p_cdraft.add_argument("--requested-scheduled-for")
    p_cdraft.add_argument("--disable-rollback", action="store_true")

    p_orders = sub.add_parser("orders", help="List change orders (cursor-paginated via --limit and --after)")
    p_orders.add_argument("--pending-my-approval", action="store_true", help="Filter orders awaiting current user's approval")
    p_orders.add_argument("--pending-my-execution", action="store_true", help="Filter orders awaiting current user's execution")
    p_orders.add_argument("--my-submissions", action="store_true", help="Filter orders submitted by current user")
    p_orders.add_argument("--limit", type=int, default=50)
    p_orders.add_argument("--after")

    p_order = sub.add_parser("order", help="Get change order details (timeline, findings, comments, optional explicit SQL reveal)")
    p_order.add_argument("order_id")
    p_order.add_argument("--reveal-sql", action="store_true", default=False, help="Explicitly audit-reveal order SQL")
    p_order.add_argument("--reveal-rollback-sql", action="store_true", default=False, help="Explicitly audit-reveal rollback SQL")
    p_order.add_argument("--no-timeline", dest="include_timeline", action="store_false", default=True)
    p_order.add_argument("--no-findings", dest="include_findings", action="store_false", default=True)
    p_order.add_argument("--no-comments", dest="include_comments", action="store_false", default=True)

    p_sub = sub.add_parser("submit-order", help="Submit a governed change order from a completed precheck")
    p_sub.add_argument("--flow-id", required=True)
    p_sub.add_argument("--run-id", required=True)
    p_sub.add_argument("--title", required=True)
    p_sub.add_argument("--sql", required=True)
    p_sub.add_argument("--remark")
    p_sub.add_argument("--requested-scheduled-for")
    p_sub.add_argument("--draft-id")
    p_sub.add_argument("--disable-rollback", action="store_true")

    p_dec = sub.add_parser("decide-order", help="Approve or reject an active change order stage")
    p_dec.add_argument("order_id")
    p_dec.add_argument("decision", choices=["approve", "reject"])
    p_dec.add_argument("--comment")
    p_dec.add_argument("--version", type=int)

    p_cmt = sub.add_parser("comment-order", help="Post a collaboration comment on a change order")
    p_cmt.add_argument("order_id")
    p_cmt.add_argument("--content", required=True)

    p_wv = sub.add_parser("withdraw-or-void", help="Withdraw a pending order or void an approved order (submitter only)")
    p_wv.add_argument("order_id")
    p_wv.add_argument("action", choices=["withdraw", "void"])
    p_wv.add_argument("--reason", required=True)
    p_wv.add_argument("--version", type=int)

    p_exec = sub.add_parser("execute-order", help="Start an execution attempt on an approved change order")
    p_exec.add_argument("order_id")
    p_exec.add_argument("--ddl-execution-mode", choices=["gh_ost", "online_ddl"])
    p_exec.add_argument("--version", type=int)

    p_att = sub.add_parser("execution-attempt", help="Fetch execution attempt status and statement results")
    p_att.add_argument("attempt_id")

    p_rb = sub.add_parser("rollback-draft", help="Create a governed rollback change draft from an executed order")
    p_rb.add_argument("order_id")
    p_rb.add_argument("--reveal-sql", action="store_true", default=False, help="Explicitly audit-reveal generated rollback SQL")

    sub.add_parser("query-context", help="List active query grants, active query sessions, and query_access flows")

    p_qmeta = sub.add_parser("query-metadata", help="Browse schemas, tables, or columns inside a read-only query session")
    _add_query_session_args(p_qmeta)
    p_qmeta.add_argument("--schema-name")
    p_qmeta.add_argument("--table-name")

    p_qry = sub.add_parser("query", help="Execute a governed read-only SELECT query (reuses active session if present)")
    _add_query_session_args(p_qry)
    p_qry.add_argument("--schema-name", required=True)
    p_qry.add_argument("--sql", required=True)
    p_qry.add_argument("--page-size", type=int, default=100)
    p_qry.add_argument("--timeout-ms", type=int)

    p_qpage = sub.add_parser("query-page", help="Fetch the next page of read-only query results")
    p_qpage.add_argument("--execution-id", required=True)
    p_qpage.add_argument("--cursor", required=True)
    p_qpage.add_argument("--page-size", type=int)
    p_qpage.add_argument("--purpose", choices=["display", "export"], default="display")

    p_qreq = sub.add_parser("query-request", help="Submit a read-only query access request")
    p_qreq.add_argument("--flow-id", required=True)
    p_qreq.add_argument("--datasource-ids", nargs="+", required=True)
    p_qreq.add_argument("--requested-until", required=True, help="RFC3339 expiration timestamp")
    p_qreq.add_argument("--reason", required=True)

    p_diag = sub.add_parser("diagnostics", help="SQL performance diagnostics operations (admin)")
    diag_sub = p_diag.add_subparsers(dest="diag_cmd", required=True)
    diag_sub.add_parser("datasources", help="List datasources and diagnostics collection status")
    p_do = diag_sub.add_parser("overview", help="Get 6-chart overview and top statements")
    p_do.add_argument("--datasource-id", required=True)
    p_do.add_argument("--from-time")
    p_do.add_argument("--to-time")
    p_do.add_argument("--compare-from")
    p_do.add_argument("--compare-to")
    p_do.add_argument("--sort-by", choices=DIAGNOSTICS_SORT_CHOICES)
    p_do.add_argument("--limit", type=int, default=50)
    p_ds = diag_sub.add_parser("snapshot", help="Trigger manual diagnostics snapshot collection")
    p_ds.add_argument("--datasource-id", required=True)
    p_dd = diag_sub.add_parser("detail", help="Get 3-chart detail and SQL template for a statement")
    p_dd.add_argument("--datasource-id", required=True)
    p_dd.add_argument("--statement-id", required=True)
    p_dd.add_argument("--from-time")
    p_dd.add_argument("--to-time")
    p_da = diag_sub.add_parser("ai-report", help="Run desensitized AI root-cause diagnosis on a statement")
    p_da.add_argument("--datasource-id", required=True)
    p_da.add_argument("--statement-id", required=True)
    p_da.add_argument("--from-time")
    p_da.add_argument("--to-time")
    p_da.add_argument("--output-locale")
    p_dp = diag_sub.add_parser("precheck-report", help="Run formal SQL precheck on an AI diagnostic report candidate SQL")
    p_dp.add_argument("--datasource-id", required=True)
    p_dp.add_argument("--report-id", required=True)
    p_dp.add_argument("--flow-id", required=True)
    p_dp.add_argument("--candidate-sql")
    p_dp.add_argument("--output-locale")
    p_dsub = diag_sub.add_parser("submit-report", help="Submit a change order from a prechecked AI diagnostic report")
    p_dsub.add_argument("--datasource-id", required=True)
    p_dsub.add_argument("--report-id", required=True)
    p_dsub.add_argument("--flow-id", required=True)
    p_dsub.add_argument("--ephemeral-run-id", required=True)
    p_dsub.add_argument("--title", required=True)
    p_dsub.add_argument("--remark")

    p_raw = sub.add_parser("raw", help="Call any `/api/v4/*` endpoint directly")
    p_raw.add_argument("method", choices=["GET", "POST", "PUT", "PATCH", "DELETE"])
    p_raw.add_argument("path", help="Endpoint path, e.g. /api/v4/dashboard/me")
    p_raw.add_argument("json_body", nargs="?", help="Optional JSON request payload")
    p_raw.add_argument("--if-match", help="Optional If-Match version number")

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        client = build_client(args)
        dispatch = {
            "me": cmd_me,
            "flows": cmd_flows,
            "metadata": cmd_metadata,
            "precheck": cmd_precheck,
            "drafts": cmd_drafts,
            "create-draft": cmd_create_draft,
            "orders": cmd_orders,
            "order": cmd_order,
            "submit-order": cmd_submit_order,
            "decide-order": cmd_decide_order,
            "comment-order": cmd_comment_order,
            "withdraw-or-void": cmd_withdraw_or_void,
            "execute-order": cmd_execute_order,
            "execution-attempt": cmd_execution_attempt,
            "rollback-draft": cmd_rollback_draft,
            "query-context": cmd_query_context,
            "query-metadata": cmd_query_metadata,
            "query": cmd_query,
            "query-page": cmd_query_page,
            "query-request": cmd_query_request,
            "diagnostics": cmd_diagnostics,
            "raw": cmd_raw,
        }
        result = dispatch[args.command](client, args)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0
    except YearningAPIError as exc:
        err_out = {
            "error": str(exc),
            "status_code": exc.status_code,
            "err_code": exc.err_code,
            "details": exc.payload,
        }
        print(json.dumps(err_out, ensure_ascii=False, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())

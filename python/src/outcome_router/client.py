from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from typing import Any, Mapping, Sequence


class OutcomeRouterError(RuntimeError):
    def __init__(self, message: str, status: int | None = None, body: Any = None):
        super().__init__(message)
        self.status = status
        self.body = body


@dataclass(frozen=True)
class RoutingMetadata:
    trace_id: str
    workflow: str
    step: str = "response"
    risk_class: str = "normal"
    policy_id: str | None = None
    deadline_ms: int | None = None
    required_region: str | None = None
    required_privacy: str | None = None
    max_cost_usd: float | None = None

    @classmethod
    def create(
        cls,
        workflow: str,
        *,
        trace_id: str | None = None,
        step: str = "response",
        risk_class: str = "normal",
        **constraints: Any,
    ) -> "RoutingMetadata":
        return cls(
            trace_id=trace_id or f"trace_{uuid.uuid4().hex}",
            workflow=workflow,
            step=step,
            risk_class=risk_class,
            **constraints,
        )

    def payload(self) -> dict[str, Any]:
        return {key: value for key, value in asdict(self).items() if value is not None}


@dataclass(frozen=True)
class RoutedResponse:
    data: Mapping[str, Any]
    decision_id: str
    model: str
    policy_version: str
    fallback_reason: str | None


class Client:
    def __init__(self, base_url: str, api_key: str, timeout_seconds: float = 120):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout_seconds = timeout_seconds

    def chat(
        self,
        messages: Sequence[Mapping[str, Any]],
        *,
        routing: RoutingMetadata,
        model: str = "auto",
        **openai_parameters: Any,
    ) -> RoutedResponse:
        if openai_parameters.get("stream"):
            raise ValueError("The dependency-free SDK returns non-streaming responses; use the HTTP endpoint for SSE.")
        payload: dict[str, Any] = {
            "model": model,
            "messages": list(messages),
            "routing": routing.payload(),
            **openai_parameters,
        }
        data, headers = self._request("POST", "/v1/chat/completions", payload)
        return RoutedResponse(
            data=data,
            decision_id=headers.get("X-Outcome-Decision-ID", ""),
            model=headers.get("X-Outcome-Model", str(data.get("model", ""))),
            policy_version=headers.get("X-Outcome-Policy-Version", ""),
            fallback_reason=headers.get("X-Outcome-Fallback-Reason"),
        )

    def outcome(
        self,
        outcome_type: str,
        value: bool | int | float,
        *,
        decision_id: str | None = None,
        trace_id: str | None = None,
        occurred_at: datetime | None = None,
        metadata: Mapping[str, Any] | None = None,
    ) -> Mapping[str, Any]:
        if not decision_id and not trace_id:
            raise ValueError("decision_id or trace_id is required")
        payload = {
            "decision_id": decision_id,
            "trace_id": trace_id,
            "type": outcome_type,
            "value": value,
            "occurred_at": (occurred_at or datetime.now(timezone.utc)).isoformat(),
            "metadata": dict(metadata or {}),
        }
        data, _ = self._request("POST", "/v1/outcomes", payload)
        return data

    def summary(
        self,
        *,
        days: int = 30,
        policy_id: str | None = None,
        primary_outcome: str | None = None,
    ) -> Mapping[str, Any]:
        parameters = [f"days={int(days)}"]
        if policy_id:
            parameters.append(f"policy_id={urllib.parse.quote(policy_id)}")
        if primary_outcome:
            parameters.append(f"primary_outcome={urllib.parse.quote(primary_outcome)}")
        data, _ = self._request("GET", "/v1/audit/summary?" + "&".join(parameters), None)
        return data

    def _request(
        self, method: str, path: str, payload: Mapping[str, Any] | None
    ) -> tuple[Mapping[str, Any], Mapping[str, str]]:
        body = None if payload is None else json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(
            self.base_url + path,
            data=body,
            method=method,
            headers={
                "Authorization": f"Bearer {self.api_key}",
                "Content-Type": "application/json",
                "Accept": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout_seconds) as response:
                decoded = json.loads(response.read().decode("utf-8"))
                return decoded, dict(response.headers.items())
        except urllib.error.HTTPError as error:
            raw = error.read().decode("utf-8", errors="replace")
            try:
                error_body = json.loads(raw)
            except json.JSONDecodeError:
                error_body = raw
            raise OutcomeRouterError(
                f"Outcome Router returned HTTP {error.code}",
                status=error.code,
                body=error_body,
            ) from error
        except urllib.error.URLError as error:
            raise OutcomeRouterError(f"Unable to reach Outcome Router: {error.reason}") from error

#!/usr/bin/env python3
"""
CalfGateway 鉴权自动化测试。

用法：
    pip install -r tools/requirements.txt
    python tools/test_auth.py
    python tools/test_auth.py --secret your-test-secret --base-url http://127.0.0.1:8100

前置条件：
    1. config.yaml 中 auth.enabled: true
    2. 受保护路径（默认 /v1/api/hello）不在 public_paths 中
    3. Mock 后端已启动：cd mock-backend && go run .
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import sys
import time
from dataclasses import dataclass
from typing import Any, Callable, Optional

import jwt
import requests


@dataclass
class TestResult:
    name: str
    passed: bool
    detail: str


def _b64url_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).decode().rstrip("=")


def _encode_hs256_manual(secret: str, payload: dict[str, Any]) -> str:
    """兼容 Go 网关 secret 为空字符串的情况（PyJWT 会拒绝空 HMAC key）。"""
    header = {"alg": "HS256", "typ": "JWT"}
    segments = [
        _b64url_encode(json.dumps(header, separators=(",", ":")).encode()),
        _b64url_encode(json.dumps(payload, separators=(",", ":")).encode()),
    ]
    signing_input = ".".join(segments).encode("ascii")
    signature = hmac.new(secret.encode("utf-8"), signing_input, hashlib.sha256).digest()
    segments.append(_b64url_encode(signature))
    return ".".join(segments)


def make_token(
    secret: str,
    sub: str = "user-123",
    exp_offset_sec: int = 7200,
    extra_claims: Optional[dict[str, Any]] = None,
) -> str:
    payload: dict[str, Any] = {
        "sub": sub,
        "exp": int(time.time()) + exp_offset_sec,
    }
    if extra_claims:
        payload.update(extra_claims)
    try:
        return jwt.encode(payload, secret, algorithm="HS256")
    except jwt.InvalidKeyError:
        return _encode_hs256_manual(secret, payload)


def request(
    base_url: str,
    path: str,
    authorization: Optional[str] = None,
    timeout: float = 5.0,
) -> requests.Response:
    headers = {}
    if authorization is not None:
        headers["Authorization"] = authorization
    return requests.get(f"{base_url.rstrip('/')}{path}", headers=headers, timeout=timeout)


def parse_json_safe(resp: requests.Response) -> Any:
    try:
        return resp.json()
    except (json.JSONDecodeError, ValueError):
        return None


def run_test(name: str, fn: Callable[[], str]) -> TestResult:
    try:
        detail = fn()
        return TestResult(name=name, passed=True, detail=detail)
    except AssertionError as exc:
        return TestResult(name=name, passed=False, detail=str(exc))
    except requests.RequestException as exc:
        return TestResult(name=name, passed=False, detail=f"请求失败: {exc}")


def test_public_path_no_token(base_url: str, public_path: str) -> str:
    resp = request(base_url, public_path)
    if resp.status_code == 401:
        raise AssertionError(f"公开路径不应要求鉴权，实际 401，body={resp.text}")
    return f"status={resp.status_code}（非 401 即通过鉴权放行）"


def test_protected_no_token(base_url: str, protected_path: str) -> str:
    resp = request(base_url, protected_path)
    body = parse_json_safe(resp)
    if resp.status_code != 401:
        raise AssertionError(f"期望 401，实际 {resp.status_code}，body={resp.text}")
    if not isinstance(body, dict) or body.get("error") != "Authorization header is required":
        raise AssertionError(f"期望 error='Authorization header is required'，实际 body={body}")
    return "401 + Authorization header is required"


def test_protected_bad_format(base_url: str, protected_path: str, token: str) -> str:
    resp = request(base_url, protected_path, authorization=f"Token {token}")
    body = parse_json_safe(resp)
    if resp.status_code != 401:
        raise AssertionError(f"期望 401，实际 {resp.status_code}，body={resp.text}")
    expected = "Authorization header format must be Bearer {token}"
    if not isinstance(body, dict) or body.get("error") != expected:
        raise AssertionError(f"期望 error='{expected}'，实际 body={body}")
    return f"401 + {expected}"


def test_protected_invalid_token(base_url: str, protected_path: str) -> str:
    resp = request(base_url, protected_path, authorization="Bearer invalid.token.here")
    body = parse_json_safe(resp)
    if resp.status_code != 401:
        raise AssertionError(f"期望 401，实际 {resp.status_code}，body={resp.text}")
    if not isinstance(body, dict) or body.get("error") != "Invalid or expired token":
        raise AssertionError(f"期望 Invalid or expired token，实际 body={body}")
    return "401 + Invalid or expired token"


def test_protected_valid_token(base_url: str, protected_path: str, token: str) -> str:
    resp = request(base_url, protected_path, authorization=f"Bearer {token}")
    if resp.status_code != 200:
        raise AssertionError(f"期望 200，实际 {resp.status_code}，body={resp.text}")
    return f"200，body={resp.text[:120]}"


def test_protected_expired_token(base_url: str, protected_path: str, secret: str) -> str:
    expired = make_token(secret, exp_offset_sec=-3600)
    resp = request(base_url, protected_path, authorization=f"Bearer {expired}")
    body = parse_json_safe(resp)
    if resp.status_code != 401:
        raise AssertionError(f"期望 401，实际 {resp.status_code}，body={resp.text}")
    if not isinstance(body, dict) or body.get("error") != "Invalid or expired token":
        raise AssertionError(f"期望 Invalid or expired token，实际 body={body}")
    return "401 + Invalid or expired token（过期）"


def test_protected_wrong_secret(base_url: str, protected_path: str, gateway_secret: str) -> str:
    wrong_secret = "definitely-wrong-secret"
    if wrong_secret == gateway_secret:
        wrong_secret += "-alt"
    token = make_token(wrong_secret)
    resp = request(base_url, protected_path, authorization=f"Bearer {token}")
    body = parse_json_safe(resp)
    if resp.status_code != 401:
        raise AssertionError(f"期望 401，实际 {resp.status_code}，body={resp.text}")
    if not isinstance(body, dict) or body.get("error") != "Invalid or expired token":
        raise AssertionError(f"期望 Invalid or expired token，实际 body={body}")
    return "401 + secret 不匹配被拒绝"


def test_sub_forwarding(base_url: str, protected_path: str, secret: str) -> str:
    sub = "test-user-456"
    token = make_token(secret, sub=sub)
    resp = request(base_url, protected_path, authorization=f"Bearer {token}")
    if resp.status_code != 200:
        raise AssertionError(f"期望 200，实际 {resp.status_code}，body={resp.text}")

    body = parse_json_safe(resp)
    if isinstance(body, dict) and "user_id" in body:
        if body["user_id"] != sub:
            raise AssertionError(f"期望 X-User-ID 透传 user_id={sub}，实际 {body['user_id']}")
        return f"X-User-ID 透传正确，user_id={sub}"

    return (
        "SKIP：Mock 未回显 X-User-ID（响应无 user_id 字段）。"
        "若需验证透传，请在 mock-backend 中回显 r.Header.Get(\"X-User-ID\")"
    )


def check_auth_enabled(base_url: str, protected_path: str) -> None:
    resp = request(base_url, protected_path)
    if resp.status_code == 200:
        print()
        print("警告: 受保护路径在无 token 时返回 200。")
        print("请确认 config.yaml 中:")
        print("  - auth.enabled: true")
        print(f"  - {protected_path} 不在 public_paths 中")
        print("修改后请重启网关再运行本脚本。")
        print()


def check_gateway_reachable(base_url: str) -> None:
    try:
        requests.get(base_url, timeout=3)
    except requests.RequestException as exc:
        print(f"无法连接网关 {base_url}：{exc}")
        print("请先启动网关：cd cmd && go run main.go")
        sys.exit(1)


def print_results(results: list[TestResult]) -> int:
    passed = sum(1 for r in results if r.passed)
    skipped = sum(1 for r in results if r.passed and r.detail.startswith("SKIP"))
    failed = len(results) - passed

    print()
    print("=" * 60)
    print("CalfGateway 鉴权测试结果")
    print("=" * 60)
    for i, result in enumerate(results, 1):
        if not result.passed:
            mark = "FAIL"
        elif result.detail.startswith("SKIP"):
            mark = "SKIP"
        else:
            mark = "PASS"
        print(f"[{mark}] {i}. {result.name}")
        print(f"       {result.detail}")
    print("-" * 60)
    print(f"合计: {len(results)}  通过: {passed - skipped}  跳过: {skipped}  失败: {failed}")

    return 0 if failed == 0 else 1


def main() -> int:
    parser = argparse.ArgumentParser(description="CalfGateway 鉴权自动化测试")
    parser.add_argument("--base-url", default="http://127.0.0.1:8100", help="网关地址")
    parser.add_argument(
        "--secret",
        default="",
        help="与 config.yaml 中 auth.secret 一致（当前配置为空则留空）",
    )
    parser.add_argument("--public-path", default="/health", help="公开路径")
    parser.add_argument("--protected-path", default="/v1/api/hello", help="受保护路径")
    args = parser.parse_args()

    print(f"网关: {args.base_url}")
    print(f"secret: {repr(args.secret)}")
    print(f"公开路径: {args.public_path}")
    print(f"受保护路径: {args.protected_path}")

    check_gateway_reachable(args.base_url)
    check_auth_enabled(args.base_url, args.protected_path)

    valid_token = make_token(args.secret)

    tests = [
        ("公开路径无需 token", lambda: test_public_path_no_token(args.base_url, args.public_path)),
        ("受保护路径缺少 token", lambda: test_protected_no_token(args.base_url, args.protected_path)),
        (
            "Authorization 格式错误（非 Bearer）",
            lambda: test_protected_bad_format(args.base_url, args.protected_path, valid_token),
        ),
        ("无效 token", lambda: test_protected_invalid_token(args.base_url, args.protected_path)),
        ("有效 token", lambda: test_protected_valid_token(args.base_url, args.protected_path, valid_token)),
        ("过期 token", lambda: test_protected_expired_token(args.base_url, args.protected_path, args.secret)),
        (
            "secret 不匹配",
            lambda: test_protected_wrong_secret(args.base_url, args.protected_path, args.secret),
        ),
        (
            "JWT sub 透传 X-User-ID",
            lambda: test_sub_forwarding(args.base_url, args.protected_path, args.secret),
        ),
    ]

    results = [run_test(name, fn) for name, fn in tests]
    return print_results(results)


if __name__ == "__main__":
    raise SystemExit(main())

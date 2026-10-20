#!/usr/bin/env python3
"""Small, credential-safe VpsCT API client using only Python's standard library."""

import argparse
import http.client
import ipaddress
import json
import math
import os
import socket
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

MAX_BODY = 4 << 20
MAX_RESPONSE = 16 << 20
MAX_SSE_EVENT = 1 << 20
DEFAULT_MAX_EVENTS = 20


class ClientError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Even a same-host redirect can escape the intended API path.
        return None


def strict_json(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate field")
            result[key] = value
        return result

    def constant(_):
        raise ValueError("non-finite number")

    return json.loads(data, object_pairs_hook=pairs, parse_constant=constant)


def endpoint(base, path, allow_http_localhost=False):
    if any(ord(c) < 33 or ord(c) == 127 for c in base + path):
        raise ClientError("URL 不能包含空白或控制字符")
    try:
        origin = urllib.parse.urlsplit(base)
        target = urllib.parse.urlsplit(path)
        port = origin.port
    except ValueError as exc:
        raise ClientError("URL 格式无效") from exc
    if (not origin.hostname or origin.username is not None or origin.password is not None
            or origin.query or origin.fragment or origin.path not in ("", "/")
            or "\\" in base or port == 0):
        raise ClientError("VPSCT_URL 必须是面板 origin，不能含凭据、子路径、查询或片段")
    local = origin.hostname.lower() == "localhost"
    try:
        local = local or ipaddress.ip_address(origin.hostname).is_loopback
    except ValueError:
        pass
    if origin.scheme != "https" and not (
            origin.scheme == "http" and allow_http_localhost and local):
        raise ClientError("必须使用 HTTPS；仅显式 --allow-http-localhost 允许回环 HTTP")
    if (target.scheme or target.netloc or target.fragment
            or not target.path.startswith("/api/v1/") or "\\" in path
            or "%" in target.path or "//" in target.path
            or any(part in (".", "..") for part in target.path.split("/"))):
        raise ClientError("只接受 /api/v1/ 下的绝对路径；路径不能编码、跳级或重定向到其他地址")
    return urllib.parse.urlunsplit((origin.scheme, origin.netloc, target.path, target.query, ""))


def load_body(source):
    if source is None:
        return None
    try:
        if source == "-":
            body = sys.stdin.buffer.read(MAX_BODY + 1)
        else:
            with open(source, "rb") as stream:
                body = stream.read(MAX_BODY + 1)
        if len(body) > MAX_BODY:
            raise ClientError("请求体超过 4 MiB")
        strict_json(body)
    except (OSError, UnicodeError, ValueError, RecursionError) as exc:
        raise ClientError("无法读取有效 JSON 请求体（不接受重复字段或非有限数值）") from exc
    return body


def redact(value, key):
    return value.replace(key, "[REDACTED]")


def fetch(method, url, key, body=None, timeout=30):
    if not key or any(ord(c) < 33 or ord(c) > 126 for c in key):
        raise ClientError("VPSCT_API_KEY 为空或含非法字符")
    headers = {"Authorization": "Bearer " + key, "Accept": "application/json, text/plain;q=0.9",
               "User-Agent": "vpsct-skill/1"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    # No cookie jar; no environment proxy that could intercept local HTTP.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=timeout) as response:
            data = response.read(MAX_RESPONSE + 1)
            if len(data) > MAX_RESPONSE:
                raise ClientError("响应超过 16 MiB；请缩小查询范围")
            media = response.headers.get_content_type()
            if data and (media == "application/json" or media.endswith("+json")):
                try:
                    strict_json(data)
                except (ValueError, UnicodeError, RecursionError) as exc:
                    raise ClientError("服务端 JSON 响应结构无效；写操作结果请通过查询确认") from exc
            return data, media
    except urllib.error.HTTPError as exc:
        with exc:
            if 300 <= exc.code < 400:
                raise ClientError("HTTP 重定向已拒绝；请核对面板的最终 HTTPS origin") from exc
            detail = ""
            try:
                error = strict_json(exc.read(MAX_RESPONSE + 1))["error"]
                if isinstance(error, dict) and isinstance(error.get("code"), str):
                    detail = " " + error["code"]
                    if isinstance(error.get("message"), str):
                        detail += ": " + error["message"]
            except (KeyError, TypeError, ValueError, UnicodeError, RecursionError):
                detail = "（服务端错误响应结构无效）"
            if exc.code == 429:
                retry_after = exc.headers.get("Retry-After", "未提供")
                detail += "; Retry-After=" + retry_after + "；请稍后再查，客户端不会自动重试"
            raise ClientError(redact("HTTP " + str(exc.code) + detail, key)) from exc
    except (socket.timeout, TimeoutError) as exc:
        raise ClientError("请求超时，结果未知；先查询状态，不要自动重试写操作") from exc
    except urllib.error.URLError as exc:
        if isinstance(exc.reason, (socket.timeout, TimeoutError)):
            raise ClientError("请求超时，结果未知；先查询状态，不要自动重试写操作") from exc
        raise ClientError("连接失败；请检查面板地址、网络和 TLS 证书，写操作需先确认结果") from exc
    except OSError as exc:
        raise ClientError("读取响应失败，结果未知；先查询状态，不要自动重试写操作") from exc
    except http.client.HTTPException as exc:
        raise ClientError("HTTP 响应结构无效，结果未知；先查询状态，不要自动重试写操作") from exc


def emit(data, media, output_fd, key):
    # Never echo the authentication key even if an upstream response reflects it.
    data = data.replace(key.encode("ascii"), b"[REDACTED]")
    json_output = None
    if data and (media == "application/json" or media.endswith("+json")):
        try:
            value = strict_json(data)
            rendered = json.dumps(value, ensure_ascii=False, indent=2)
            json_output = redact(rendered, key)
            # Unicode escapes can conceal a reflected credential in the raw
            # bytes. Redact the decoded value in private output files too.
            if json_output != rendered:
                data = json_output.encode("utf-8")
        except (ValueError, UnicodeError, RecursionError) as exc:
            raise ClientError("响应不是有效 UTF-8 JSON；写操作请先查询服务端状态") from exc
    if output_fd is not None:
        try:
            with os.fdopen(output_fd, "wb", closefd=False) as stream:
                stream.write(data)
        except OSError as exc:
            raise ClientError("无法保存响应；写操作请先查询服务端状态") from exc
        return
    if not data:
        return
    try:
        if json_output is not None:
            print(json_output)
        elif media.startswith("text/"):
            sys.stdout.write(data.decode("utf-8"))
        else:
            raise ClientError("此响应不能安全输出为文本；请使用 --output 保存")
    except (ValueError, UnicodeError, RecursionError) as exc:
        raise ClientError("响应不是有效 UTF-8 文本；请使用 --output 保存") from exc


def stream_events(url, key, timeout=30, max_events=DEFAULT_MAX_EVENTS):
    """Emit complete project SSE events as bounded NDJSON; never reconnect."""
    if not key or any(ord(c) < 33 or ord(c) > 126 for c in key):
        raise ClientError("VPSCT_API_KEY 为空或含非法字符")
    request = urllib.request.Request(url, headers={
        "Authorization": "Bearer " + key, "Accept": "text/event-stream",
        "User-Agent": "vpsct-skill/1"}, method="GET")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    deadline = time.monotonic() + timeout
    count, received, event_bytes = 0, 0, 0
    pending, data_lines = b"", []
    event_name = "message"
    try:
        with opener.open(request, timeout=timeout) as response:
            if response.headers.get_content_type() != "text/event-stream":
                raise ClientError("--stream 需要 text/event-stream 响应，未输出响应正文")
            # urllib returns HTTPResponse with a socket-backed buffered reader.
            # read1 performs at most one socket read, unlike readline/read which
            # can keep extending their timeout when a server drips bytes.
            try:
                stream_socket = response.fp.raw._sock
            except AttributeError as exc:
                raise ClientError("HTTP 连接不支持有界流式读取") from exc
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError
                stream_socket.settimeout(remaining)
                chunk = response.read1(min(65536, MAX_RESPONSE - received + 1))
                if time.monotonic() >= deadline:
                    raise TimeoutError
                if not chunk:
                    if pending or data_lines:
                        raise ClientError("事件流在完整事件之前断开；已输出事件保留，不自动重连")
                    raise ClientError("事件流已关闭；请核对 key 和服务状态，已输出事件保留，不自动重连")
                received += len(chunk)
                if received > MAX_RESPONSE:
                    raise ClientError("事件流超过 16 MiB；已输出事件保留，不自动重连")
                pending += chunk
                while b"\n" in pending:
                    raw_line, pending = pending.split(b"\n", 1)
                    if raw_line.endswith(b"\r"):
                        raw_line = raw_line[:-1]
                    event_bytes += len(raw_line) + 1
                    if event_bytes > MAX_SSE_EVENT:
                        raise ClientError("单个事件超过 1 MiB；已输出事件保留，不自动重连")
                    try:
                        line = raw_line.decode("utf-8")
                    except UnicodeError as exc:
                        raise ClientError("事件流不是有效 UTF-8；已输出事件保留，不自动重连") from exc
                    if not line:
                        if data_lines:
                            try:
                                data = strict_json("\n".join(data_lines))
                            except (ValueError, UnicodeError, RecursionError) as exc:
                                raise ClientError("事件 data 不是有效 JSON；已输出事件保留，不自动重连") from exc
                            record = json.dumps({"event": event_name, "data": data}, ensure_ascii=False)
                            print(redact(record, key), flush=True)
                            count += 1
                            if count >= max_events:
                                return count
                        event_name, data_lines, event_bytes = "message", [], 0
                    elif not line.startswith(":"):
                        field, separator, value = line.partition(":")
                        if separator and value.startswith(" "):
                            value = value[1:]
                        if field == "event":
                            event_name = value or "message"
                        elif field == "data":
                            data_lines.append(value)
                if event_bytes + len(pending) > MAX_SSE_EVENT:
                    raise ClientError("单个事件超过 1 MiB；已输出事件保留，不自动重连")
    except urllib.error.HTTPError as exc:
        with exc:
            if 300 <= exc.code < 400:
                raise ClientError("HTTP 重定向已拒绝；请核对面板的最终 HTTPS origin") from exc
            retry = "; Retry-After=" + exc.headers.get("Retry-After", "未提供") if exc.code == 429 else ""
            raise ClientError(redact("HTTP " + str(exc.code) + retry + "；未打开事件流，不自动重连", key)) from exc
    except (socket.timeout, TimeoutError) as exc:
        raise ClientError("事件流达到总超时；已输出完整事件保留，不自动重连，可查询当前状态") from exc
    except urllib.error.URLError as exc:
        if isinstance(exc.reason, (socket.timeout, TimeoutError)):
            raise ClientError("事件流达到总超时；已输出完整事件保留，不自动重连，可查询当前状态") from exc
        raise ClientError("事件流连接失败；请检查地址、网络和 TLS 证书，不自动重连") from exc
    except (OSError, http.client.HTTPException) as exc:
        raise ClientError("事件流读取失败或截断；已输出完整事件保留，不自动重连") from exc


def main(argv=None):
    parser = argparse.ArgumentParser(description="通过环境变量访问 VpsCT API，不自动重试或跟随重定向")
    parser.add_argument("method", choices=("GET", "POST", "PUT", "DELETE"))
    parser.add_argument("path", help="/api/v1/ 下的路径，可含查询参数")
    parser.add_argument("--body", metavar="FILE|-", help="JSON 请求文件；- 从 stdin 读取")
    parser.add_argument("--output", metavar="FILE", help="新建权限为 0600 的响应文件（不覆盖已有文件）")
    parser.add_argument("--timeout", type=float, default=30, help="连接/读超时秒数；--stream 为整个连接总期限，默认 30")
    parser.add_argument("--stream", action="store_true", help="GET SSE，以 NDJSON 即收即输出完整事件，不自动重连")
    parser.add_argument("--max-events", type=int, help="仅 --stream 可用，最多接收的事件数，默认 20（含 hello）")
    parser.add_argument("--allow-http-localhost", action="store_true", help="显式允许本机回环 HTTP")
    args = parser.parse_args(argv)
    key = os.environ.get("VPSCT_API_KEY", "")
    output_fd = None
    try:
        if not math.isfinite(args.timeout) or args.timeout <= 0:
            raise ClientError("超时必须是有限正数")
        if args.method == "GET" and args.body is not None:
            raise ClientError("GET 不接受请求体")
        if args.stream and (args.method != "GET" or args.output is not None):
            raise ClientError("--stream 只支持无 body 的 GET，不能配合 --output；事件以 NDJSON 输出到 stdout")
        if args.max_events is not None and (not args.stream or args.max_events <= 0):
            raise ClientError("--max-events 仅配合 --stream，且必须为正整数")
        url = endpoint(os.environ.get("VPSCT_URL", ""), args.path, args.allow_http_localhost)
        body = load_body(args.body)
        if args.stream:
            stream_events(url, key, args.timeout, args.max_events or DEFAULT_MAX_EVENTS)
            return 0
        if args.output:
            try:
                # Reserve the private file before any remote mutation. Exclusive
                # creation also prevents overwrites and following a symlink.
                output_fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            except OSError as exc:
                raise ClientError("无法新建输出文件；请选择不存在的文件路径（未发送请求）") from exc
        data, media = fetch(args.method, url, key, body, args.timeout)
        emit(data, media, output_fd, key)
        return 0
    except ClientError as exc:
        message = str(exc)
        if key:
            message = redact(message, key)
        print(message, file=sys.stderr)
        return 1
    finally:
        if output_fd is not None:
            os.close(output_fd)


if __name__ == "__main__":
    sys.exit(main())

"""Isolated transport tests; no live panel, VPS or public-network requests."""

import contextlib
import http.client
import http.server
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("vpsct_client", Path(__file__).with_name("vpsct.py"))
client = importlib.util.module_from_spec(spec)
spec.loader.exec_module(client)
TEST_KEY = "vpsct_test_transport_only"


class TransportTest(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.status = 200
        self.response = b'{"ok":true}'
        self.response_type = "application/json"
        self.extra_headers = {}
        self.stream_writer = None
        test = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                test.requests.append({"method": self.command, "path": self.path,
                                      "headers": dict(self.headers),
                                      "body": self.rfile.read(int(self.headers.get("Content-Length", 0)))})
                self.send_response(test.status)
                self.send_header("Content-Type", test.response_type)
                for key, value in test.extra_headers.items():
                    self.send_header(key, value)
                self.end_headers()
                try:
                    if test.stream_writer is not None:
                        test.stream_writer(self.wfile)
                    else:
                        self.wfile.write(test.response)
                except (BrokenPipeError, ConnectionResetError):
                    pass

            do_POST = do_GET
            do_PUT = do_GET
            do_DELETE = do_GET

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.origin = "http://127.0.0.1:" + str(self.server.server_address[1])
        self.temp = tempfile.TemporaryDirectory()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.temp.cleanup()

    def call(self, *args, environ=None):
        stdout, stderr = io.StringIO(), io.StringIO()
        env = {"VPSCT_URL": self.origin, "VPSCT_API_KEY": TEST_KEY}
        if environ:
            env.update(environ)
        with patch.dict(os.environ, env), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            result = client.main([*args, "--allow-http-localhost"])
        return result, stdout.getvalue(), stderr.getvalue()

    def test_authentication_without_cookies_or_environment_proxy(self):
        self.extra_headers = {"Set-Cookie": "ctlvps_session=not-a-real-session"}
        for _ in range(2):
            code, output, error = self.call("GET", "/api/v1/agent/capabilities",
                                            environ={"http_proxy": "http://127.0.0.1:1"})
            self.assertEqual((code, error), (0, ""))
            self.assertEqual(json.loads(output), {"ok": True})
        for req in self.requests:
            self.assertEqual(req["headers"]["Authorization"], "Bearer " + TEST_KEY)
            self.assertNotIn("Cookie", req["headers"])

    def test_redirect_never_reaches_destination(self):
        destinations = []

        class Sink(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                destinations.append(dict(self.headers))
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass

        sink = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Sink)
        thread = threading.Thread(target=sink.serve_forever, daemon=True)
        thread.start()
        try:
            self.status = 302
            self.extra_headers = {"Location": "http://127.0.0.1:" + str(sink.server_address[1]) + "/steal"}
            code, output, error = self.call("GET", "/api/v1/dashboard")
            self.assertEqual(code, 1)
            self.assertEqual(output, "")
            self.assertIn("重定向已拒绝", error)
            self.assertEqual(destinations, [])
            self.assertEqual(len(self.requests), 1)
        finally:
            sink.shutdown()
            sink.server_close()
            thread.join()

    def test_http_requires_explicit_loopback(self):
        for origin in ("http://panel.example.com", "http://127.0.0.1.example.com", "http://192.0.2.1"):
            with self.subTest(origin=origin), self.assertRaises(client.ClientError):
                client.endpoint(origin, "/api/v1/meta", True)
        with self.assertRaises(client.ClientError):
            client.endpoint(self.origin, "/api/v1/meta")
        self.assertEqual(client.endpoint("http://[::1]:8080", "/api/v1/meta", True),
                         "http://[::1]:8080/api/v1/meta")

    def test_rejects_ambiguous_origin_and_paths_before_network(self):
        for path in ("https://other.example.com/api/v1/meta", "//other.example.com/api/v1/meta",
                     "/api/agent/v1/heartbeat", "/api/v1/../auth", "/api/v1/%2e%2e/auth",
                     "/api/v1/%2Fmeta", "/api/v1//meta", "/api/v1/meta#fragment",
                     "/api/v1/meta\n", "/api/v1/meta\\else"):
            with self.subTest(path=path):
                code, output, _ = self.call("GET", path)
                self.assertEqual((code, output), (1, ""))
        for origin in ("https://user:password@panel.example.com", "https://panel.example.com/panel",
                       "https://panel.example.com?token=x", "https://panel.example.com/#part"):
            with self.subTest(origin=origin), self.assertRaises(client.ClientError):
                client.endpoint(origin, "/api/v1/meta")
        self.assertEqual(self.requests, [])

    def test_no_mutation_retry_and_429_guidance(self):
        self.status = 429
        self.response = b'{"error":{"code":"rate_limited","message":"wait"}}'
        self.extra_headers = {"Retry-After": "60"}
        code, output, error = self.call("POST", "/api/v1/shares/1/pause")
        self.assertEqual((code, output), (1, ""))
        self.assertIn("Retry-After=60", error)
        self.assertEqual(len(self.requests), 1)
        self.status = 503
        self.call("POST", "/api/v1/shares/1/pause")
        self.assertEqual(len(self.requests), 2)

    def test_invalid_json_and_body_are_errors(self):
        for response in (b'{"broken":', b'{"a":1,"a":2}', b'{"a":NaN}'):
            self.response = response
            code, output, error = self.call("GET", "/api/v1/dashboard")
            self.assertEqual((code, output), (1, ""))
            self.assertIn("JSON 响应结构无效", error)
        before = len(self.requests)
        body = Path(self.temp.name) / "invalid.json"
        body.write_text('{"a":NaN}')
        self.assertEqual(self.call("PUT", "/api/v1/settings", "--body", str(body))[0], 1)
        self.assertEqual(self.call("GET", "/api/v1/meta", "--body", str(body))[0], 1)
        self.assertEqual(len(self.requests), before)

    def test_json_body_from_file_and_stdin(self):
        body = Path(self.temp.name) / "body.json"
        body.write_text('{"name":"fixture"}')
        self.assertEqual(self.call("PUT", "/api/v1/settings", "--body", str(body))[0], 0)
        stdin = io.TextIOWrapper(io.BytesIO(b'{"name":"stdin fixture"}'))
        with patch.object(client.sys, "stdin", stdin):
            self.assertEqual(self.call("POST", "/api/v1/subscriptions/preview", "--body", "-")[0], 0)
        self.assertEqual(json.loads(self.requests[0]["body"]), {"name": "fixture"})
        self.assertEqual(json.loads(self.requests[1]["body"]), {"name": "stdin fixture"})
        self.assertEqual(self.requests[0]["headers"]["Content-Type"], "application/json")

    def test_response_file_is_private_and_not_overwritten(self):
        self.response = b'{"reflected_key":"' + TEST_KEY.encode() + b'"}'
        output = Path(self.temp.name) / "response.json"
        self.assertEqual(self.call("GET", "/api/v1/nodes", "--output", str(output))[0], 0)
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
        self.assertNotIn(TEST_KEY, output.read_text())
        before = len(self.requests)
        self.assertEqual(self.call("POST", "/api/v1/nodes", "--output", str(output))[0], 1)
        self.assertEqual(len(self.requests), before)
        self.assertIn("[REDACTED]", output.read_text())
        self.assertNotIn(TEST_KEY, self.call("GET", "/api/v1/nodes")[1])
        self.status = 403
        self.response = json.dumps({"error": {"code": "forbidden", "message": TEST_KEY}}).encode()
        self.assertNotIn(TEST_KEY, self.call("GET", "/api/v1/nodes")[2])

    def test_timeout_is_nonzero_and_does_not_retry(self):
        with patch.object(client.urllib.request.OpenerDirector, "open", side_effect=TimeoutError):
            code, output, error = self.call("POST", "/api/v1/shares")
        self.assertEqual((code, output), (1, ""))
        self.assertIn("结果未知", error)
        self.assertEqual(self.requests, [])

    def test_json_escaped_reflected_key_is_redacted_after_decoding(self):
        escaped_key = "".join("\\u%04x" % ord(c) for c in TEST_KEY)
        self.response = ('{"reflected_key":"' + escaped_key + '"}').encode()
        code, output, error = self.call("GET", "/api/v1/nodes")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(json.loads(output), {"reflected_key": "[REDACTED]"})
        target = Path(self.temp.name) / "escaped-key.json"
        self.assertEqual(self.call("GET", "/api/v1/nodes", "--output", str(target))[0], 0)
        self.assertEqual(json.loads(target.read_text()), {"reflected_key": "[REDACTED]"})

    def test_malformed_http_is_nonzero_without_traceback(self):
        with patch.object(client.urllib.request.OpenerDirector, "open", side_effect=http.client.BadStatusLine("malformed")):
            code, output, error = self.call("POST", "/api/v1/shares")
        self.assertEqual((code, output), (1, ""))
        self.assertIn("HTTP 响应结构无效", error)
        self.assertNotIn("Traceback", error)
        self.assertEqual(self.requests, [])

    def test_stream_outputs_complete_events_and_ignores_pings(self):
        self.response_type = "text/event-stream"
        escaped_key = "".join("\\u%04x" % ord(c) for c in TEST_KEY)
        self.response = (": ping\n\nevent: hello\ndata: {}\n\n"
                         "event: share.changed\ndata: {\n"
                         'data: "id": 7, "reflected_key": "' + escaped_key + '"}\n\n').encode()
        code, output, error = self.call("GET", "/api/v1/events", "--stream", "--max-events", "2")
        self.assertEqual((code, error), (0, ""))
        records = [json.loads(line) for line in output.splitlines()]
        self.assertEqual(records, [{"event": "hello", "data": {}},
                                   {"event": "share.changed", "data": {"id": 7, "reflected_key": "[REDACTED]"}}])
        self.assertNotIn(TEST_KEY, output)
        self.assertEqual(self.requests[0]["headers"]["Accept"], "text/event-stream")
        self.assertEqual(self.requests[0]["headers"]["Authorization"], "Bearer " + TEST_KEY)
        self.assertNotIn("Cookie", self.requests[0]["headers"])

    def test_stream_emits_before_connection_ends(self):
        self.response_type = "text/event-stream"
        first_received, release_second = threading.Event(), threading.Event()

        def writer(stream):
            stream.write(b"event: hello\ndata: {}\n\n")
            stream.flush()
            release_second.wait(2)
            stream.write(b'event: server.changed\ndata: {"id":1}\n\n')
            stream.flush()

        self.stream_writer = writer

        class ObservedOutput(io.StringIO):
            def flush(self):
                first_received.set()
                super().flush()

        output, error, results = ObservedOutput(), io.StringIO(), []
        env = {"VPSCT_URL": self.origin, "VPSCT_API_KEY": TEST_KEY}
        with patch.dict(os.environ, env), contextlib.redirect_stdout(output), contextlib.redirect_stderr(error):
            worker = threading.Thread(target=lambda: results.append(client.main([
                "GET", "/api/v1/events", "--stream", "--max-events", "2", "--allow-http-localhost"])))
            worker.start()
            try:
                self.assertTrue(first_received.wait(1), "first event was buffered until the connection closed")
                self.assertTrue(worker.is_alive())
                self.assertEqual(json.loads(output.getvalue().strip()), {"event": "hello", "data": {}})
            finally:
                release_second.set()
                worker.join(3)
        self.assertFalse(worker.is_alive())
        self.assertEqual((results, error.getvalue()), ([0], ""))
        self.assertEqual(len(output.getvalue().splitlines()), 2)

    def test_stream_total_deadline_survives_continuous_comments(self):
        self.response_type = "text/event-stream"

        def writer(stream):
            stream.write(b"event: hello\ndata: {}\n\n")
            stream.flush()
            for _ in range(20):
                time.sleep(0.03)
                stream.write(b": ping\n\n")
                stream.flush()

        self.stream_writer = writer
        started = time.monotonic()
        code, output, error = self.call("GET", "/api/v1/events", "--stream", "--timeout", "0.15")
        elapsed = time.monotonic() - started
        self.assertEqual(code, 1)
        self.assertLess(elapsed, 0.5, "continuous traffic extended the total deadline")
        self.assertEqual(json.loads(output.strip()), {"event": "hello", "data": {}})
        self.assertIn("总超时", error)
        self.assertIn("不自动重连", error)
        self.assertEqual(len(self.requests), 1)

    def test_stream_rejects_wrong_media_truncated_and_invalid_data(self):
        code, output, error = self.call("GET", "/api/v1/events", "--stream")
        self.assertEqual((code, output), (1, ""))
        self.assertIn("text/event-stream", error)
        self.response_type = "text/event-stream"
        for response, message in ((b"event: hello\ndata: {}", "完整事件之前断开"),
                                  (b"event: hello\ndata: {bad}\n\n", "有效 JSON"),
                                  (b"event: hello\ndata: {\"a\":1,\"a\":2}\n\n", "有效 JSON")):
            with self.subTest(response=response):
                self.response = response
                code, output, error = self.call("GET", "/api/v1/events", "--stream")
                self.assertEqual((code, output), (1, ""))
                self.assertIn(message, error)
                self.assertNotIn(TEST_KEY, error)

    def test_stream_total_deadline_bounds_a_dripping_partial_line(self):
        self.response_type = "text/event-stream"

        def writer(stream):
            stream.write(b"event: hello\ndata: {}\n\n")
            stream.flush()
            for byte in b'data: {"unfinished":"' + TEST_KEY.encode():
                time.sleep(0.03)
                stream.write(bytes([byte]))
                stream.flush()

        self.stream_writer = writer
        started = time.monotonic()
        code, output, error = self.call("GET", "/api/v1/events", "--stream", "--timeout", "0.15")
        self.assertEqual(code, 1)
        self.assertLess(time.monotonic() - started, 0.5, "a partial line extended the total deadline")
        self.assertEqual(json.loads(output.strip()), {"event": "hello", "data": {}})
        self.assertIn("总超时", error)
        self.assertNotIn(TEST_KEY, output + error)
        self.assertEqual(len(self.requests), 1)

    def test_stream_bounds_event_size_total_bytes_and_default_event_count(self):
        self.response_type = "text/event-stream"
        self.response = b"event: hello\ndata: {}\n\n" * (client.DEFAULT_MAX_EVENTS + 1)
        code, output, error = self.call("GET", "/api/v1/events", "--stream")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(len(output.splitlines()), client.DEFAULT_MAX_EVENTS)
        self.response = b'data: {"large":"' + b"x" * 80 + b'"}\n\n'
        with patch.object(client, "MAX_SSE_EVENT", 64):
            code, output, error = self.call("GET", "/api/v1/events", "--stream")
        self.assertEqual((code, output), (1, ""))
        self.assertIn("单个事件超过", error)
        self.response = b": ping\n\n" * 100
        with patch.object(client, "MAX_RESPONSE", 64):
            code, output, error = self.call("GET", "/api/v1/events", "--stream")
        self.assertEqual((code, output), (1, ""))
        self.assertIn("事件流超过", error)

    def test_stream_rejects_incompatible_flags_without_network(self):
        output = Path(self.temp.name) / "events.jsonl"
        for args in (("POST", "/api/v1/events", "--stream"),
                     ("GET", "/api/v1/events", "--stream", "--output", str(output)),
                     ("GET", "/api/v1/events", "--max-events", "2"),
                     ("GET", "/api/v1/events", "--stream", "--max-events", "0"),
                     ("GET", "/api/v1/events", "--stream", "--max-events", "-1")):
            with self.subTest(args=args):
                self.assertEqual(self.call(*args)[0], 1)
        self.assertEqual(self.requests, [])
        self.assertFalse(output.exists())

    def test_stream_redirect_and_auth_failure_do_not_reconnect(self):
        for status in (302, 401, 429):
            self.status = status
            self.extra_headers = {"Location": self.origin + "/api/v1/events", "Retry-After": "60"}
            before = len(self.requests)
            code, output, error = self.call("GET", "/api/v1/events", "--stream")
            self.assertEqual((code, output), (1, ""))
            self.assertEqual(len(self.requests), before + 1)
            self.assertNotIn(TEST_KEY, error)
        self.assertIn("Retry-After=60", error)


if __name__ == "__main__":
    unittest.main()

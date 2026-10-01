"""Offline malformed-HTTP regressions for credential-safe live fixtures."""

import socketserver
import threading
import traceback
import unittest
import urllib.request

from ku_live_fixture import LiveCheckError, NoRedirect, request_json


class RequestJSONRedactionTests(unittest.TestCase):
    def test_malformed_status_and_oversized_header_fail_without_raw_response(self):
        response_secret = b"fixture-response-secret"
        responses = [
            response_secret + b" invalid status line\r\n",
            b"HTTP/1.1 200 OK\r\nX-Secret: "
            + response_secret
            + b"x" * 65536
            + b"\r\n\r\n{}",
        ]
        for raw_response in responses:
            with self.subTest(response_size=len(raw_response)):
                class MalformedResponse(socketserver.BaseRequestHandler):
                    def handle(self):
                        self.request.recv(4096)
                        try:
                            self.request.sendall(raw_response)
                        except ConnectionError:
                            pass

                with socketserver.TCPServer(
                    ("127.0.0.1", 0), MalformedResponse
                ) as server:
                    server.timeout = 2
                    thread = threading.Thread(target=server.handle_request)
                    thread.start()
                    opener = urllib.request.build_opener(
                        NoRedirect(), urllib.request.ProxyHandler({})
                    )
                    try:
                        request_json(
                            opener,
                            "http://127.0.0.1:" + str(server.server_address[1]),
                            "/models",
                            authorization="fixture-owner-projection-key",
                            timeout=2,
                        )
                    except LiveCheckError as error:
                        self.assertEqual(
                            str(error), "GET /models could not reach its endpoint"
                        )
                        self.assertIsNone(error.__cause__)
                        self.assertTrue(error.__suppress_context__)
                        diagnostic = "".join(traceback.format_exception(error))
                        self.assertNotIn(response_secret.decode(), diagnostic)
                        self.assertNotIn("fixture-owner-projection-key", diagnostic)
                        self.assertNotIn("BadStatusLine", diagnostic)
                        self.assertNotIn("LineTooLong", diagnostic)
                    else:
                        self.fail("Malformed HTTP response was accepted")
                    finally:
                        thread.join(timeout=3)
                    self.assertFalse(thread.is_alive())


if __name__ == "__main__":
    unittest.main()

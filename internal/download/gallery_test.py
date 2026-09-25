"""Offline integration checks against the pinned gallery-dl dependency."""
import contextlib
import http.server
import io
import json
import pathlib
import re
import runpy
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

from gallery_dl import extractor
from gallery_dl.extractor.common import Extractor, Message

SCRIPT = pathlib.Path(__file__).with_name("gallery.py")


class AdapterTests(unittest.TestCase):
    def run_adapter(self, count=2, extension="jpg", fail=False):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(404 if fail else 200)
                self.end_headers()
                self.wfile.write(b"image fixture")
            def log_message(self, *args):
                pass
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()

        class Fixture(Extractor):
            category = "fixture"
            subcategory = "post"
            root = "https://example.com"
            def items(self):
                yield Message.Directory, None, {}
                for i in range(count):
                    yield Message.Url, f"http://127.0.0.1:{server.server_port}/{i}", {"extension": extension[i] if isinstance(extension, list) else extension}

        try:
            with tempfile.TemporaryDirectory() as directory:
                output = io.StringIO()
                args = [str(SCRIPT), json.dumps({"url": "https://example.com/post", "quality": "1080"}), directory, "20"]
                fixture = Fixture(re.match(".*", args[1]))
                with patch.object(extractor, "find", return_value=fixture), patch.object(sys, "argv", args), contextlib.redirect_stdout(output):
                    with self.assertRaises(SystemExit) as result:
                        runpy.run_path(str(SCRIPT), run_name="__main__")
                files = sorted(pathlib.Path(directory).glob("*"))
                manifest = json.loads(output.getvalue()) if output.getvalue() else []
                for path in manifest:
                    self.assertTrue(pathlib.Path(path).is_file())
                return result.exception.code, [p.name for p in files], manifest
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_ordered_gallery(self):
        code, files, manifest = self.run_adapter()
        self.assertEqual(code, 0)
        self.assertEqual(files, ["001.jpg", "002.jpg"])
        self.assertEqual([pathlib.Path(p).name for p in manifest], files)

    def test_mixed_gallery_preserves_order(self):
        code, files, manifest = self.run_adapter(extension=["jpg", "mp4"])
        self.assertEqual(code, 0)
        self.assertEqual([pathlib.Path(p).name for p in manifest], ["001.jpg", "002.mp4"])

    def test_video_carousel_keeps_all_items(self):
        code, files, manifest = self.run_adapter(extension="mp4")
        self.assertEqual(code, 0)
        self.assertEqual([pathlib.Path(p).name for p in manifest], ["001.mp4", "002.mp4"])

    def test_video_uses_ytdlp(self):
        code, files, manifest = self.run_adapter(count=1, extension="mp4")
        self.assertEqual(code, 3)
        self.assertEqual(files, [])

    def test_overflow_is_not_truncated(self):
        code, files, manifest = self.run_adapter(count=21)
        self.assertNotEqual(code, 0)
        self.assertEqual(files, [])
        self.assertEqual(manifest, [])

    def test_failed_download_has_no_manifest(self):
        code, files, manifest = self.run_adapter(fail=True)
        self.assertNotEqual(code, 0)
        self.assertEqual(manifest, [])


if __name__ == "__main__":
    unittest.main()

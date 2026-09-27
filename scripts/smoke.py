"""Offline end-to-end checks, run inside the built image by CI."""
import functools
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen

API = os.environ.get("SMOKE_API", "http://127.0.0.1:8080")
KEY = os.environ["SNATCHER_API_KEY"]
ORIGIN = "http://127.0.0.1:18081"


def request(path, body=None, authenticated=True):
    headers = {"Content-Type": "application/json"}
    if authenticated:
        headers["X-API-Key"] = KEY
    req = Request(path if path.startswith("http") else API + path,
                  data=json.dumps(body).encode() if body is not None else None,
                  headers=headers)
    with urlopen(req, timeout=30) as response:
        return json.load(response)


def job(path):
    result = request("/v1/snatch", {"url": ORIGIN + path})
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        time.sleep(3)
        result = request("/v1/jobs/" + result["id"])
        if result["status"] in ("completed", "failed", "cancelled"):
            return result
    raise AssertionError("job did not finish")


class Fixtures(SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        payload = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        photo = "/broken.jpg" if payload["url"].endswith("/broken.html") else "/photo.png"
        result = {"status": "picker", "picker": [
            {"type": "photo", "url": ORIGIN + photo},
            {"type": "video", "url": ORIGIN + "/clip.mp4"},
        ]}
        data = json.dumps(result).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    subprocess.run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1",
                    "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:v", "libx264",
                    "-c:a", "aac", "-shortest", str(root / "clip.mp4")], check=True)
    subprocess.run(["ffmpeg", "-v", "error", "-i", str(root / "clip.mp4"),
                    "-frames:v", "1", str(root / "photo.png")], check=True)
    (root / "gallery.html").write_text('<html><title>Photos</title><img src="photo.png"></html>')
    (root / "broken.html").write_text('<html><title>Broken photo</title></html>')
    (root / "broken.jpg").write_bytes(b"\xff\xd8\xff")
    (root / "second.mp4").write_bytes((root / "clip.mp4").read_bytes())
    (root / "collection.html").write_text('<html><title>Collection</title><video src="clip.mp4"></video><video src="second.mp4"></video></html>')
    server = ThreadingHTTPServer(("127.0.0.1", 18081), functools.partial(Fixtures, directory=directory))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        for source, count in [("/clip.mp4", 1), ("/gallery.html", 2), ("/collection.html", 2)]:
            result = job(source)
            assert result["status"] == "completed", result
            assert len(result["items"]) == count, result
            for i, item in enumerate(result["items"]):
                assert item["url"].startswith(API + "/download/"), item
                with urlopen(item["url"], timeout=30) as response:
                    media = response.read()
                assert media, item
                path = root / ("download-" + str(i) + Path(item["filename"]).suffix)
                path.write_bytes(media)
                subprocess.run(["ffmpeg", "-v", "error", "-xerror", "-i", str(path), "-f", "null", "-"], check=True)
                if item["type"] == "video":
                    metadata = json.loads(subprocess.check_output(["ffprobe", "-v", "error", "-show_streams", "-of", "json", str(path)]))
                    codecs = {s["codec_type"]: s["codec_name"] for s in metadata["streams"]}
                    assert codecs.get("video") == "h264" and codecs.get("audio") == "aac", codecs
            print("PASS", source, flush=True)
        result = job("/broken.html")
        assert result["status"] == "failed", result
        print("PASS corrupt media rejected", flush=True)
    finally:
        server.shutdown()
        server.server_close()
        thread.join()

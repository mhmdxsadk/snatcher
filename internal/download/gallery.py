"""Pinned gallery-dl adapter: one post/gallery, ordered files, no user config."""
import contextlib
import json
import os
import sys

from gallery_dl import config, extractor, job

# Generic content categories, not platform-specific extraction code.
CATEGORIES = {"post", "tweet", "status", "image", "gallery", "album", "pin",
              "video", "reel", "clip", "item", "shortlink", "redirect", "vm", "vmpost"}


def main():
    request, directory, limit = json.loads(sys.argv[1]), sys.argv[2], int(sys.argv[3])
    quality = request["quality"]
    fmt = "bv*+ba/b" if quality == "max" else f"bv*[height<=?{quality}]+ba/b[height<=?{quality}]"
    config.set(("output",), "mode", "null")
    config.set(("cache",), "file", ":memory:")
    for key, value in {
        "base-directory": directory, "directory": [],
        "filename": "{snatcher_index:03}.{extension}",
        "retries": 1, "timeout": 20, "sleep-request": 0,
        "videos": "ytdl", "audio": False, "previews": False,
        "cookies": None, "postprocessors": [],
    }.items():
        config.set(("extractor",), key, value)
    config.set(("downloader", "ytdl"), "module", "yt_dlp")
    config.set(("downloader", "ytdl"), "format", fmt)
    config.set(("downloader", "ytdl"), "raw-options", {
        "format_sort": ["vcodec:h264", "acodec:aac"],
        "js_runtimes": {"node": {}}, "remote_components": [],
        "cachedir": False, "socket_timeout": 20, "retries": 1,
    })
    ext = extractor.find(request["url"])
    if ext is None:
        return 3  # Only unsupported URLs fall through to yt-dlp.
    if ext.subcategory not in CATEGORIES:
        return 4
    paths = []
    pending = []
    count = 0
    rejected = False

    class Download(job.DownloadJob):
        def __init__(self, url, parent=None):
            super().__init__(url, parent)
            self.depth = parent.depth + 1 if parent else 0
            if self.depth > 3 or self.extractor.subcategory not in CATEGORIES:
                raise ValueError("Only individual posts and galleries are supported")

        def handle_url(self, url, metadata):
            nonlocal count, rejected
            count += 1
            if count > limit:
                rejected = True
                raise ValueError("Gallery exceeds the file limit")
            metadata["snatcher_index"] = count
            pending.append((self, url, metadata.copy()))

        def download_item(self, url, metadata):
            super().handle_url(url, metadata)
            path = self.pathfmt.realpath
            if not os.path.isfile(path):
                raise ValueError("Gallery item did not download")
            paths.append(path)

    with contextlib.redirect_stdout(sys.stderr):
        status = Download(ext).run()
    if rejected:
        return 4
    if status or not pending:
        return 4 if pending else 1
    images = {"jpg", "jpeg", "png", "webp", "gif", "avif"}
    if len(pending) == 1 and not any(str(meta.get("extension", "")).lower() in images for _, _, meta in pending):
        return 3  # Preserve yt-dlp quality/audio handling for video-only posts.
    try:
        with contextlib.redirect_stdout(sys.stderr):
            for task, url, metadata in pending:
                task.download_item(url, metadata)
                if task.status:
                    return 4
    except Exception:
        return 4
    print(json.dumps(paths))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:
        print(type(exc).__name__ + ": " + str(exc), file=sys.stderr)
        sys.exit(1)

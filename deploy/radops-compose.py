#!/usr/bin/env python3
"""RadOps wrapper: keep provider credentials out of the checkout and shell."""
import os
import subprocess
import sys
from pathlib import Path

root = Path("/var/snap/docker/common/radchat")
env = dict(os.environ)
for line in Path("/etc/radchat/environment").read_text().splitlines():
    if line.strip() and not line.lstrip().startswith("#"):
        key, value = line.split("=", 1)
        env[key.strip()] = value.strip().strip("'\"")
env["RADCHAT_IMAGE"] = (root / "image").read_text().strip()
sys.exit(subprocess.call(
    ["/snap/bin/docker", "compose", "--project-directory", str(root),
     "-f", str(root / "compose.yaml"), *sys.argv[1:]], env=env))
